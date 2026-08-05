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

func TestLibretroFlipRows(t *testing.T) {
	// 2x2, bottom-up. Row 0 is the bottom of the picture.
	src := []uint8{
		1, 2, 3, 4, 5, 6, 7, 8, // bottom row
		9, 10, 11, 12, 13, 14, 15, 16, // top row
	}
	dst := make([]uint8, len(src))
	libretroFlipRows(dst, src, 2, 2)
	want := []uint8{
		9, 10, 11, 12, 13, 14, 15, 16,
		1, 2, 3, 4, 5, 6, 7, 8,
	}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("byte %d = %d, want %d (%v)", i, dst[i], want[i], dst)
		}
	}
}

func TestLibretroResolutionSize(t *testing.T) {
	for _, c := range []struct {
		value  string
		gw, gh int32
		w, h   int
		ok     bool
	}{
		{"240p", 320, 240, 320, 240, true},
		{"480p", 640, 480, 640, 480, true},
		{"480p", 1280, 720, 854, 480, true}, // widescreen pack, width rounded even
		{"720p", 1280, 720, 1280, 720, true},
		{"1080p", 0, 0, 1440, 1080, true}, // no game size yet: assume 4:3
		{"Content config", 640, 480, 0, 0, false},
	} {
		w, h, ok := libretroResolutionSize(c.value, c.gw, c.gh)
		if w != c.w || h != c.h || ok != c.ok {
			t.Errorf("libretroResolutionSize(%q, %d, %d) = %d, %d, %v; want %d, %d, %v",
				c.value, c.gw, c.gh, w, h, ok, c.w, c.h, c.ok)
		}
	}
}

func TestLibretroRebasePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "action.zss"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	// Present in the engine tree: rebased.
	if got, want := libretroRebasePath("data/action.zss", root), filepath.Join(root, "data", "action.zss"); got != want {
		t.Errorf("present: got %q, want %q", got, want)
	}
	// Absent from the engine tree (old layout): the content's path survives.
	if got := libretroRebasePath("data/gofx.def", root); got != "data/gofx.def" {
		t.Errorf("absent: got %q, want content path", got)
	}
	// Not engine-owned: untouched.
	if got := libretroRebasePath("chars/foo/foo.def", root); got != "chars/foo/foo.def" {
		t.Errorf("chars: got %q", got)
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

	// Zip extracted into a subfolder: the real game sits one level down.
	outer := t.TempDir()
	nested := filepath.Join(outer, "Game v2")
	if err := os.MkdirAll(filepath.Join(nested, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := libretroGameRoot(outer); got != nested {
		t.Errorf("nested: libretroGameRoot(%q) = %q, want %q", outer, got, nested)
	}
}

func TestLibretroFindMotif(t *testing.T) {
	chdir := func(dir string) {
		t.Helper()
		old, _ := os.Getwd()
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chdir(old) })
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Old Ikemen pack: motif named only by save/config.json.
	root := t.TempDir()
	chdir(root)
	write(filepath.Join(root, "data", "pack", "system.def"), nil)
	write(filepath.Join(root, "save", "config.json"), []byte(`{"Motif":"data/pack/system.def"}`))
	if got := libretroFindMotif(); got != "data/pack/system.def" {
		t.Errorf("config.json: got %q", got)
	}

	// No config: the subfolder glob finds it.
	os.Remove(filepath.Join(root, "save", "config.json"))
	if got := libretroFindMotif(); got != filepath.ToSlash(filepath.Join("data", "pack", "system.def")) {
		t.Errorf("glob: got %q", got)
	}

	// M.U.G.E.N default spot wins over subfolders.
	write(filepath.Join(root, "data", "system.def"), nil)
	if got := libretroFindMotif(); got != "data/system.def" {
		t.Errorf("data/system.def: got %q", got)
	}

	// Nothing anywhere.
	chdir(t.TempDir())
	if got := libretroFindMotif(); got != "" {
		t.Errorf("empty: got %q", got)
	}
}
