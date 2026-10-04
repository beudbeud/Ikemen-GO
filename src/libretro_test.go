//go:build libretro

package main

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{1080, 240, 4}, // ceil(4.5) = 5, clamped to 4
		{0, 480, 1},    // no game size yet
	} {
		if got := libretroSpriteShrinkFactor(c.gameH, c.outH); got != c.want {
			t.Errorf("factor(%d, %d) = %d, want %d", c.gameH, c.outH, got, c.want)
		}
	}
}

func TestLibretroShrinkSprite(t *testing.T) {
	old, oldIdx, oldGameH := libretroSpriteShrink, libretroShrinkIndexed, libretroShrinkGameH
	t.Cleanup(func() {
		libretroSpriteShrink, libretroShrinkIndexed, libretroShrinkGameH = old, oldIdx, oldGameH
	})
	libretroSpriteShrink, libretroShrinkIndexed, libretroShrinkGameH = 2, true, 0

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

	// Auto mode spares pixel art: indexed sprites pass through untouched.
	libretroShrinkIndexed = false
	if _, w, h := libretroShrinkSprite(idx, 512, 512, 1); w != 512 || h != 512 {
		t.Errorf("indexed spared: got %dx%d", w, h)
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

	// Auto's per-sprite HD check: no global shrink, but a true-color sprite
	// taller than the 480p frame gets its own divisor; indexed stays spared.
	libretroSpriteShrink, libretroShrinkIndexed, libretroShrinkGameH = 1, false, 480
	if _, ow, oh := libretroShrinkSprite(rgba, w, h, 4); ow != 256 || oh != 256 {
		t.Errorf("hd rgba: got %dx%d", ow, oh)
	}
	if _, ow, oh := libretroShrinkSprite(idx, w, h, 1); ow != 512 || oh != 512 {
		t.Errorf("hd indexed spared: got %dx%d", ow, oh)
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

func TestSffCacheRoundTrip(t *testing.T) {
	// Both ways a recorded load can end: texels on the heap until the file is
	// stored (write-behind), or no heap budget and the file mapped at once.
	for _, budget := range []int64{3 << 30, 0} {
		t.Run(fmt.Sprintf("budget%d", budget), func(t *testing.T) { testSffCacheRoundTrip(t, budget) })
	}
}

func testSffCacheRoundTrip(t *testing.T, available int64) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	// The cache is libretro-only; fake being a core for the test.
	oldPresent, oldAvail := libretroPresent, libretroMemAvailable
	libretroPresent = func() {}
	libretroMemAvailable = func() int64 { return available }
	t.Cleanup(func() { libretroPresent, libretroMemAvailable = oldPresent, oldAvail })

	// A fake source file the cache validates against.
	src := "fake.sff"
	if err := os.WriteFile(src, []byte("not a real sff"), 0644); err != nil {
		t.Fatal(err)
	}

	// Build an Sff the way loadSff would leave it.
	s := newSff()
	s.filename = src
	s.header.NumberOfSprites = 4
	s.palList.SetSource(0, []uint32{0xff00ff00, 0x11223344})
	s.palList.PalTable[[2]uint16{1, 1}] = 0
	s.palList.numcols[[2]uint16{1, 1}] = 2
	s.palList.duplicatePals[0] = []int{3, 5}

	mk := func(g, n uint16) *Sprite {
		spr := newSprite()
		spr.Group, spr.Number = g, n
		spr.Size = [2]uint16{4, 2}
		spr.Offset = [2]int16{-3, 7}
		spr.palidx = 0
		spr.coldepth = 8
		return spr
	}
	// File order is not group order: the prefetch queue sorts by group, and
	// every texture must still end up with its own texels.
	list := []*Sprite{mk(7, 0), mk(7, 1), mk(9000, 0), mk(0, 0)}
	links := []int32{-1, 0, -1, -1} // sprite 1 shares sprite 0's texture
	for _, spr := range list {
		s.sprites[[2]uint16{spr.Group, spr.Number}] = spr
	}

	// mainThreadTask must be drainable or the load blocks.
	old := sys.mainThreadTask
	sys.mainThreadTask = make(chan func(), 16)
	t.Cleanup(func() { sys.mainThreadTask = old })

	// Capture through the real pipeline so the entry file is exercised too.
	if !sffCacheBegin() {
		t.Fatal("sffCacheBegin refused")
	}
	for _, spr := range list {
		sffCaptureExpect(spr)
	}
	texels, texels3 := []byte{1, 2, 3, 4, 5, 6, 7, 8}, []byte{9, 8, 7, 6, 5, 4, 3, 2}
	trim := [2][4]float32{{1, 0, 3, 2}}
	if !sffCaptureAdd(list[0], texels, 4, 2, 8, trim) || !sffCaptureAdd(list[3], texels3, 4, 2, 8, trim) {
		t.Fatal("the recording load's sprites were not captured")
	}
	if sffCaptureAdd(newSprite(), []byte{9, 9}, 2, 1, 8, trim) {
		t.Fatal("another loader's sprite was captured")
	}
	// list[1] is a link, list[2] stays blank

	// The recorded load itself: no upload, its sprites read the cache's texels.
	if !sffCacheFinish(src, true, false, s, list, links) {
		t.Fatal("sffCacheFinish refused")
	}
	(<-sys.mainThreadTask)()
	l, l3 := list[0].lazy, list[3].lazy
	if l == nil || l3 == nil || !bytes.Equal(l.data, texels) || !bytes.Equal(l3.data, texels3) ||
		list[0].trim != trim || list[1].lazy != l || list[2].lazy != nil {
		t.Fatalf("first load not lazy on the cache's texels: %+v %+v", l, l3)
	}
	if onHeap := available > 0; onHeap != (l.keep == nil) {
		t.Fatalf("texels on the heap: %v, want %v", l.keep == nil, onHeap)
	}
	// Once the writer has stored the file, heap texels become the mapped ones.
	sffCacheFlush()
	if available > 0 {
		(<-sys.mainThreadTask)()
	}
	if l.keep == nil || l3.keep == nil || !bytes.Equal(l.data, texels) || !bytes.Equal(l3.data, texels3) {
		t.Fatalf("texels not handed over to the mapped file, each sprite its own: %+v %+v", l, l3)
	}
	if sffHeld != 0 {
		t.Fatalf("%d bytes still counted on the heap", sffHeld)
	}

	got := sffCacheLoad(src, true, false)
	if got == nil {
		t.Fatal("cache miss after store")
	}
	if got.header.NumberOfSprites != 4 || len(got.sprites) != 4 {
		t.Fatalf("header/sprites: %d/%d", got.header.NumberOfSprites, len(got.sprites))
	}
	if l := got.sprites[[2]uint16{0, 0}].lazy; l == nil || !bytes.Equal(l.data, texels3) {
		t.Fatalf("second sprite's texels: %+v", l)
	}
	spr := got.sprites[[2]uint16{7, 0}]
	if spr == nil || spr.Size != [2]uint16{4, 2} || spr.Offset != [2]int16{-3, 7} || spr.palidx != 0 {
		t.Fatalf("sprite fields: %+v", spr)
	}
	// Mapped: texels left in the file until first drawn, shared by the link.
	if l := spr.lazy; l == nil || !bytes.Equal(l.data, texels) ||
		l.w != 4 || l.h != 2 || l.depth != 8 || l.keep == nil {
		t.Fatalf("lazy texels: %+v", spr.lazy)
	}
	if linked := got.sprites[[2]uint16{7, 1}]; linked.lazy != spr.lazy || linked.trim != trim {
		t.Error("linked sprite does not share the lazy texels and their trim")
	}
	if spr.trim != trim {
		t.Errorf("trim boxes lost: %v", spr.trim)
	}
	// Texels are on disk once: magic, the two blobs, then the table.
	if st, err := os.Stat(sffCachePath(src, true, false)); err != nil {
		t.Error(err)
	} else if st.Size() > 500 {
		t.Errorf("entry file is %d bytes", st.Size())
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, "ikemen-go", "*")); len(tmps) != 1 {
		t.Errorf("leftover files in the cache directory: %v", tmps)
	}
	if got.sprites[[2]uint16{9000, 0}].lazy != nil {
		t.Error("blank sprite got lazy texels")
	}
	if n := len(sys.mainThreadTask); n != 0 {
		t.Errorf("%d uploads queued, want none", n)
	}
	if p := got.palList.Get(0); len(p) != 2 || p[0] != 0xff00ff00 {
		t.Errorf("palette: %v", p)
	}
	if got.palList.numcols[[2]uint16{1, 1}] != 2 {
		t.Errorf("numcols lost")
	}
	if d := got.palList.duplicatePals[0]; len(d) != 2 || d[0] != 3 || d[1] != 5 {
		t.Errorf("duplicatePals lost: %v (RemapPal follows them)", got.palList.duplicatePals)
	}

	// Wrong flags -> different key -> miss.
	if sffCacheLoad(src, false, false) != nil {
		t.Error("flag change should miss")
	}
	// Touch the source -> stale -> miss and self-clean.
	if err := os.WriteFile(src, []byte("changed!"), 0644); err != nil {
		t.Fatal(err)
	}
	if sffCacheLoad(src, true, false) != nil {
		t.Error("stale cache should miss")
	}
}

// IKEMEN_TEST_SFF=<a real .sff>: what the recorded load holds on the heap, what
// the stored file holds once the sprites are handed over to it, and what a
// cached load reads back must be the same texels, sprite for sprite.
func TestSffCacheRealFile(t *testing.T) {
	src := os.Getenv("IKEMEN_TEST_SFF")
	if src == "" {
		t.Skip("IKEMEN_TEST_SFF not set")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	oldPresent, oldAvail := libretroPresent, libretroMemAvailable
	libretroPresent = func() {}
	libretroMemAvailable = func() int64 { return 12 << 30 }
	t.Cleanup(func() { libretroPresent, libretroMemAvailable = oldPresent, oldAvail })
	old := sys.mainThreadTask
	sys.mainThreadTask = make(chan func(), 1<<16)
	t.Cleanup(func() { sys.mainThreadTask = old })
	drain := func() {
		for len(sys.mainThreadTask) > 0 {
			(<-sys.mainThreadTask)()
		}
	}
	s, err := loadSff(src, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	drain()
	sums := map[*lazyTex][sha1.Size]byte{}
	for _, spr := range s.sprites {
		if l := spr.lazy; l != nil {
			if l.keep != nil {
				t.Fatal("texels not on the heap")
			}
			sums[l] = sha1.Sum(l.data)
		}
	}
	sffCacheFlush()
	drain()
	for l, sum := range sums {
		if l.keep == nil || sha1.Sum(l.data) != sum {
			t.Fatalf("a %dx%d texture changed between the heap and the file (mapped: %v)", l.w, l.h, l.keep != nil)
		}
	}
	c := sffCacheLoad(src, true, false)
	if c == nil {
		t.Fatal("cache miss")
	}
	for k, spr := range s.sprites {
		a, b := spr.lazy, c.sprites[k].lazy
		if (a == nil) != (b == nil) || a != nil && sha1.Sum(a.data) != sha1.Sum(b.data) {
			t.Fatalf("sprite %v differs between the recorded load and the cached load", k)
		}
	}
	t.Logf("%d sprites, %d textures", len(s.sprites), len(sums))
}

func TestLibretroSffCacheEvict(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, size int, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		os.Chtimes(p, when, when)
		return p
	}
	oldest := mk("a.sfc", 40, 3*time.Hour)
	older := mk("b.sfc", 40, 2*time.Hour)
	recent := mk("c.sfc", 40, time.Hour)
	fresh := mk("d.sfc", 40, 0)
	other := mk("notes.txt", 1000, 5*time.Hour) // not an entry: never counted or removed
	oldFree := sffCacheFree
	t.Cleanup(func() { sffCacheFree = oldFree })
	sffCacheFree = func(string) int64 { return -1 } // unknown: the cap alone decides

	sffCacheEvict(dir, 100, fresh)
	for p, want := range map[string]bool{oldest: false, older: false, recent: true, fresh: true, other: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}

	// The entry just written survives even when it alone is over the cap.
	sffCacheEvict(dir, 10, fresh)
	if _, err := os.Stat(fresh); err != nil {
		t.Error("kept entry was evicted")
	}
	if _, err := os.Stat(recent); err == nil {
		t.Error("older entry should go when over the cap")
	}

	// Under the cap, but the disk is short of its reserve by 50 bytes: the
	// oldest entries go until those 50 are back.
	a, b, c := mk("a.sfc", 40, 3*time.Hour), mk("b.sfc", 40, 2*time.Hour), mk("c.sfc", 40, time.Hour)
	sffCacheFree = func(string) int64 { return sffCacheReserve - 50 }
	sffCacheEvict(dir, 1<<20, fresh)
	for p, want := range map[string]bool{a: false, b: false, c: true, fresh: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("reserve: %s exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
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

func TestLibretroQueueRumble(t *testing.T) {
	lr.rumble[1].Store(0)
	libretroQueueRumble(1, 0x1234, 0xBEEF, 60)
	v := lr.rumble[1].Load()
	if v&1 == 0 {
		t.Fatal("dirty bit not set")
	}
	if lo := uint16(v >> 48); lo != 0x1234 {
		t.Errorf("lo: got %#x", lo)
	}
	if hi := uint16(v >> 32); hi != 0xBEEF {
		t.Errorf("hi: got %#x", hi)
	}
	if ticks := uint32(v>>1) & 0x7fffffff; ticks != 60 {
		t.Errorf("ticks: got %d", ticks)
	}
	// Out-of-range ports must not panic or write anywhere.
	libretroQueueRumble(-1, 1, 1, 1)
	libretroQueueRumble(len(lr.rumble), 1, 1, 1)
	lr.rumble[1].Store(0)
}

// A pack's config.ini in the old numeric joystick format (Ultimate Cosmos
// ships one) must not reach the live input tables: they are built inside
// loadConfig, so the core's forced mapping has to be applied before that.
func TestLibretroForceInputReachesLiveTables(t *testing.T) {
	initLUTs()
	path := filepath.Join(t.TempDir(), "config.ini")
	pack := "[Config]\nPlayers = 4\n[Joystick_P1]\nJoystick = 0\nUp = 10\nA = 0\nB = 1\nStart = 7\nMenu = 6\n"
	if err := os.WriteFile(path, []byte(pack), 0644); err != nil {
		t.Fatal(err)
	}
	saved := libretroConfigOverride
	defer func() { libretroConfigOverride = saved }()
	libretroConfigOverride = nil
	libretroForceInput()
	keys, joys := sys.keyConfig, sys.joystickConfig
	t.Cleanup(func() { sys.keyConfig, sys.joystickConfig = keys, joys })
	sys.keyConfig, sys.joystickConfig = nil, nil
	if _, err := loadConfig(path); err != nil {
		t.Fatal(err)
	}
	j := sys.joystickConfig[0]
	for name, got := range map[string]int{"DP_U": j.dU, "A": j.bA, "B": j.bB, "START": j.bS, "BACK": j.bM} {
		if want := StringToButtonLUT[name]; got != want {
			t.Errorf("%s: live button %d, want %d", name, got, want)
		}
	}
}

func TestLibretroEnvArgs(t *testing.T) {
	t.Setenv("IKEMEN_ARGS", "-p1 Kfm -p2 Kfm -s stages/kfm.def -p1.ai 8 -nosound")
	flags := sys.cmdFlags
	t.Cleanup(func() { sys.cmdFlags = flags })
	sys.cmdFlags = nil
	libretroEnvArgs()
	want := map[string]string{"-p1": "Kfm", "-p2": "Kfm", "-s": "stages/kfm.def", "-p1.ai": "8", "-nosound": ""}
	if len(sys.cmdFlags) != len(want) {
		t.Fatalf("got %v, want %v", sys.cmdFlags, want)
	}
	for k, v := range want {
		if sys.cmdFlags[k] != v {
			t.Fatalf("%s = %q, want %q (%v)", k, sys.cmdFlags[k], v, sys.cmdFlags)
		}
	}
}

func TestLibretroParseRange(t *testing.T) {
	for _, c := range []struct {
		in       string
		from, to uint64
		ok       bool
	}{
		{"600:2400", 600, 2400, true},
		{"1:2", 1, 2, true},
		{"0:1", 0, 1, false},      // frames count from 1
		{"2400:600", 0, 0, false}, // empty window
		{"600", 0, 0, false},
		{"a:b", 0, 0, false},
	} {
		from, to, ok := libretroParseRange(c.in)
		if ok != c.ok || (ok && (from != c.from || to != c.to)) {
			t.Errorf("%q: got %d,%d,%v want %d,%d,%v", c.in, from, to, ok, c.from, c.to, c.ok)
		}
	}
}

func TestLibretroBenchSummary(t *testing.T) {
	// 100 frames: steps 1..100ms, so p50 is the 51st value and p95 the 96th.
	steps := make([]time.Duration, 100)
	draws := make([]uint64, 100)
	for i := range steps {
		steps[i] = time.Duration(i+1) * time.Millisecond
		draws[i] = uint64(i)
	}
	got := libretroBenchSummary(steps, draws, 2*time.Second)
	for _, want := range []string{
		"frames=100", "fps=50.00", "step_ms_mean=50.500",
		"step_ms_p50=51.000", "step_ms_p95=96.000", "step_ms_max=100.000",
		"draws_mean=49.5", "draws_max=99",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
	if got := libretroBenchSummary(nil, nil, time.Second); !strings.Contains(got, "frames=0") {
		t.Errorf("empty window: %q", got)
	}
}

func TestLibretroWritePPM(t *testing.T) {
	// 2x2 bottom-up RGBA; the PPM must be top-down RGB with alpha dropped.
	rgba := []uint8{
		1, 2, 3, 99, 4, 5, 6, 99, // bottom row
		7, 8, 9, 99, 10, 11, 12, 99, // top row
	}
	path := filepath.Join(t.TempDir(), "f.ppm")
	if err := libretroWritePPM(path, rgba, 2, 2); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte("P6\n2 2\n255\n"), 7, 8, 9, 10, 11, 12, 1, 2, 3, 4, 5, 6)
	if string(got) != string(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSpriteTrim(t *testing.T) {
	px := make([]byte, 10*10)
	px[3*10+4], px[5*10+6] = 7, 1 // figure spans x 4..6, y 3..5
	// Padded by a texel: x 3..7, y 2..6.
	if got, want := spriteTrim(px, 10, 10, 1, 1), [4]float32{0.3, 0.2, 0.8, 0.7}; got != want {
		t.Errorf("trim = %v, want %v", got, want)
	}
	if got := spriteTrim(make([]byte, 100), 10, 10, 1, 1); got != [4]float32{} {
		t.Errorf("blank sprite: trim = %v, want none", got)
	}
	px[0], px[99] = 1, 1 // figure now fills the canvas: nothing to save
	if got := spriteTrim(px, 10, 10, 1, 1); got != [4]float32{} {
		t.Errorf("full sprite: trim = %v, want none", got)
	}
	// RGBA: only an all-zero texel is empty -- invisible colour still counts.
	rgba := make([]byte, 10*10*4)
	rgba[(3*10+4)*4+3] = 255 // opaque black at (4,3)
	rgba[(5*10+6)*4+0] = 9   // zero alpha, non-zero red at (6,5)
	if got, want := spriteTrim(rgba, 10, 10, 4, 4), [4]float32{0.3, 0.2, 0.8, 0.7}; got != want {
		t.Errorf("rgba trim = %v, want %v", got, want)
	}
	// Black box ignores alpha: only the red texel at (6,5) counts, padded to
	// x 5..7, y 4..6 -- a 3x3 box, well under three quarters of the canvas.
	if got, want := spriteTrim(rgba, 10, 10, 4, 3), [4]float32{0.5, 0.4, 0.8, 0.7}; got != want {
		t.Errorf("black trim = %v, want %v", got, want)
	}
}
