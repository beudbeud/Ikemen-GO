package main

// Disk cache of decoded SFF sprite data, active only in the libretro core.
// Measured on a Pi 5: a 720p screenpack sff takes 4.5s to decode (PNG inflate
// plus the sprite shrink) but well under a second to read back as raw texels.
// The cache stores exactly what would be handed to the GPU -- post-shrink --
// so a hit skips both the decode and the shrink.
//
// Layout: $HOME/.cache/ikemen-go/<sha1(key)>.sfc, key = absolute source path
// + load flags + shrink settings. The file embeds the source's size and mtime;
// any mismatch regenerates it. ponytail: no compression -- a USB3 read beats
// the Pi's inflate several times over; add lz4 if cache size ever hurts.

import (
	"bufio"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const sffCacheMagic = "IKSFC001"

type sffCaptureEntry struct {
	off         int64 // position of the pixel blob in the spill file
	n           int32
	w, h, depth int32
}

var (
	sffCacheMu    sync.Mutex
	sffCaptureMap map[*Sprite]sffCaptureEntry // nil when no loadSff is recording
	// Captured pixels spill straight to a temp file: an HD character SFF is
	// hundreds of MB of decoded texels, and holding them all until the store
	// at the end of the load is what used to spike RAM on a 4GB board.
	sffSpillFile   *os.File
	sffSpillWriter *bufio.Writer
	sffSpillOff    int64
)

// sffCaptureAdd is called from SetPxl/SetRaw with the exact post-shrink bytes
// about to be uploaded. Entries for sprites outside the recording loadSff are
// simply never looked up.
func sffCaptureAdd(s *Sprite, data []byte, w, h, depth int32) {
	sffCacheMu.Lock()
	if sffCaptureMap != nil && sffSpillWriter != nil {
		if _, err := sffSpillWriter.Write(data); err != nil {
			// Disk trouble: stop recording, the load itself is unaffected.
			sffCaptureMap = nil
		} else {
			sffCaptureMap[s] = sffCaptureEntry{sffSpillOff, int32(len(data)), w, h, depth}
			sffSpillOff += int64(len(data))
		}
	}
	sffCacheMu.Unlock()
}

// sffCacheBegin starts recording; false when the cache is off or another load
// is already recording (that load just is not cached).
func sffCacheBegin() bool {
	if libretroPresent == nil {
		return false
	}
	sffCacheMu.Lock()
	defer sffCacheMu.Unlock()
	if sffCaptureMap != nil {
		return false
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return false
	}
	dir = filepath.Join(dir, "ikemen-go")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, "spill*")
	if err != nil {
		return false
	}
	sffSpillFile, sffSpillWriter, sffSpillOff = f, bufio.NewWriterSize(f, 1<<20), 0
	sffCaptureMap = map[*Sprite]sffCaptureEntry{}
	return true
}

// sffCacheEnd stops recording and hands back what was captured plus the spill
// file holding the pixel blobs (flushed). The caller owns closing and removing
// the file. Safe to call more than once; later calls return nil.
func sffCacheEnd() (map[*Sprite]sffCaptureEntry, *os.File) {
	sffCacheMu.Lock()
	defer sffCacheMu.Unlock()
	m, f := sffCaptureMap, sffSpillFile
	if sffSpillWriter != nil && sffSpillWriter.Flush() != nil {
		m = nil
	}
	sffCaptureMap, sffSpillFile, sffSpillWriter = nil, nil, nil
	return m, f
}

// sffCacheDiscard ends a recording without storing it (error paths).
func sffCacheDiscard() {
	if _, f := sffCacheEnd(); f != nil {
		f.Close()
		os.Remove(f.Name())
	}
}

func sffCachePath(filename string, char, isActPal bool) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(filename)
	if err != nil {
		abs = filename
	}
	key := fmt.Sprintf("%s|%v|%v|%d|%v|%d", abs, char, isActPal,
		libretroSpriteShrink, libretroShrinkIndexed, libretroShrinkGameH)
	sum := sha1.Sum([]byte(key))
	return filepath.Join(dir, "ikemen-go", hex.EncodeToString(sum[:])+".sfc")
}

// sffCacheSourceStat identifies the source file the way the loader resolves it.
func sffCacheSourceStat(filename string) (size, mtime int64, ok bool) {
	p := FileExist(filename)
	if p == "" {
		p = filename
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0, 0, false
	}
	return st.Size(), st.ModTime().UnixNano(), true
}

// --- store ----------------------------------------------------------------

type sfcWriter struct {
	w   *bufio.Writer
	err error
}

func (w *sfcWriter) write(v interface{}) {
	if w.err == nil {
		w.err = binary.Write(w.w, binary.LittleEndian, v)
	}
}

func (w *sfcWriter) writeBytes(b []byte) {
	if w.err == nil {
		_, w.err = w.w.Write(b)
	}
}

// sffCacheStore writes the decoded state of s. list is the sprite order of the
// source file, links[i] >= 0 marks a sprite sharing the texture of list[links[i]].
//
// The write is synchronous on the loading thread: paletteMap and PalTable are
// remapped at runtime once the engine owns the Sff, so writing later would
// race. It only ever runs on the first, uncached load -- the one that already
// pays for the full decode.
func sffCacheStore(filename string, char, isActPal bool, s *Sff,
	list []*Sprite, links []int32, captured map[*Sprite]sffCaptureEntry, spill *os.File) {
	if spill != nil {
		defer func() {
			spill.Close()
			os.Remove(spill.Name())
		}()
	}
	path := sffCachePath(filename, char, isActPal)
	if path == "" || captured == nil || spill == nil {
		return
	}
	size, mtime, ok := sffCacheSourceStat(filename)
	if !ok {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "sfc*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	w := &sfcWriter{w: bufio.NewWriterSize(tmp, 1<<20)}
	w.writeBytes([]byte(sffCacheMagic))
	w.write(size)
	w.write(mtime)
	w.write(s.header.Version)
	w.write(s.header.NumberOfSprites)
	w.write(s.header.NumberOfPalettes)

	pl := &s.palList
	w.write(uint32(len(pl.palettes)))
	for _, p := range pl.palettes {
		w.write(uint32(len(p)))
		w.write(p)
	}
	w.write(uint32(len(pl.paletteMap)))
	for _, m := range pl.paletteMap {
		w.write(int32(m))
	}
	writeIdxMap := func(m map[[2]uint16]int) {
		w.write(uint32(len(m)))
		for k, v := range m {
			w.write(k[0])
			w.write(k[1])
			w.write(int32(v))
		}
	}
	writeIdxMap(pl.PalTable)
	writeIdxMap(pl.numcols)

	w.write(uint32(len(list)))
	var blob []byte
	for i, spr := range list {
		w.write(spr.Group)
		w.write(spr.Number)
		w.write(spr.Size)
		w.write(spr.Offset)
		w.write(int32(spr.palidx))
		w.write(int32(spr.rle))
		w.write(spr.coldepth)
		w.write(uint32(len(spr.Pal)))
		w.write(spr.Pal)
		switch e, hasPix := captured[spr]; {
		case links != nil && links[i] >= 0:
			w.write(byte(2))
			w.write(links[i])
		case hasPix:
			w.write(byte(1))
			w.write(e.w)
			w.write(e.h)
			w.write(e.depth)
			w.write(uint32(e.n))
			if int(e.n) > cap(blob) {
				blob = make([]byte, e.n)
			}
			blob = blob[:e.n]
			if _, err := spill.ReadAt(blob, e.off); err != nil {
				w.err = err
			}
			w.writeBytes(blob)
		default:
			w.write(byte(0)) // blank sprite
		}
	}

	if w.err == nil {
		w.err = w.w.Flush()
	}
	if err := tmp.Close(); err != nil || w.err != nil {
		return
	}
	os.Rename(tmp.Name(), path)
}

// --- load -----------------------------------------------------------------

// sfcReader streams the cache file instead of holding it whole: each pixel
// blob owns its allocation, so a queued texture upload pins only its own
// sprite, not a several-hundred-MB file.
type sfcReader struct {
	r   *bufio.Reader
	tmp [8]byte
	err bool
}

func (r *sfcReader) bytes(n int) []byte {
	if r.err || n < 0 || n > 1<<28 { // corrupt length must not OOM
		r.err = true
		return nil
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r.r, b); err != nil {
		r.err = true
		return nil
	}
	return b
}

// small reads n <= 8 bytes into a scratch buffer only valid until the next read.
func (r *sfcReader) small(n int) []byte {
	if r.err {
		return nil
	}
	if _, err := io.ReadFull(r.r, r.tmp[:n]); err != nil {
		r.err = true
		return nil
	}
	return r.tmp[:n]
}

func (r *sfcReader) u16() uint16 {
	b := r.small(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (r *sfcReader) u32() uint32 {
	b := r.small(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *sfcReader) i32() int32 { return int32(r.u32()) }

func (r *sfcReader) i64() int64 {
	b := r.small(8)
	if b == nil {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b))
}

func (r *sfcReader) u32s(n int) []uint32 {
	b := r.bytes(n * 4)
	if b == nil {
		return nil
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return out
}

// sffCacheLoad returns the cached Sff, or nil on miss/staleness/corruption.
// Texture uploads are queued on the main thread exactly like a normal load.
func sffCacheLoad(filename string, char, isActPal bool) *Sff {
	if libretroPresent == nil {
		return nil
	}
	path := sffCachePath(filename, char, isActPal)
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	start := time.Now()
	drop := func() *Sff {
		os.Remove(path)
		return nil
	}

	r := &sfcReader{r: bufio.NewReaderSize(f, 1<<20)}
	if string(r.bytes(len(sffCacheMagic))) != sffCacheMagic {
		return drop()
	}
	size, mtime, ok := sffCacheSourceStat(filename)
	if !ok || r.i64() != size || r.i64() != mtime {
		return drop()
	}

	s := newSff()
	s.filename = filename
	copy(s.header.Version[:], r.bytes(4))
	s.header.NumberOfSprites = r.u32()
	s.header.NumberOfPalettes = r.u32()

	const limit = 1 << 20 // sanity bound for any count read from disk
	pl := &s.palList
	np := int(r.u32())
	if r.err || np > limit {
		return drop()
	}
	for i := 0; i < np; i++ {
		pl.SetSource(i, r.u32s(int(r.u32())))
	}
	nm := int(r.u32())
	if r.err || nm > limit {
		return drop()
	}
	pl.paletteMap = make([]int, nm)
	for i := range pl.paletteMap {
		pl.paletteMap[i] = int(r.i32())
	}
	readIdxMap := func() map[[2]uint16]int {
		n := int(r.u32())
		if r.err || n > limit {
			r.err = true
			return nil
		}
		m := make(map[[2]uint16]int, n)
		for i := 0; i < n; i++ {
			g, u := r.u16(), r.u16()
			m[[2]uint16{g, u}] = int(r.i32())
		}
		return m
	}
	pl.PalTable = readIdxMap()
	pl.numcols = readIdxMap()

	ns := int(r.u32())
	if r.err || ns > limit {
		return drop()
	}
	list := make([]*Sprite, ns)
	type link struct{ dst, src int }
	var links []link
	for i := 0; i < ns; i++ {
		spr := newSprite()
		spr.Group = r.u16()
		spr.Number = r.u16()
		spr.Size[0], spr.Size[1] = r.u16(), r.u16()
		spr.Offset[0], spr.Offset[1] = int16(r.u16()), int16(r.u16())
		spr.palidx = int(r.i32())
		spr.rle = int(r.i32())
		if b := r.bytes(1); b != nil {
			spr.coldepth = b[0]
		}
		if n := int(r.u32()); n > 0 {
			spr.Pal = r.u32s(n)
		}
		kind := byte(0)
		if b := r.bytes(1); b != nil {
			kind = b[0]
		}
		switch kind {
		case 1:
			w, h, depth := r.i32(), r.i32(), r.i32()
			data := r.bytes(int(r.u32()))
			if r.err {
				return drop()
			}
			filter := false
			if depth > 8 {
				filter = sys.cfg.Video.RGBSpriteBilinearFilter
			}
			spr.uploadTexture(data, w, h, depth, filter)
		case 2:
			links = append(links, link{i, int(r.i32())})
		}
		if r.err {
			return drop()
		}
		list[i] = spr
		if s.sprites[[2]uint16{spr.Group, spr.Number}] == nil {
			s.sprites[[2]uint16{spr.Group, spr.Number}] = spr
		}
	}
	// Texture links ride the same FIFO as the uploads above, so the source's
	// texture exists by the time the copy runs -- same trick as shareCopy.
	for _, l := range links {
		if l.src < 0 || l.src >= ns {
			return drop()
		}
		dst, src := list[l.dst], list[l.src]
		sys.mainThreadTask <- func() {
			dst.Tex = src.Tex
		}
	}
	if r.err {
		return drop()
	}
	fmt.Fprintf(os.Stderr, "Ikemen GO: sff %s: %d sprites from cache in %dms\n",
		filename, ns, time.Since(start).Milliseconds())
	return s
}
