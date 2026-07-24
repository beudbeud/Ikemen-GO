//go:build libretro

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLibretroConvertFrame(t *testing.T) {
	// 2x2, bottom-up RGBA. Row 0 is the bottom of the picture.
	src := []uint8{
		1, 2, 3, 0, 4, 5, 6, 0, // bottom row
		7, 8, 9, 0, 10, 11, 12, 0, // top row
	}
	dst := make([]uint8, len(src))
	libretroConvertFrame(dst, src, 2, 2, true)

	// Top row first, and R/B swapped with X forced opaque.
	want := []uint8{
		9, 8, 7, 255, 12, 11, 10, 255,
		3, 2, 1, 255, 6, 5, 4, 255,
	}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("flipped: byte %d = %d, want %d (%v)", i, dst[i], want[i], dst)
		}
	}

	libretroConvertFrame(dst, src, 2, 2, false)
	if dst[0] != 3 || dst[3] != 255 {
		t.Fatalf("unflipped: got %v", dst)
	}
}

func TestLibretroGameRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "save"), 0755); err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(root, "data", "system.def")
	if err := os.WriteFile(def, nil, 0644); err != nil {
		t.Fatal(err)
	}

	for _, in := range []string{root, def, filepath.Join(root, "save", "config.ini")} {
		if got := libretroGameRoot(in); got != root {
			t.Errorf("libretroGameRoot(%q) = %q, want %q", in, got, root)
		}
	}
}
