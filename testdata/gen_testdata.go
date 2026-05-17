//go:build ignore

// gen_testdata.go generates synthetic test fixture files.
// Run with: go run testdata/gen_testdata.go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

func main() {
	type fixture struct {
		name    string
		size    int
		content func(i int) byte
	}

	fixtures := []fixture{
		{
			name: "sample.MP4",
			size: 1024,
			content: func(i int) byte {
				// Deterministic: repeating pattern based on index
				return byte((i*17 + 42) % 256)
			},
		},
		{
			name: "sample.LRF",
			size: 512,
			content: func(i int) byte {
				return byte((i*13 + 7) % 256)
			},
		},
		{
			name: "sample.WAV",
			size: 256,
			content: func(i int) byte {
				return byte((i*11 + 3) % 256)
			},
		},
	}

	for _, fix := range fixtures {
		data := make([]byte, fix.size)
		for i := range data {
			data[i] = fix.content(i)
		}
		path := fix.name
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
			os.Exit(1)
		}
		h := sha256.Sum256(data)
		fmt.Printf("%s: SHA256=%s size=%d\n", fix.name, hex.EncodeToString(h[:]), fix.size)
	}

	// corrupt_sample.MP4: same as sample.MP4 but byte[0] flipped
	mp4data := make([]byte, 1024)
	for i := range mp4data {
		mp4data[i] = byte((i*17 + 42) % 256)
	}
	corrupt := make([]byte, len(mp4data))
	copy(corrupt, mp4data)
	corrupt[0] ^= 0xFF // flip all bits of byte 0

	if err := os.WriteFile("corrupt_sample.MP4", corrupt, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write corrupt_sample.MP4: %v\n", err)
		os.Exit(1)
	}
	ch := sha256.Sum256(corrupt)
	fmt.Printf("corrupt_sample.MP4: SHA256=%s size=%d\n", hex.EncodeToString(ch[:]), len(corrupt))
}
