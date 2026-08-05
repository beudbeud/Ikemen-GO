package main

import (
	"path/filepath"
	"strings"
)

// Hooks the libretro core installs into the engine. Both are zero in a normal
// standalone build, so `libretroPresent != nil` is the "am I a core?" test.
// ponytail: two package vars instead of a platform interface; there is exactly
// one alternate host and it only needs these two things.
var (
	// libretroPresent hands the finished frame to the frontend and blocks until
	// the frontend asks for the next one. Called on the game thread.
	libretroPresent func()
	// libretroPollInput delivers the keyboard events the frontend reported, from
	// inside pollEvents so they land after eventUpdate() has cleared sys.esc.
	libretroPollInput func()
	// libretroPads is how many virtual gamepads the frontend exposes.
	libretroPads int
	// libretroExit replaces os.Exit: a core may not kill the frontend process.
	libretroExit func()
	// libretroHeadlessGL creates the engine's GL context straight from EGL,
	// with no window and no SDL video driver. Non-nil only in a `gles` core,
	// which is the build for frontends that already own the display: on a
	// KMS/DRM console the frontend holds the DRM master and SDL has no driver
	// left it could open. Called on the game thread, which is where the context
	// then lives.
	libretroHeadlessGL func(w, h int) error
	// libretroConfigOverride gets the config the moment it is read, before
	// anything acts on it. The core uses it to point the engine's own files
	// (scripts, common states, effects) somewhere other than the content
	// folder, which is how a game folder built for an older Ikemen can still
	// run: only its motif, chars, stages and sound are taken from it.
	libretroConfigOverride func(*Config)
	// libretroEngineRoot is where OpenFile looks when a relative path is not in
	// the content folder. Empty unless the core was told to source the engine
	// files from the frontend's system directory.
	libretroEngineRoot string
)

// libretroSpriteShrink divides sprite texture resolution at upload (0 or 1 =
// off). Set once at content load, before any sprite exists. On a shared-memory
// GPU every texture byte is a RAM byte, and a 720p HD pack's sprites do not
// fit a 4GB board at full size; on a CRT-class output the loss is invisible.
var libretroSpriteShrink int32

// libretroShrinkSprite returns the sprite bitmap reduced by the configured
// factor: palette indices by decimation (they cannot be averaged), RGB by box
// average, RGBA alpha-weighted so transparent texels do not darken edges.
// Small sprites -- fonts, UI elements -- pass through untouched.
func libretroShrinkSprite(data []byte, w, h, bpp int32) ([]byte, int32, int32) {
	n := libretroSpriteShrink
	if n <= 1 || w*h < 65536 || w < n || h < n ||
		(bpp != 1 && bpp != 3 && bpp != 4) || int32(len(data)) != w*h*bpp {
		return data, w, h
	}
	ow, oh := w/n, h/n
	out := make([]byte, ow*oh*bpp)
	for y := int32(0); y < oh; y++ {
		for x := int32(0); x < ow; x++ {
			di := (y*ow + x) * bpp
			if bpp == 1 {
				out[di] = data[y*n*w+x*n]
				continue
			}
			var r, g, b, a uint32
			for dy := int32(0); dy < n; dy++ {
				for dx := int32(0); dx < n; dx++ {
					si := ((y*n+dy)*w + x*n + dx) * bpp
					if bpp == 4 {
						pa := uint32(data[si+3])
						r += uint32(data[si]) * pa
						g += uint32(data[si+1]) * pa
						b += uint32(data[si+2]) * pa
						a += pa
					} else {
						r += uint32(data[si])
						g += uint32(data[si+1])
						b += uint32(data[si+2])
					}
				}
			}
			if bpp == 4 {
				if a > 0 {
					out[di], out[di+1], out[di+2] = byte(r/a), byte(g/a), byte(b/a)
				}
				out[di+3] = byte(a / uint32(n*n))
			} else {
				k := uint32(n * n)
				out[di], out[di+1], out[di+2] = byte(r/k), byte(g/k), byte(b/k)
			}
		}
	}
	return out, ow, oh
}

// libretroEngineFirst reports whether a relative path is pure engine code, for
// which the system tree must win over the content's copy: old packs ship the
// whole script directory of their release, and running those against the
// current engine only errors. Everything else stays content-first -- data/
// carries the pack's own motif and select screen.
func libretroEngineFirst(p string) bool {
	return strings.HasPrefix(strings.ToLower(filepath.ToSlash(p)), "external/script/")
}

// libretroSearchOrder is the one place that decides where a relative path is
// looked up: the system tree first for engine scripts, last for everything
// else. Without a system tree (standalone build, or the core option off) it is
// just the path itself.
func libretroSearchOrder(p string) []string {
	if libretroEngineRoot == "" || filepath.IsAbs(p) {
		return []string{p}
	}
	rooted := filepath.Join(libretroEngineRoot, p)
	if libretroEngineFirst(p) {
		return []string{rooted, p}
	}
	return []string{p, rooted}
}
