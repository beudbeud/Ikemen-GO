package main

// Disk cache of decoded SFF sprite data, active only in the libretro core.
// Measured on a Pi 5: a 720p screenpack sff takes 4.5s to decode (PNG inflate
// plus the sprite shrink) but well under a second to read back as raw texels.
// The cache stores exactly what would be handed to the GPU -- post-shrink --
// so a hit skips both the decode and the shrink.
//
// Layout: $HOME/.cache/ikemen-go/<sha1(key)>.sfc, key = absolute source path
// + load flags + shrink settings. The file embeds the source's size and mtime;
// any mismatch regenerates it.
//
// An entry is [magic][pixel blobs][table][table offset][magic]. The blobs
// come first because they are written while the source decodes: the store
// then only appends the table (everything but texels: palettes, sprite
// headers, where each blob is, its trim boxes) and renames the file, so
// texels reach the disk once. A load reads the table in one piece and maps
// the rest: no texel page is touched until a sprite is warmed or drawn.
//
// Writing happens behind the load, on its own goroutine: an SD card takes
// ~12MiB/s, and a loader that waited for it kept an HD pack's first boot on
// a black screen for 70s with the CPU idle. Until its file is complete a
// sprite reads its texels from the heap, within a budget. ponytail: no compression -- a USB3 read beats
// the Pi's inflate several times over; add lz4 if the size cap below starts
// evicting what a session needs.
//
// Entries are raw texels, hundreds of MB for an HD character, and the key
// multiplies them (every resolution, every path to the same game), so the
// directory is capped at sffCacheMaxBytes: each store evicts the least
// recently used entries, and a hit refreshes its entry's mtime. Temp files a
// killed core left behind are removed the first time the cache is used.

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const sffCacheMagic = "IKSFC002"

// sffCacheMaxBytes caps the cache directory. It lives in the frontend's share
// (an SD card on Recalbox), where 16.8GB of entries were found uncapped.
// 2GiB was too small for an HD pack at full definition (sprite detail Auto at
// its authored size): Ultimate Cosmos' menus, effects and two characters are
// ~2.4GiB of texels, so every load evicted what the next one needed and the
// boot fell back to decoding -- 113s of frozen screen.
// ponytail: fixed cap; make it a core option if a pack needs more cached.
const sffCacheMaxBytes = 6 << 30

type sffCaptureEntry struct {
	off         int64 // position of the pixel blob in the entry file
	n           int32 // 0: a sprite of the recording load with no pixels (yet)
	w, h, depth int32
	trim        [2][4]float32
	data        []byte // the texels while the heap holds them for the sprite
	written     bool   // the writer has put them in the file
}

// sffRecording is the entry file a loadSff is building.
type sffRecording struct {
	f       *os.File
	w       *bufio.Writer // the writer goroutine's
	off     int64         // where the next blob goes; the loader's until the load ends
	entries map[*Sprite]sffCaptureEntry
	// over: the heap budget is spent (or there is none). Texels then leave
	// the heap as they reach the file, the load waits for them, and the
	// sprites read the mapped file when it ends -- what every load did
	// before writes went behind.
	over   bool
	failed bool // a write failed: nothing is stored
}

var (
	sffCacheMu   sync.Mutex
	sffCacheCond = sync.NewCond(&sffCacheMu)
	sffRec       *sffRecording // the load being recorded, nil when none
	// sffHeld is the texel bytes the heap holds until they are on disk, and
	// sffBudget what it may reach before a load waits for the disk: half the
	// available RAM when a recording begins, 1.5GiB at most (an HD pack's
	// boot is 1.3GiB of texels), nothing on a board under 3GiB.
	sffHeld, sffBudget int64
	// sffJobs is the writer goroutine's queue: blobs and stores, in file
	// order -- which is why a blob is queued under the lock that gave it its
	// offset (decode workers capture concurrently).
	sffJobs     []func()
	sffJobsOnce sync.Once
	// sffCacheSwept is set once the leftovers of earlier processes are gone.
	sffCacheSwept sync.Once
	// sffCacheOff: an entry file could not be written or mapped (disk
	// trouble); nothing more is recorded this session.
	sffCacheOff atomic.Bool
)

// sffCaptureExpect registers a sprite of the recording load. Only those are
// captured: other loaders run meanwhile (the select screen's portrait
// preload), and their pixels are not this file's.
func sffCaptureExpect(s *Sprite) {
	sffCacheMu.Lock()
	if sffRec != nil {
		sffRec.entries[s] = sffCaptureEntry{}
	}
	sffCacheMu.Unlock()
}

// sffCaptureAdd is called from SetPxl/SetRaw with the exact post-shrink bytes
// of a sprite's texture, and their trim boxes. True: the sprite gets its
// texture from the cache's copy of them (sffCacheFinish) -- the caller must
// not upload them. An uncached load that uploaded as it decoded put every
// sprite of every file on the GPU, most never drawn: 3GiB of unevictable
// memory booting an HD pack, and a 4GiB Pi thrashed to a halt.
func sffCaptureAdd(s *Sprite, data []byte, w, h, depth int32, trim [2][4]float32) bool {
	sffCacheMu.Lock()
	rec := sffRec
	if rec == nil || rec.failed {
		sffCacheMu.Unlock()
		return false
	}
	old, ours := rec.entries[s]
	if !ours {
		sffCacheMu.Unlock()
		return false
	}
	if old.data != nil {
		sffHeld -= int64(old.n) // set twice: the first blob stays in the file, unused
	}
	n := int64(len(data))
	rec.entries[s] = sffCaptureEntry{off: rec.off, n: int32(n), w: w, h: h, depth: depth, trim: trim, data: data}
	rec.off += n
	sffHeld += n
	sffEnqueueLocked(func() { rec.writeBlob(s, data) })
	if sffHeld > sffBudget && !rec.over {
		rec.over = true
		for k, e := range rec.entries {
			if e.written && e.data != nil {
				sffHeld -= int64(e.n)
				e.data = nil
				rec.entries[k] = e
			}
		}
	}
	for rec.over && sffHeld > sffBudget && !rec.failed {
		sffCacheCond.Wait()
	}
	sffCacheMu.Unlock()
	return canMmap
}

func sffEnqueueLocked(job func()) {
	sffJobs = append(sffJobs, job)
	sffCacheCond.Broadcast()
}

func sffEnqueue(job func()) {
	sffCacheMu.Lock()
	sffEnqueueLocked(job)
	sffCacheMu.Unlock()
}

// sffWriterLoop is the writer goroutine.
func sffWriterLoop() {
	for {
		sffCacheMu.Lock()
		for len(sffJobs) == 0 {
			sffCacheCond.Wait()
		}
		job := sffJobs[0]
		sffJobs[0] = nil
		sffJobs = sffJobs[1:]
		sffCacheMu.Unlock()
		job()
	}
}

// writeBlob runs on the writer goroutine.
func (rec *sffRecording) writeBlob(s *Sprite, data []byte) {
	sffCacheMu.Lock()
	failed := rec.failed
	sffCacheMu.Unlock()
	var err error
	if !failed {
		_, err = rec.w.Write(data)
	}
	sffCacheMu.Lock()
	if err != nil {
		rec.failed = true // disk trouble: the load itself is unaffected
		sffCacheOff.Store(true)
	}
	e := rec.entries[s]
	e.written = true
	if rec.over && e.data != nil {
		sffHeld -= int64(e.n)
		e.data = nil
	}
	rec.entries[s] = e
	sffCacheCond.Broadcast()
	sffCacheMu.Unlock()
}

// sffCacheBegin starts recording; false when the cache is off or another load
// is already recording (that load just is not cached).
func sffCacheBegin() bool {
	if libretroPresent == nil || sffCacheOff.Load() {
		return false
	}
	sffCacheMu.Lock()
	defer sffCacheMu.Unlock()
	if sffRec != nil {
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
	// Only one core runs at a time, and nothing of this one is on disk yet:
	// any half-written entry here was left by a core killed mid-load.
	sffCacheSwept.Do(func() {
		for _, pat := range []string{"spill*", "sfc*"} {
			tmps, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, t := range tmps {
				if filepath.Ext(t) != ".sfc" {
					os.Remove(t)
				}
			}
		}
	})
	f, err := os.CreateTemp(dir, "sfc*")
	if err != nil {
		return false
	}
	sffJobsOnce.Do(func() { go sffWriterLoop() })
	rec := &sffRecording{f: f, w: bufio.NewWriterSize(f, 1<<20), off: int64(len(sffCacheMagic)),
		entries: map[*Sprite]sffCaptureEntry{}, over: !canMmap}
	rec.w.WriteString(sffCacheMagic)
	sffBudget = 0
	if libretroMemAvailable != nil && !libretroLowRAM {
		sffBudget = min(libretroMemAvailable()/2, 3<<29)
	}
	sffRec = rec
	return true
}

// sffCacheAbort ends the recording of a load that failed: nothing is stored.
func sffCacheAbort() {
	sffCacheMu.Lock()
	rec := sffRec
	sffRec = nil
	if rec != nil {
		rec.failed = true // the blobs still queued are not worth writing
		for k, e := range rec.entries {
			if e.data != nil {
				sffHeld -= int64(e.n)
				e.data = nil
				rec.entries[k] = e
			}
		}
		sffEnqueueLocked(func() { rec.store("", nil) })
	}
	sffCacheMu.Unlock()
}

// sffCacheFlush waits for the writer to be done with all that was queued.
func sffCacheFlush() {
	done := make(chan struct{})
	sffJobsOnce.Do(func() { go sffWriterLoop() })
	sffEnqueue(func() { close(done) })
	<-done
}

// sffCacheFinish ends the recording of a completed load. Its sprites get
// their texels the lazy way, as after a cached load: textures are made on
// first draw or in idle time, under the GPU memory cap. The texels are the
// heap's until the writer has stored the entry file, then the mapped file's;
// past the heap budget the load waits for the file here and maps it at once.
// False when the file cannot serve (a failed write, no mapping) and the heap
// no longer has the texels: the caller loads the source again, not recorded.
//
// list is the sprite order of the source file, links[i] >= 0 marks a sprite
// sharing the texture of list[links[i]]. The table is built here, on the
// loading thread: paletteMap and PalTable are remapped at runtime once the
// engine owns the Sff, so reading them later would race.
func sffCacheFinish(filename string, char, isActPal bool, s *Sff, list []*Sprite, links []int32) bool {
	sffCacheMu.Lock()
	rec := sffRec
	sffRec = nil
	sffCacheMu.Unlock()
	if rec == nil {
		return false
	}
	if rec.over {
		sffEnqueue(func() {
			if rec.w.Flush() != nil {
				sffCacheMu.Lock()
				rec.failed = true
				sffCacheMu.Unlock()
			}
		})
		sffCacheFlush()
	}
	// No capture can come any more, and what the writer still changes in an
	// entry (written, data) is not read from this copy.
	sffCacheMu.Lock()
	captured := make(map[*Sprite]sffCaptureEntry, len(rec.entries))
	var held int64
	for k, e := range rec.entries {
		captured[k] = e
		if e.data != nil {
			held += int64(e.n)
		}
	}
	over, failed := rec.over, rec.failed
	sffCacheMu.Unlock()

	path := sffCachePath(filename, char, isActPal)
	var table []byte
	if size, mtime, ok := sffCacheSourceStat(filename); ok && path != "" {
		table = sffCacheTable(size, mtime, s, list, links, captured)
	}
	if !canMmap { // the caller uploaded as it decoded; only the file is left to do
		sffEnqueue(func() { rec.store(path, table) })
		return true
	}

	var m *fileMapping
	if over {
		if !failed {
			m = mmapFile(rec.f)
		}
		if m == nil {
			sffEnqueue(func() { rec.store("", nil) })
			return false
		}
	}
	lazies := make([]*Sprite, 0, len(captured))
	texs := make([]*lazyTex, 0, len(captured))
	offs := make([]int64, 0, len(captured)) // of texs[i] in the file
	for _, spr := range list {
		e := captured[spr]
		if e.n <= 0 {
			continue
		}
		data := e.data
		if m != nil {
			if e.off+int64(e.n) > int64(len(m.data)) {
				sffEnqueue(func() { rec.store("", nil) })
				return false
			}
			data = m.data[e.off : e.off+int64(e.n) : e.off+int64(e.n)]
		}
		lazies = append(lazies, spr)
		offs = append(offs, e.off)
		texs = append(texs, &lazyTex{data: data, keep: m, w: e.w, h: e.h, depth: e.depth,
			filter: e.depth > 8 && sys.cfg.Video.RGBSpriteBilinearFilter})
	}
	// On the main thread, behind the shareCopy tasks this load queued: they
	// copy a texture that was never made, and must not run after this.
	sys.mainThreadTask <- func() {
		for i, spr := range lazies {
			spr.lazy, spr.trim = texs[i], captured[spr].trim
		}
		for i, spr := range list {
			if links != nil && links[i] >= 0 {
				spr.lazy, spr.trim = list[links[i]].lazy, list[links[i]].trim
			}
		}
		lazyEnqueue(lazies) // sorts lazies: texs[i] is not theirs past this
	}

	sffEnqueue(func() {
		stored := rec.store(path, table)
		if over {
			return
		}
		// The heap's texels are on disk: hand the sprites the mapped file.
		var m *fileMapping
		if stored {
			if f, err := os.Open(path); err == nil {
				m = mmapFile(f)
				f.Close()
			}
		}
		sffCacheMu.Lock()
		sffHeld -= held
		sffCacheCond.Broadcast()
		sffCacheMu.Unlock()
		if m == nil {
			return // they stay on the heap
		}
		// On the main thread, the only one that writes a lazyTex.
		sys.mainThreadTask <- func() {
			lazyQueue.Lock()
			for i, l := range texs {
				if end := offs[i] + int64(len(l.data)); l.data != nil && end <= int64(len(m.data)) {
					l.data, l.keep = m.data[offs[i]:end:end], m
				}
			}
			lazyQueue.Unlock()
		}
	})
	return true
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
	w   io.Writer
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

// sffCacheTable serializes everything of s but its texels: what follows the
// blobs in an entry file. Nil when it cannot be written out.
func sffCacheTable(size, mtime int64, s *Sff, list []*Sprite, links []int32,
	captured map[*Sprite]sffCaptureEntry) []byte {
	var buf bytes.Buffer
	w := &sfcWriter{w: &buf}
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
	w.write(uint32(len(pl.duplicatePals)))
	for k, dups := range pl.duplicatePals {
		w.write(int32(k))
		w.write(uint32(len(dups)))
		for _, d := range dups {
			w.write(int32(d))
		}
	}

	w.write(uint32(len(list)))
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
		switch e := captured[spr]; {
		case links != nil && links[i] >= 0:
			w.write(byte(2))
			w.write(links[i])
		case e.n > 0:
			w.write(byte(1))
			w.write(e.w)
			w.write(e.h)
			w.write(e.depth)
			w.write(e.off)
			w.write(e.n)
			w.write(e.trim)
		default:
			w.write(byte(0)) // blank sprite
		}
	}
	if w.err != nil {
		return nil
	}
	return buf.Bytes()
}

// store runs on the writer goroutine, after the recording's blobs: it
// appends the table and puts the entry file in place, or removes it (no
// table, a failed write). True when the entry is in place.
func (rec *sffRecording) store(path string, table []byte) bool {
	defer os.Remove(rec.f.Name()) // no-op after a successful rename
	defer rec.f.Close()
	sffCacheMu.Lock()
	failed := rec.failed
	sffCacheMu.Unlock()
	if failed || table == nil || path == "" {
		return false
	}
	// A file cut short (power lost before the data reached the disk) has no
	// footer where a load looks for one.
	var foot [8]byte
	binary.LittleEndian.PutUint64(foot[:], uint64(rec.off))
	rec.w.Write(table)
	rec.w.Write(foot[:])
	rec.w.WriteString(sffCacheMagic)
	if rec.w.Flush() != nil || rec.f.Close() != nil || os.Rename(rec.f.Name(), path) != nil {
		sffCacheOff.Store(true)
		return false
	}
	sffCacheEvict(filepath.Dir(path), sffCacheMaxBytes, path)
	return true
}

// sffCacheEvict removes the least recently used entries of dir until the
// entries total at most max bytes. keep, the entry just written, stays even
// when it alone is over the cap: it is what the next launch will read.
func sffCacheEvict(dir string, max int64, keep string) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.sfc"))
	type entry struct {
		path  string
		size  int64
		mtime time.Time
	}
	var entries []entry
	var total int64
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			entries = append(entries, entry{p, st.Size(), st.ModTime()})
			total += st.Size()
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mtime.Before(entries[j].mtime) })
	for _, e := range entries {
		if total <= max {
			break
		}
		if e.path != keep && os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}

// --- load -----------------------------------------------------------------

// sfcReader walks an entry's table, read whole into b.
type sfcReader struct {
	b   []byte
	off int
	err bool
}

func (r *sfcReader) bytes(n int) []byte {
	if r.err || n < 0 || n > len(r.b)-r.off { // a corrupt length must not OOM
		r.err = true
		return nil
	}
	b := r.b[r.off : r.off+n]
	r.off += n
	return b
}

func (r *sfcReader) u16() uint16 {
	b := r.bytes(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (r *sfcReader) u32() uint32 {
	b := r.bytes(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *sfcReader) i32() int32 { return int32(r.u32()) }

func (r *sfcReader) i64() int64 {
	b := r.bytes(8)
	if b == nil {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b))
}

func (r *sfcReader) u32s(n int) []uint32 {
	if n < 0 || n > len(r.b)/4 {
		r.err = true
		return nil
	}
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
// Mapped (Unix), each sprite keeps its texels in the file and makes its texture
// when first drawn (Sprite.texture); otherwise uploads are queued on the main
// thread exactly like a normal load.
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

	// The table sits at the end, where the footer says: one read, whatever
	// the size of the texels before it.
	const magicLen = int64(len(sffCacheMagic))
	st, err := f.Stat()
	if err != nil || st.Size() < 2*magicLen+8 {
		return drop()
	}
	var head, foot [16]byte
	if _, err := f.ReadAt(head[:magicLen], 0); err != nil || string(head[:magicLen]) != sffCacheMagic {
		return drop()
	}
	if _, err := f.ReadAt(foot[:], st.Size()-8-magicLen); err != nil || string(foot[8:]) != sffCacheMagic {
		return drop()
	}
	tableOff := int64(binary.LittleEndian.Uint64(foot[:8]))
	tableLen := st.Size() - 8 - magicLen - tableOff
	if tableOff < magicLen || tableLen <= 0 || tableLen > 1<<28 {
		return drop()
	}
	r := &sfcReader{b: make([]byte, tableLen)}
	if _, err := f.ReadAt(r.b, tableOff); err != nil {
		return drop()
	}
	// Mapped, the texels stay in the file until a sprite is first drawn.
	m := mmapFile(f)
	if m != nil {
		// A sprite's first draw must not wait on the SD card: 40ms hitches.
		willNeed(m.data)
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
	nd := int(r.u32())
	if r.err || nd > limit {
		return drop()
	}
	for i := 0; i < nd; i++ {
		k, n := int(r.i32()), int(r.u32())
		if r.err || n > limit {
			return drop()
		}
		for j := 0; j < n; j++ {
			pl.duplicatePals[k] = append(pl.duplicatePals[k], int(r.i32()))
		}
	}

	ns := int(r.u32())
	if r.err || ns > limit {
		return drop()
	}
	list := make([]*Sprite, ns)
	var lazies []*Sprite
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
			off, n := r.i64(), int64(r.i32())
			var trim [2][4]float32
			for j := range trim {
				for k := range trim[j] {
					trim[j][k] = math.Float32frombits(r.u32())
				}
			}
			if r.err || w <= 0 || h <= 0 || (depth != 8 && depth != 24 && depth != 32) ||
				n != int64(w)*int64(h)*int64(depth/8) || off < magicLen || off+n > tableOff {
				return drop() // SetData trusts these: a damaged entry must not reach GL
			}
			filter := false
			if depth > 8 {
				filter = sys.cfg.Video.RGBSpriteBilinearFilter
			}
			if m != nil {
				spr.lazy = &lazyTex{data: m.data[off : off+n : off+n], keep: m, w: w, h: h, depth: depth, filter: filter}
				spr.trim = trim
				lazies = append(lazies, spr)
			} else {
				data := make([]byte, n)
				if _, err := f.ReadAt(data, off); err != nil {
					return drop()
				}
				spr.uploadTexture(data, w, h, depth, filter, trim)
			}
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
		if src.lazy != nil {
			dst.lazy, dst.trim = src.lazy, src.trim
			continue
		}
		sys.mainThreadTask <- func() {
			dst.Tex = src.Tex
			dst.trim = src.trim
		}
	}
	if r.err {
		return drop()
	}
	lazyEnqueue(lazies)
	now := time.Now()
	os.Chtimes(path, now, now) // eviction order is last use, not creation
	fmt.Fprintf(os.Stderr, "Ikemen GO: sff %s: %d sprites from cache in %dms\n",
		filename, ns, time.Since(start).Milliseconds())
	return s
}
