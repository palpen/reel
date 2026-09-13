package cmd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pspenano/reel/internal/display"
	"github.com/pspenano/reel/internal/lockfile"
	"github.com/pspenano/reel/internal/state"
	"github.com/pspenano/reel/internal/transfer"
)

// RunStatus implements `reel status`.
func RunStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit structured JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unsupported arguments: %v", fs.Args())
	}

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfgDir, err := configDir()
	if err != nil {
		return err
	}
	lk, err := lockfile.AcquireShared(filepath.Join(cfgDir, "reel.lock"))
	if err != nil {
		return err
	}
	defer lk.Release()

	st, err := state.Load(filepath.Join(cfgDir, "state.jsonl"))
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	cameras, err := detectCameras(cfg.Cameras)
	if err != nil {
		return fmt.Errorf("detect cameras: %w", err)
	}

	type cameraStatus struct {
		Connected bool   `json:"connected"`
		Volume    string `json:"volume,omitempty"`
		FileCount int    `json:"file_count,omitempty"`
		TotalGB   string `json:"total_gb,omitempty"`
	}
	type laptopStatus struct {
		Dir     string `json:"dir"`
		Folders int    `json:"folders"`
		Files   int    `json:"files"`
		TotalGB string `json:"total_gb"`
	}
	type hdStatus struct {
		Copies           map[string]int `json:"copies"`
		Connected        bool           `json:"connected"`
		ManagedSizeGB    string         `json:"managed_size_gb,omitempty"`
		StaleVerifyCount int            `json:"stale_verify_count"`
		LastVerifiedAt   time.Time      `json:"last_verified_at,omitempty"`
	}
	type statusOut struct {
		Camera           cameraStatus `json:"camera"`
		Laptop           laptopStatus `json:"laptop"`
		HD               hdStatus     `json:"hd"`
		LastImportAt     *time.Time   `json:"last_import_at"`
		LastBackupAt     *time.Time   `json:"last_backup_at"`
		LastCleanAt      *time.Time   `json:"last_clean_at"`
		TotalTracked     int          `json:"total_tracked"`
		RetainedPartials []string     `json:"retained_partials"`
	}

	out := statusOut{TotalTracked: st.Len()}

	// Camera
	if len(cameras) > 0 {
		dc := &cameras[0]
		files, _ := dc.Walk()
		var totalSize int64
		for _, f := range files {
			totalSize += f.Size
		}
		out.Camera = cameraStatus{
			Connected: true,
			Volume:    dc.VolumePath,
			FileCount: len(files),
			TotalGB:   display.Bytes(totalSize),
		}
	} else {
		out.Camera = cameraStatus{Connected: false}
	}

	// Laptop
	{
		var folders, files int
		var total int64
		entries, err := os.ReadDir(cfg.LaptopDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					folders++
					sub := filepath.Join(cfg.LaptopDir, e.Name())
					filepath.WalkDir(sub, func(p string, d os.DirEntry, err error) error {
						if err == nil && !d.IsDir() {
							files++
							if info, e := d.Info(); e == nil {
								total += info.Size()
							}
						}
						return nil
					})
				}
			}
		}
		out.Laptop = laptopStatus{
			Dir:     cfg.LaptopDir,
			Folders: folders,
			Files:   files,
			TotalGB: display.Bytes(total),
		}
	}

	// HD
	{
		_, hdErr := approvedHD(cfg)
		hdConnected := hdErr == nil
		copies := map[string]int{}
		var managedSize int64
		var staleCount int
		var lastVerified time.Time
		staleThreshold := 7 * 24 * time.Hour
		now := time.Now()
		for _, r := range st.All() {
			copies[copyStatus(r)]++
			if r.HDPath != "" {
				if info, err := os.Stat(r.HDPath); err == nil {
					managedSize += info.Size()
				}
			}
			if r.HDVerifiedAt != nil {
				if now.Sub(*r.HDVerifiedAt) > staleThreshold {
					staleCount++
				}
				if r.HDVerifiedAt.After(lastVerified) {
					lastVerified = *r.HDVerifiedAt
				}
			}
		}
		out.HD = hdStatus{
			Copies:           copies,
			Connected:        hdConnected,
			ManagedSizeGB:    display.Bytes(managedSize),
			StaleVerifyCount: staleCount,
			LastVerifiedAt:   lastVerified,
		}
	}

	for _, root := range []string{cfg.LaptopDir, hdManaged(cfg)} {
		paths, err := transfer.SweepOrphanTmps(root)
		if err == nil {
			out.RetainedPartials = append(out.RetainedPartials, paths...)
		}
	}
	// Last import/backup/clean
	for _, r := range st.All() {
		if r.ImportedAt != nil && (out.LastImportAt == nil || r.ImportedAt.After(*out.LastImportAt)) {
			t := *r.ImportedAt
			out.LastImportAt = &t
		}
		if r.BackedUpAt != nil && (out.LastBackupAt == nil || r.BackedUpAt.After(*out.LastBackupAt)) {
			t := *r.BackedUpAt
			out.LastBackupAt = &t
		}
		if r.CleanedAt != nil && (out.LastCleanAt == nil || r.CleanedAt.After(*out.LastCleanAt)) {
			t := *r.CleanedAt
			out.LastCleanAt = &t
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable
	fmt.Println("=== reel status ===")
	fmt.Printf("\nCamera:  ")
	if out.Camera.Connected {
		fmt.Printf("connected (%s) — %d files, %s\n", out.Camera.Volume, out.Camera.FileCount, out.Camera.TotalGB)
	} else {
		fmt.Println("not connected")
	}

	fmt.Printf("Laptop:  %s — %d folders, %d files, %s\n",
		out.Laptop.Dir, out.Laptop.Folders, out.Laptop.Files, out.Laptop.TotalGB)

	fmt.Printf("HD:      ")
	if out.HD.Connected {
		fmt.Printf("connected — managed: %s", out.HD.ManagedSizeGB)
		if out.HD.StaleVerifyCount > 0 {
			fmt.Printf(", %d files with stale verification", out.HD.StaleVerifyCount)
		}
		fmt.Println()
	} else {
		fmt.Println("not connected")
	}

	for _, path := range out.RetainedPartials {
		fmt.Printf("Retained partial (never automatically removed): %s\n", path)
	}
	fmt.Printf("HD copies: %v\n", out.HD.Copies)
	fmt.Printf("\nTracked files: %d\n", out.TotalTracked)
	if out.LastImportAt != nil {
		fmt.Printf("Last import:   %s\n", out.LastImportAt.Local().Format(time.RFC3339))
	}
	if out.LastBackupAt != nil {
		fmt.Printf("Last backup:   %s\n", out.LastBackupAt.Local().Format(time.RFC3339))
	}
	if out.LastCleanAt != nil {
		fmt.Printf("Last clean:    %s\n", out.LastCleanAt.Local().Format(time.RFC3339))
	}

	return nil
}

func copyStatus(r *state.Row) string {
	if r.HDPath == "" {
		return "absent"
	}
	h, n, err := transfer.HashFile(r.HDPath)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}
	if err != nil {
		return "conflicting"
	}
	if r.SHA256 == "" || r.HDVolumeUUID == "" || r.HDVerifiedAt == nil {
		return "unverified"
	}
	if h != r.SHA256 || n != r.SizeBytes {
		return "conflicting"
	}
	if time.Since(*r.HDVerifiedAt) > defaultStaleThreshold {
		return "stale"
	}
	return "verified"
}
