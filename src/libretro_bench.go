//go:build libretro

package main

// A/B harness hooks. All of them are off unless their variable is set, and
// they cost one branch per frame when off.
//
//	IKEMEN_BENCH=<from>:<to>  aggregate presented frames [from, to), log a
//	                          single "Ikemen GO: bench ..." line at frame <to>,
//	                          then quit.
//	IKEMEN_DUMP=<dir>         with IKEMEN_DUMP_FRAMES=<n>[,<n>...]: write the
//	                          presented frame <n> to <dir>/frame_<n>.ppm, and
//	                          quit after the last one.
//	IKEMEN_BENCH_PPROF=<file> with IKEMEN_BENCH: CPU profile of the window
//	                          only -- a whole-run profile is mostly loading.
//	IKEMEN_BENCH_FILL=1       with IKEMEN_BENCH: also log the textures that
//	                          cover the most screen per frame over the window,
//	                          before and after the trim scissor.
//	IKEMEN_SEED=<n>           seed both random sources before the engine
//	                          starts, so an AI fight plays out the same way on
//	                          every run.
//
// Frame numbers count presented frames since boot. Under libretro the engine
// runs lockstep -- exactly one frame per retro_run -- so frame n is the same
// picture on every run of the same content, whatever the machine's speed. That
// is what makes a dump comparable pixel for pixel across two builds, and a
// bench window the same slice of the game in both -- provided the game itself
// is deterministic: the engine seeds its generator from the clock and Lua's
// math.random uses Go's auto-seeded one, so two unseeded AI fights diverge
// within a few seconds. IKEMEN_SEED pins both.
//
// Both quit through the engine's own exit path instead of waiting for a
// signal: a SIGTERM racing the core's shutdown is how a profile or a log gets
// cut short.

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"time"
)

type libretroFillStat struct {
	draws       uint64
	area, drawn float64
}

type libretroDrawCounter interface{ DrawCalls() uint64 }

type libretroPresentedReader interface {
	ReadPresentedRGBA(data []uint8, width, height int) bool
}

var lrBench struct {
	on       bool
	from, to uint64

	frame     uint64 // presented frames since boot
	start     time.Time
	steps     []time.Duration
	draws     []uint64
	lastDraws uint64

	pprof *os.File

	fill map[Texture]*libretroFillStat

	dumpDir    string
	dumpFrames map[uint64]bool
	dumpLast   uint64
	dumpBuf    []uint8
}

// libretroBenchInit reads the harness variables; called once at content load,
// before the engine goroutine starts drawing random numbers.
func libretroBenchInit() {
	if v := os.Getenv("IKEMEN_SEED"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n != 0 {
			Srand(int32(n))
			rand.Seed(n) //nolint:staticcheck // gopher-lua's math.random reads the global source
			fmt.Fprintf(os.Stderr, "Ikemen GO: random sources seeded with %d\n", n)
		} else {
			fmt.Fprintf(os.Stderr, "Ikemen GO: IKEMEN_SEED=%q is not a non-zero integer\n", v)
		}
	}
	if v := os.Getenv("IKEMEN_BENCH"); v != "" {
		from, to, ok := libretroParseRange(v)
		if !ok {
			fmt.Fprintf(os.Stderr, "Ikemen GO: bench: IKEMEN_BENCH=%q is not <from>:<to>\n", v)
		} else {
			lrBench.on, lrBench.from, lrBench.to = true, from, to
			lrBench.steps = make([]time.Duration, 0, to-from)
			lrBench.draws = make([]uint64, 0, to-from)
			if os.Getenv("IKEMEN_BENCH_FILL") != "" {
				lrBench.fill = map[Texture]*libretroFillStat{}
				libretroFillSprites = map[Texture]*Sprite{}
				libretroFill = libretroFillAdd
			}
		}
	}
	if dir := os.Getenv("IKEMEN_DUMP"); dir != "" {
		lrBench.dumpFrames = map[uint64]bool{}
		for _, f := range strings.Split(os.Getenv("IKEMEN_DUMP_FRAMES"), ",") {
			if n, err := strconv.ParseUint(strings.TrimSpace(f), 10, 64); err == nil {
				lrBench.dumpFrames[n] = true
				if n > lrBench.dumpLast {
					lrBench.dumpLast = n
				}
			}
		}
		if len(lrBench.dumpFrames) == 0 {
			fmt.Fprintln(os.Stderr, "Ikemen GO: dump: IKEMEN_DUMP set but IKEMEN_DUMP_FRAMES lists no frame")
		} else {
			lrBench.dumpDir = dir
		}
	}
}

func libretroParseRange(v string) (from, to uint64, ok bool) {
	a, b, found := strings.Cut(v, ":")
	if !found {
		return 0, 0, false
	}
	from, errA := strconv.ParseUint(a, 10, 64)
	to, errB := strconv.ParseUint(b, 10, 64)
	return from, to, errA == nil && errB == nil && to > from
}

// libretroBenchFrame runs on the game thread for every presented frame, after
// the frame is complete in the presented framebuffer. step is what the engine
// spent on it, from the frontend's request to the finished render.
func libretroBenchFrame(w, h int, step time.Duration) {
	if !lrBench.on && lrBench.dumpDir == "" {
		return
	}
	lrBench.frame++
	n := lrBench.frame

	var draws uint64
	if dc, ok := gfx.(libretroDrawCounter); ok {
		total := dc.DrawCalls()
		draws, lrBench.lastDraws = total-lrBench.lastDraws, total
	}

	if lrBench.on {
		switch {
		case n == lrBench.from:
			lrBench.start = time.Now()
			libretroBenchStartPprof()
			fallthrough
		case n > lrBench.from && n < lrBench.to:
			lrBench.steps = append(lrBench.steps, step)
			lrBench.draws = append(lrBench.draws, draws)
		case n == lrBench.to:
			if lrBench.pprof != nil {
				pprof.StopCPUProfile()
				lrBench.pprof.Close()
			}
			elapsed := time.Since(lrBench.start)
			if lrBench.fill != nil {
				libretroFillReport(uint64(len(lrBench.steps)))
			}
			fmt.Fprintln(os.Stderr, libretroBenchSummary(lrBench.steps, lrBench.draws, elapsed))
			libretroOnExit()
		}
	}

	if lrBench.dumpDir != "" && lrBench.dumpFrames[n] {
		libretroDumpFrame(n, w, h)
		if n == lrBench.dumpLast {
			libretroOnExit()
		}
	}
}

// libretroFillAdd accounts one quad of the frame being drawn, when that frame
// is in the bench window. Game thread.
func libretroFillAdd(tex Texture, area, drawn float32) {
	if n := lrBench.frame + 1; n < lrBench.from || n >= lrBench.to {
		return
	}
	st := lrBench.fill[tex]
	if st == nil {
		st = &libretroFillStat{}
		lrBench.fill[tex] = st
	}
	st.draws++
	st.area += float64(area)
	st.drawn += float64(drawn)
}

// libretroFillReport logs screen coverage per frame: the total, then the 20
// textures that drew the most, named by their sprite where one is known.
func libretroFillReport(frames uint64) {
	if frames == 0 {
		return
	}
	type row struct {
		tex Texture
		st  *libretroFillStat
	}
	var rows []row
	var area, drawn float64
	for tex, st := range lrBench.fill {
		rows = append(rows, row{tex, st})
		area += st.area
		drawn += st.drawn
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].st.drawn > rows[j].st.drawn })
	f := float64(frames)
	fmt.Fprintf(os.Stderr, "Ikemen GO: fill total area_mpx=%.2f drawn_mpx=%.2f textures=%d\n", area/f/1e6, drawn/f/1e6, len(rows))
	for i, r := range rows {
		if i == 20 {
			break
		}
		name := "?"
		if s := libretroFillSprites[r.tex]; s != nil {
			name = fmt.Sprintf("%d,%d %dx%d %dbpp", s.Group, s.Number, s.Size[0], s.Size[1], s.coldepth)
		}
		fmt.Fprintf(os.Stderr, "Ikemen GO: fill %-24s draws=%.1f area_mpx=%.3f drawn_mpx=%.3f\n",
			name, float64(r.st.draws)/f, r.st.area/f/1e6, r.st.drawn/f/1e6)
	}
}

// libretroBenchStartPprof starts the window's CPU profile. The whole-run one
// (IKEMEN_PROFILE) is left alone: Go runs one CPU profile at a time, so this
// one only starts when that one is off.
func libretroBenchStartPprof() {
	path := os.Getenv("IKEMEN_BENCH_PPROF")
	if path == "" {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ikemen GO: bench pprof:", err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		fmt.Fprintln(os.Stderr, "Ikemen GO: bench pprof:", err)
		f.Close()
		return
	}
	lrBench.pprof = f
}

// libretroBenchSummary formats the one line the A/B driver parses. fps is
// wall-clock over the window, so it carries the GPU's share of the frame that
// step, measured on the game thread, does not.
func libretroBenchSummary(steps []time.Duration, draws []uint64, elapsed time.Duration) string {
	n := len(steps)
	if n == 0 || elapsed <= 0 {
		return "Ikemen GO: bench frames=0"
	}
	sorted := append([]time.Duration(nil), steps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum time.Duration
	for _, s := range steps {
		sum += s
	}
	var dsum, dmax uint64
	for _, d := range draws {
		dsum += d
		if d > dmax {
			dmax = d
		}
	}
	ms := func(d time.Duration) float64 { return float64(d) / 1e6 }
	return fmt.Sprintf("Ikemen GO: bench frames=%d fps=%.2f step_ms_mean=%.3f step_ms_p50=%.3f step_ms_p95=%.3f step_ms_max=%.3f draws_mean=%.1f draws_max=%d",
		n, float64(n)/elapsed.Seconds(),
		ms(sum/time.Duration(n)), ms(sorted[n/2]), ms(sorted[n*95/100]), ms(sorted[n-1]),
		float64(dsum)/float64(n), dmax)
}

// libretroDumpFrame writes presented frame n as a binary PPM (P6), top-down
// RGB: the simplest format any image tool reads, and byte-comparable.
func libretroDumpFrame(n uint64, w, h int) {
	rd, ok := gfx.(libretroPresentedReader)
	if !ok {
		fmt.Fprintln(os.Stderr, "Ikemen GO: dump: this renderer cannot read back the presented frame")
		return
	}
	if len(lrBench.dumpBuf) != w*h*4 {
		lrBench.dumpBuf = make([]uint8, w*h*4)
	}
	if !rd.ReadPresentedRGBA(lrBench.dumpBuf, w, h) {
		fmt.Fprintf(os.Stderr, "Ikemen GO: dump: frame %d: readback failed\n", n)
		return
	}
	path := filepath.Join(lrBench.dumpDir, fmt.Sprintf("frame_%d.ppm", n))
	if err := libretroWritePPM(path, lrBench.dumpBuf, w, h); err != nil {
		fmt.Fprintf(os.Stderr, "Ikemen GO: dump: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "Ikemen GO: dump: frame %d -> %s\n", n, path)
}

// libretroWritePPM writes bottom-up RGBA as a top-down P6 PPM, alpha dropped.
func libretroWritePPM(path string, rgba []uint8, w, h int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	fmt.Fprintf(bw, "P6\n%d %d\n255\n", w, h)
	row := make([]byte, w*3)
	for y := h - 1; y >= 0; y-- {
		src := rgba[y*w*4 : (y+1)*w*4]
		for x := 0; x < w; x++ {
			row[x*3], row[x*3+1], row[x*3+2] = src[x*4], src[x*4+1], src[x*4+2]
		}
		bw.Write(row)
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
