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

// libretroEngineFirst reports whether a relative path is pure engine code, for
// which the system tree must win over the content's copy: old packs ship the
// whole script directory of their release, and running those against the
// current engine only errors. Everything else stays content-first -- data/
// carries the pack's own motif and select screen.
func libretroEngineFirst(p string) bool {
	return strings.HasPrefix(strings.ToLower(filepath.ToSlash(p)), "external/script/")
}
