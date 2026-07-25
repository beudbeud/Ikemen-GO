package main

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
)
