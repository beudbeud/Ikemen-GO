//go:build libretro

package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/ini.v1"
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

func TestLibretroSearchOrder(t *testing.T) {
	old := libretroEngineRoot
	t.Cleanup(func() { libretroEngineRoot = old })

	libretroEngineRoot = ""
	if got := libretroSearchOrder("data/system.def"); len(got) != 1 || got[0] != "data/system.def" {
		t.Errorf("no root: got %v", got)
	}

	libretroEngineRoot = "/sys/ikemen"
	rooted := filepath.Join("/sys/ikemen", "data/system.def")
	if got := libretroSearchOrder("data/system.def"); len(got) != 2 || got[0] != "data/system.def" || got[1] != rooted {
		t.Errorf("content-first: got %v", got)
	}
	scripted := filepath.Join("/sys/ikemen", "external/script/default.lua")
	if got := libretroSearchOrder("external/script/default.lua"); len(got) != 2 || got[0] != scripted || got[1] != "external/script/default.lua" {
		t.Errorf("engine-first: got %v", got)
	}
	abs := filepath.Join("/abs", "x.def")
	if got := libretroSearchOrder(abs); len(got) != 1 || got[0] != abs {
		t.Errorf("absolute: got %v", got)
	}
}

func TestLibretroSpriteShrinkFactor(t *testing.T) {
	for _, c := range []struct{ gameH, outH, want int32 }{
		{720, 480, 2},  // 720p pack on a 480p CRT
		{720, 720, 1},  // native
		{720, 1080, 1}, // upscaled output never shrinks
		{1080, 480, 3},
		{720, 240, 3},
		{1080, 240, 4}, // clamped: ceil(4.5) -> 4
		{0, 480, 1},    // no game size yet
	} {
		if got := libretroSpriteShrinkFactor(c.gameH, c.outH); got != c.want {
			t.Errorf("factor(%d, %d) = %d, want %d", c.gameH, c.outH, got, c.want)
		}
	}
}

func TestLibretroShrinkSprite(t *testing.T) {
	old := libretroSpriteShrink
	t.Cleanup(func() { libretroSpriteShrink = old })
	libretroSpriteShrink = 2

	// Below the size threshold: untouched.
	small := []byte{1, 2, 3, 4}
	if out, w, h := libretroShrinkSprite(small, 2, 2, 1); w != 2 || h != 2 || &out[0] != &small[0] {
		t.Errorf("small: got %dx%d", w, h)
	}

	// Indexed 512x512: decimated to 256x256, top-left texel of each block kept.
	w, h := int32(512), int32(512)
	idx := make([]byte, w*h)
	idx[0], idx[2] = 7, 9 // texels (0,0) and (2,0)
	out, ow, oh := libretroShrinkSprite(idx, w, h, 1)
	if ow != 256 || oh != 256 || out[0] != 7 || out[1] != 9 {
		t.Errorf("indexed: %dx%d out[0]=%d out[1]=%d", ow, oh, out[0], out[1])
	}

	// RGBA 512x512: alpha-weighted, so a transparent black texel in the block
	// does not darken the opaque red one.
	rgba := make([]byte, w*h*4)
	set := func(x, y int32, r, g, b, a byte) {
		i := (y*w + x) * 4
		rgba[i], rgba[i+1], rgba[i+2], rgba[i+3] = r, g, b, a
	}
	set(0, 0, 255, 0, 0, 255) // opaque red; the 3 other texels transparent black
	out, ow, oh = libretroShrinkSprite(rgba, w, h, 4)
	if ow != 256 || oh != 256 {
		t.Fatalf("rgba: got %dx%d", ow, oh)
	}
	if out[0] != 255 || out[3] != 63 {
		t.Errorf("rgba: got r=%d a=%d, want r=255 a=63", out[0], out[3])
	}
}

func TestLibretroDefaultCommon(t *testing.T) {
	defaults, err := ini.Load([]byte(
		"[Common]\nStates = a.zss, b.zss\nFx = data/gofx/gofx.def\nModules = \nLua = loop()\n"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	cfg.DefaultOnlyIni = defaults
	cfg.Common.States = map[string][]string{"States": {"data/old.zss"}}
	cfg.Common.Fx = map[string][]string{"Fx": {"data/inputdisplay.def"}}

	libretroDefaultCommon(&cfg)

	if got := cfg.Common.States["States"]; len(got) != 2 || got[0] != "a.zss" || got[1] != "b.zss" {
		t.Errorf("States: got %v", got)
	}
	if got := cfg.Common.Fx["Fx"]; len(got) != 1 || got[0] != "data/gofx/gofx.def" {
		t.Errorf("Fx: got %v", got)
	}
	if got := cfg.Common.Modules; len(got["Modules"]) != 0 {
		t.Errorf("Modules: got %v", got)
	}
	if got := cfg.Common.Lua["Lua"]; len(got) != 1 || got[0] != "loop()" {
		t.Errorf("Lua: got %v", got)
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
