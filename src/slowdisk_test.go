//go:build unix

package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// A match played from the sprite cache, as far as the disk is concerned: the
// real cache load, warm-up, prefetch, cap and eviction, under a renderer that
// only reads the texels it is handed. What it prints is what the game thread
// would wait for on the disk holding the cache.
//
//	IKEMEN_TEST_SLOWDISK=p1.sff:p2.sff:fx.sff:stage.sff   (the last one is the stage)
//	IKEMEN_TEST_CACHE=<directory on the disk to measure>
//	IKEMEN_TEST_GPUCAP_MB=1024   the board's GPU memory cap (a quarter of its RAM)
//
// The first run records the entries. Drop them from the page cache, then run
// again: that one measures.
func TestSffCacheSlowDisk(t *testing.T) {
	files := strings.Split(os.Getenv("IKEMEN_TEST_SLOWDISK"), ":")
	dir := os.Getenv("IKEMEN_TEST_CACHE")
	if files[0] == "" || dir == "" {
		t.Skip("IKEMEN_TEST_SLOWDISK / IKEMEN_TEST_CACHE not set")
	}
	gpuCap := int64(1 << 30)
	if v := os.Getenv("IKEMEN_TEST_GPUCAP_MB"); v != "" {
		fmt.Sscan(v, &gpuCap)
		gpuCap <<= 20
	}
	t.Setenv("XDG_CACHE_HOME", dir)
	oldPresent, oldAvail, oldGfx, oldTasks := libretroPresent, libretroMemAvailable, gfx, sys.mainThreadTask
	oldFrame := sys.frameCounter
	libretroPresent = func() {}
	libretroMemAvailable = func() int64 { return 12 << 30 }
	sys.mainThreadTask = make(chan func(), 1<<16)
	fake := &slowGfx{}
	gfx = fake
	t.Cleanup(func() {
		libretroPresent, libretroMemAvailable, gfx, sys.mainThreadTask = oldPresent, oldAvail, oldGfx, oldTasks
		lazyRoom.Store(math.MaxInt64)
		sys.frameCounter = oldFrame
	})
	drain := func() {
		for len(sys.mainThreadTask) > 0 {
			(<-sys.mainThreadTask)()
		}
	}

	recorded := false
	for _, f := range files {
		if _, err := os.Stat(sffCachePath(f, true, false)); err != nil {
			if _, err := loadSff(f, true, false, false); err != nil {
				t.Fatal(err)
			}
			drain()
			recorded = true
		}
	}
	if recorded {
		sffCacheFlush()
		drain()
		t.Skip("entries recorded: drop them from the page cache and run again")
	}

	// The loading screen: one file after the other, then the rest of the load.
	begin := time.Now()
	var sffs []*Sff
	for _, f := range files {
		s := sffCacheLoad(f, true, false)
		if s == nil {
			t.Fatalf("%s: cache miss", f)
		}
		sffs = append(sffs, s)
	}
	lazyRoom.Store(gpuCap)
	time.Sleep(2 * time.Second)
	lazyMakeNow(time.Second)
	loaded := time.Since(begin)

	sprites := make([][]*Sprite, len(sffs))
	var all []*lazyTex
	for i, s := range sffs {
		for _, spr := range s.sprites {
			if spr.lazy != nil {
				sprites[i] = append(sprites[i], spr)
				all = append(all, spr.lazy)
			}
		}
		sort.Slice(sprites[i], func(a, b int) bool {
			x, y := sprites[i][a], sprites[i][b]
			return x.Group < y.Group || x.Group == y.Group && x.Number < y.Number
		})
	}

	// 60 seconds of fight. The stage shows whole on the first frame; each
	// character shows a sprite it has not shown yet every 10 frames, the
	// effect files one every 30: the moves of a fight come in no order.
	rnd := rand.New(rand.NewSource(1))
	const frames = 3600
	var demand []time.Duration
	var cold, drawn int
	var line strings.Builder // per 5s: cold draws / ms waited / textures warm
	coldAt, waitAt := 0, time.Duration(0)
	var owed int64
	allWarm := time.Duration(0)
	for f := 0; f < frames; f++ {
		t0 := time.Now()
		sys.frameCounter++
		draw := func(spr *Sprite) {
			if spr.lazy.tex == nil && !spr.lazy.warm.Load() {
				cold++
			}
			drawn++
			spr.texture()
		}
		last := len(sprites) - 1
		if f == 0 {
			for _, spr := range sprites[last] {
				draw(spr)
			}
		}
		for i, list := range sprites[:last] {
			if every := map[bool]int{true: 10, false: 30}[i < 2]; f%every == i && len(list) > 0 {
				draw(list[rnd.Intn(len(list))])
			}
		}
		demand = append(demand, time.Since(t0))
		waitAt += demand[f]
		if f%300 == 299 {
			n := 0
			for _, l := range all {
				if l.warm.Load() {
					n++
				}
			}
			fmt.Fprintf(&line, " %d/%.0fms/%d", cold-coldAt, float64(waitAt)/1e6, n)
			coldAt, waitAt = cold, 0
		}
		// libretroGPUSample
		if f%60 == 0 {
			owed = max(fake.used-gpuCap, 0)
			lazyRoom.Store(gpuCap - fake.used)
			if allWarm == 0 {
				n := 0
				for _, l := range all {
					if l.warm.Load() {
						n++
					}
				}
				if n == len(all) {
					allWarm = time.Since(begin)
				}
			}
		}
		if owed > 0 {
			if n := lazyEvict(min(owed, 8<<20)); n > 0 {
				owed -= n
			} else {
				owed = 0
			}
		}
		// libretroPrefetch
		for time.Since(t0) < 8*time.Millisecond {
			if made, _ := lazyPrefetchOne(); !made {
				break
			}
		}
		if d := time.Until(t0.Add(time.Second / 60)); d > 0 {
			time.Sleep(d)
		}
	}

	var total, worst time.Duration
	var over12, over33, over100 int
	for _, d := range demand {
		total += d
		worst = max(worst, d)
		if d > 12*time.Millisecond {
			over12++
		}
		if d > 33*time.Millisecond {
			over33++
		}
		if d > 100*time.Millisecond {
			over100++
		}
	}
	warm := 0
	for _, l := range all {
		if l.warm.Load() {
			warm++
		}
	}
	fmt.Printf("slowdisk: load %.1fs, first frame %.0fms, %d draws (%d cold), waited %.1fs in all, worst %.0fms, frames >12ms %d >33ms %d >100ms %d, warm %d/%d (all at %.0fs), gpu %dMiB\n",
		loaded.Seconds(), float64(demand[0])/1e6, drawn, cold, total.Seconds(), float64(worst)/1e6,
		over12, over33, over100, warm, len(all), allWarm.Seconds(), fake.used>>20)
	fmt.Println("slowdisk: per 5s, cold draws/waited/warm:" + line.String())
}

// slowGfx makes textures that only read their texels: the page faults of an
// upload, without a GPU.
type slowGfx struct {
	Renderer
	used int64
}

type slowTex struct {
	Texture
	g *slowGfx
	n int64
}

var slowSink byte

func (g *slowGfx) newTexture(w, h, depth int32, filter bool) (Texture, error) {
	n := int64(w) * int64(h)
	if depth > 8 {
		n *= 4
	}
	return &slowTex{g: g, n: n}, nil
}

func (t *slowTex) SetData(data []byte) {
	for i := 0; i < len(data); i += 4096 {
		slowSink += data[i]
	}
	t.g.used += t.n
}

func (t *slowTex) release() { t.g.used -= t.n }
