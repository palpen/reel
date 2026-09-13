// Package volume resolves mounted macOS volume identity; names are labels only.
package volume

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/pspenano/reel/internal/safefs"
	"golang.org/x/sys/unix"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Identity struct {
	UUID     string
	Device   uint64
	Mount    string
	Physical []string
}

func plist(args ...string) (map[string]any, error) {
	data, err := exec.Command("/usr/sbin/diskutil", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve mounted volume: %w", err)
	}
	cmd := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(data)
	data, err = cmd.Output()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(data, &m)
	return m, err
}
func text(m map[string]any, k string) string { v, _ := m[k].(string); return v }

var disk = regexp.MustCompile(`^disk[0-9]+`)

func Resolve(root string) (Identity, error) {
	d, err := safefs.OpenDir(root, false)
	if err != nil {
		return Identity{}, err
	}
	defer d.Close()
	m, err := plist("info", "-plist", root)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{UUID: text(m, "VolumeUUID"), Mount: text(m, "MountPoint")}
	if id.UUID == "" || filepath.Clean(root) != id.Mount {
		return id, fmt.Errorf("not an identifiable mounted volume: %s", root)
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(d.Fd()), &st); err != nil {
		return id, err
	}
	id.Device = uint64(st.Dev)
	whole := text(m, "ParentWholeDisk")
	if whole == "" {
		whole = text(m, "PartOfWhole")
	}
	if virtual, _ := m["VirtualOrPhysical"].(string); strings.EqualFold(virtual, "Physical") {
		if p := disk.FindString(whole); p != "" {
			id.Physical = []string{p}
		}
	}
	if len(id.Physical) == 0 {
		apfs, e := plist("apfs", "list", "-plist")
		if e == nil {
			containers, _ := apfs["Containers"].([]any)
			for _, v := range containers {
				c, ok := v.(map[string]any)
				if !ok || text(c, "ContainerReference") != whole {
					continue
				}
				stores, _ := c["PhysicalStores"].([]any)
				for _, v := range stores {
					p, ok := v.(map[string]any)
					if ok {
						if name := disk.FindString(text(p, "DeviceIdentifier")); name != "" {
							id.Physical = append(id.Physical, name)
						}
					}
				}
			}
		}
	}
	return id, nil
}
func (i Identity) Check() error {
	n, err := Resolve(i.Mount)
	if err != nil {
		return err
	}
	if i.UUID != n.UUID || i.Device != n.Device {
		return fmt.Errorf("volume changed: %s", i.Mount)
	}
	return nil
}
func Independent(a, b Identity) error {
	if a.UUID == b.UUID || a.Device == b.Device || len(a.Physical) == 0 || len(b.Physical) == 0 {
		return fmt.Errorf("independent physical storage could not be established")
	}
	for _, x := range a.Physical {
		for _, y := range b.Physical {
			if x == y {
				return fmt.Errorf("camera and backup share physical storage")
			}
		}
	}
	return nil
}
func Contains(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("path outside approved root: %s", path)
	}
	d, err := safefs.OpenDir(root, false)
	if err != nil {
		return err
	}
	defer d.Close()
	var rootStat unix.Stat_t
	if err = unix.Fstat(int(d.Fd()), &rootStat); err != nil {
		return err
	}
	parent, err := safefs.OpenBeneath(d, filepath.Dir(rel), false)
	if err != nil {
		return err
	}
	defer parent.Close()
	f, err := safefs.OpenAt(parent, filepath.Base(path), unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	err = unix.Fstat(int(f.Fd()), &st)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if st.Dev != rootStat.Dev {
		return fmt.Errorf("nested mount outside approved device: %s", path)
	}
	return closeErr
}
