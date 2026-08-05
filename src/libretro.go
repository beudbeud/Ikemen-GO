//go:build libretro

// Libretro core front-end for Ikemen GO.
//
// The engine's main loop is driven by Lua and never returns, so it runs in its
// own goroutine on its own OS thread (it owns the SDL/GL context). retro_run
// and that goroutine hand a single frame back and forth over two channels, so
// only one of them is ever touching engine state at a time.
//
// ponytail: the frame is read back from the hidden window with glReadPixels and
// handed to the frontend as a software framebuffer. That costs one readback and
// one CPU colour-swap per frame, and it keeps every line of the renderer, the
// window code and the Lua loop exactly as-is. The upgrade path, if the readback
// ever shows up in a profile, is RETRO_ENVIRONMENT_SET_HW_RENDER plus making
// Renderer_GL33 bind the frontend's FBO instead of 0.
package main

/*
#include "libretro_glue.h"
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/gopxl/beep/v2"
	"github.com/veandco/go-sdl2/sdl"
)

// The frontend paces us at this rate; see retro_get_system_av_info.
const libretroFPS = 60.0

// How long retro_run waits for a frame before repeating the previous one.
// The engine blocks for seconds at a time while loading characters and stages.
const libretroFrameTimeout = 50 * time.Millisecond

var lr struct {
	frameDone chan struct{} // game -> frontend: a frame is in lr.out
	frameReq  chan struct{} // frontend -> game: go compute the next one
	ready     chan struct{} // closed once the engine has produced its first frame
	readyOnce sync.Once

	started  bool
	quitting bool

	w, h       int
	repW, repH int     // last geometry reported to the frontend (retro thread only)
	raw        []uint8 // straight from the GL readback
	out        []uint8 // XRGB8888, top-down: what the frontend sees

	bgraTried, bgraOK bool // whether the driver accepts a BGRA readback

	applied       map[string]string // option values the running engine was built with
	warnedOptions bool              // an "applies after restart" message is on screen

	speaker  *LibretroSpeaker
	audioAcc float64
	pcm      []int16

	keys map[int]bool       // RETROK_* -> was down last frame
	keyq []libretroKeyEvent // filled by retro_run, drained on the game thread
}

type libretroKeyEvent struct {
	key  Key
	mods ModifierKey
	down bool
}

//export retro_init
func retro_init() {}

//export retro_deinit
func retro_deinit() {}

//export retro_set_controller_port_device
func retro_set_controller_port_device(port C.uint, device C.uint) {}

//export retro_reset
func retro_reset() {
	// ponytail: no reset. The engine has no "restart from scratch" entry point
	// that does not also tear down the GL context, and F1/menu already covers
	// what a player would want here.
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_retro_system_av_info) {
	w, h := lr.w, lr.h
	if w <= 0 || h <= 0 {
		w, h = 640, 480
	}
	// max_* is the frontend's allocation hint and cannot be raised later, so
	// leave room: the engine can recreate its window bigger mid-run.
	maxW, maxH := w, h
	if maxW < 1920 {
		maxW = 1920
	}
	if maxH < 1080 {
		maxH = 1080
	}
	lr.repW, lr.repH = w, h
	info.geometry.base_width = C.uint(w)
	info.geometry.base_height = C.uint(h)
	info.geometry.max_width = C.uint(maxW)
	info.geometry.max_height = C.uint(maxH)
	info.geometry.aspect_ratio = C.float(float32(w) / float32(h))
	info.timing.fps = C.double(libretroFPS)
	info.timing.sample_rate = C.double(libretroSampleRate())
}

//export retro_load_game
func retro_load_game(game *C.struct_retro_game_info) C.bool {
	if lr.started {
		// The engine can only be started once per process: the Go runtime, the
		// GL context and the Lua state all outlive an unload.
		if lr.quitting {
			libretroMessage("Ikemen GO: restart the frontend to load content again")
		}
		return C.bool(!lr.quitting)
	}
	if game == nil || game.path == nil {
		libretroMessage("Ikemen GO: load the game folder, or any file inside it")
		return C.bool(false)
	}

	root := libretroGameRoot(C.GoString(game.path))
	if err := os.Chdir(root); err != nil {
		libretroMessage("Ikemen GO: " + err.Error())
		return C.bool(false)
	}

	libretroUseSystemEngine()
	libretroForceResolution()
	libretroForceRenderer()
	lr.applied = make(map[string]string, len(libretroOptionKeys))
	for _, k := range libretroOptionKeys {
		lr.applied[k] = libretroVariable(k)
	}

	// On a KMS/DRM frontend (Recalbox, a plain RetroArch on a TTY) there is no
	// display server and RetroArch already owns the device, so SDL cannot open
	// a second window. Its offscreen driver renders to an EGL surfaceless
	// context instead, which is all the readback needs. A `gles` core skips SDL
	// video entirely and talks to EGL itself, so it needs none of this.
	if libretroHeadlessGL == nil && os.Getenv("SDL_VIDEODRIVER") == "" &&
		os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		os.Setenv("SDL_VIDEODRIVER", "offscreen")
	}

	pixfmt := C.int(C.RETRO_PIXEL_FORMAT_XRGB8888)
	if !C.ik_env(C.uint(C.RETRO_ENVIRONMENT_SET_PIXEL_FORMAT), unsafe.Pointer(&pixfmt)) {
		libretroMessage("Ikemen GO: frontend does not support XRGB8888")
		return C.bool(false)
	}
	C.ik_set_input_descriptors()

	lr.frameDone = make(chan struct{})
	lr.frameReq = make(chan struct{})
	lr.ready = make(chan struct{})
	lr.keys = make(map[int]bool)
	lr.speaker = &LibretroSpeaker{}

	speaker = lr.speaker // sys.init() keeps a speaker that is already set
	libretroPresent = libretroPresentFrame
	libretroPollInput = libretroDrainKeys
	libretroExit = libretroOnExit
	libretroPads = MaxPlayerNo
	lr.started = true

	go func() {
		runtime.LockOSThread()
		realMain()
	}()

	// Block until the engine has a window, a GL context and a first frame, so
	// retro_get_system_av_info reports the real geometry and sample rate.
	<-lr.ready
	return C.bool(!lr.quitting)
}

//export retro_unload_game
func retro_unload_game() {
	if !lr.started || lr.quitting {
		return
	}
	// Ask the engine to close the way the window's X button would, then keep
	// pumping frames until it has run its own shutdown. Setting sys.gameEnd
	// directly would not work: eventUpdate() overwrites it from shouldClose().
	deadline := time.Now().Add(2 * time.Second)
	for !lr.quitting && time.Now().Before(deadline) {
		select {
		case <-lr.frameDone:
			sys.window.closeflag = true // safe: the game thread is parked
			lr.frameReq <- struct{}{}
		case <-time.After(libretroFrameTimeout):
		}
	}
}

//export retro_run
func retro_run() {
	if !lr.started {
		return
	}
	if lr.quitting {
		C.ik_env(C.uint(C.RETRO_ENVIRONMENT_SHUTDOWN), nil)
		return
	}

	C.ik_input_poll()

	// The two options are only read when content loads (window, engine root and
	// Lua state are all built from them), so a change mid-run cannot be
	// applied; say so instead of silently ignoring it.
	var dirty C.bool
	if bool(C.ik_env(C.uint(C.RETRO_ENVIRONMENT_GET_VARIABLE_UPDATE), unsafe.Pointer(&dirty))) && bool(dirty) {
		libretroWarnPendingOptions()
	}

	select {
	case <-lr.frameDone:
		// The channel receive orders this read after the game thread's write,
		// so lr.w/lr.h are stable here. Tell the frontend before handing it a
		// frame with a new geometry.
		if lr.w != lr.repW || lr.h != lr.repH {
			lr.repW, lr.repH = lr.w, lr.h
			geo := C.struct_retro_game_geometry{
				base_width:   C.uint(lr.w),
				base_height:  C.uint(lr.h),
				aspect_ratio: C.float(float32(lr.w) / float32(lr.h)),
			}
			C.ik_env(C.uint(C.RETRO_ENVIRONMENT_SET_GEOMETRY), unsafe.Pointer(&geo))
		}
		C.ik_video(unsafe.Pointer(&lr.out[0]), C.uint(lr.w), C.uint(lr.h), C.size_t(lr.w*4))
		// The game thread is parked in libretroPresentFrame right now, which is
		// the only moment it is safe to write the shared input state.
		libretroReadInput()
		lr.frameReq <- struct{}{}
	case <-time.After(libretroFrameTimeout):
		// Still loading. A nil frame tells the frontend to repeat the last one.
		C.ik_video(nil, C.uint(lr.w), C.uint(lr.h), C.size_t(lr.w*4))
	}

	libretroPushAudio()
}

// libretroPresentFrame runs on the game thread, right after gfx.EndFrame().
func libretroPresentFrame() {
	if lr.quitting {
		return
	}

	w, h := sys.window.GetSize()
	if w <= 0 || h <= 0 {
		return
	}
	if lr.w != w || lr.h != h {
		lr.w, lr.h = w, h
		lr.raw = make([]uint8, w*h*4)
		lr.out = make([]uint8, w*h*4)
	}
	// A BGRA readback is already XRGB8888, leaving only the row flip; fall
	// back to RGBA plus the CPU channel swap when the driver refuses (checked
	// on every read: the first one decides, a later failure self-heals).
	// Vulkan reads back top-down RGBA and always takes the fallback path.
	if r, ok := gfx.(bgraReader); ok && (!lr.bgraTried || lr.bgraOK) {
		lr.bgraOK = r.ReadPixelsBGRA(lr.raw, w, h)
		lr.bgraTried = true
	}
	if lr.bgraOK {
		libretroFlipRows(lr.out, lr.raw, w, h)
	} else {
		gfx.ReadPixels(lr.raw, w, h)
		libretroConvertFrame(lr.out, lr.raw, w, h, !strings.HasPrefix(gfx.GetName(), "Vulkan"))
	}

	lr.readyOnce.Do(func() { close(lr.ready) })

	lr.frameDone <- struct{}{}
	<-lr.frameReq
}

// bgraReader is implemented by the GL renderers: a readback that already
// matches libretro's XRGB8888 byte order, reporting whether the driver took it.
type bgraReader interface {
	ReadPixelsBGRA(data []uint8, width, height int) bool
}

// libretroFlipRows turns GL's bottom-up rows into the top-down order libretro
// wants; the pixels themselves are already in the right byte order.
func libretroFlipRows(dst, src []uint8, w, h int) {
	stride := w * 4
	for y := 0; y < h; y++ {
		copy(dst[y*stride:(y+1)*stride], src[(h-1-y)*stride:(h-y)*stride])
	}
}

// GL hands back bottom-up RGBA; libretro wants top-down XRGB8888, which is
// B,G,R,X in memory on a little-endian host.
func libretroConvertFrame(dst, src []uint8, w, h int, flip bool) {
	stride := w * 4
	for y := 0; y < h; y++ {
		sy := y
		if flip {
			sy = h - 1 - y
		}
		in := src[sy*stride : sy*stride+stride]
		out := dst[y*stride : y*stride+stride]
		for x := 0; x < stride; x += 4 {
			out[x+0] = in[x+2]
			out[x+1] = in[x+1]
			out[x+2] = in[x+0]
			out[x+3] = 0xff
		}
	}
}

func libretroOnExit() {
	lr.quitting = true
	lr.readyOnce.Do(func() { close(lr.ready) })
}

// libretroGameRoot turns whatever content the user picked into the directory
// the engine should run from: the folder holding data/.
func libretroGameRoot(path string) string {
	dir := path
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		dir = filepath.Dir(path)
	}
	for d, i := dir, 0; i < 3; d, i = filepath.Dir(d), i+1 {
		if fi, err := os.Stat(filepath.Join(d, "data")); err == nil && fi.IsDir() {
			return d
		}
	}
	// A zip often extracts into a subfolder ("Game/Game v2/data"), so look one
	// level down before giving up.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if fi, err := os.Stat(filepath.Join(dir, e.Name(), "data")); err == nil && fi.IsDir() {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return dir
}

// libretroUseSystemEngine honours the "Engine files" core option. Set to
// "System directory", the engine's own files come from <system>/ikemen instead
// of the content folder, so a game folder built for an older Ikemen -- which
// ships that release's data/ and external/ and would otherwise crash in its own
// Lua -- contributes only its motif, chars, stages and sound.
//
// ponytail: no version negotiation, the newer files simply win. Ikemen has no
// data format version to compare, and a pack that needs its own scripts is not
// one this can rescue anyway; leaving the option off is the answer there.
func libretroUseSystemEngine() {
	if libretroVariable("ikemen_go_engine_files") != "System directory" {
		return
	}

	var dir *C.char
	if !C.ik_env(C.uint(C.RETRO_ENVIRONMENT_GET_SYSTEM_DIRECTORY), unsafe.Pointer(&dir)) || dir == nil {
		libretroMessage("Ikemen GO: no system directory, using the content's engine files")
		return
	}
	root := filepath.Join(C.GoString(dir), "ikemen")
	// data/system.def too: a tree with the scripts but not data/ passes the
	// main.lua check, then fails much later with an opaque motif panic.
	for _, f := range []string{
		filepath.Join("external", "script", "main.lua"),
		filepath.Join("data", "system.def"),
	} {
		if fi, err := os.Stat(filepath.Join(root, f)); err != nil || fi.IsDir() {
			libretroMessage("Ikemen GO: install data/, external/ and font/ in " + root)
			return
		}
	}

	libretroEngineRoot = root
	// require() resolves through package.path, which gopher-lua builds from
	// LUA_PATH. The trailing ";;" keeps the default, so the content folder
	// still answers for anything the engine tree does not carry.
	os.Setenv("LUA_PATH", filepath.Join(root, "?.lua")+";;")
	libretroConfigOverride = func(cfg *Config) { libretroRebaseConfig(cfg, root) }
}

var libretroOptionKeys = []string{
	"ikemen_go_engine_files", "ikemen_go_resolution", "ikemen_go_renderer",
}

// libretroForceRenderer honours the "Renderer" core option: override the
// content's RenderMode with a value selectRenderer actually accepts. The gles
// core has a single renderer and ignores RenderMode entirely.
func libretroForceRenderer() {
	switch v := libretroVariable("ikemen_go_renderer"); v {
	case "OpenGL 3.3", "Vulkan 1.3":
		prev := libretroConfigOverride
		libretroConfigOverride = func(cfg *Config) {
			if prev != nil {
				prev(cfg)
			}
			cfg.Video.RenderMode = v
		}
	}
}

// libretroVariable returns the frontend's current value for a core option,
// or "" if it has none.
func libretroVariable(name string) string {
	key := C.CString(name)
	defer C.free(unsafe.Pointer(key))
	if v := C.ik_get_variable(key); v != nil {
		return C.GoString(v)
	}
	return ""
}

// libretroWarnPendingOptions tells the player an option change is waiting for
// a restart, and clears the warning if the change is reverted.
func libretroWarnPendingOptions() {
	pending := false
	for _, k := range libretroOptionKeys {
		if libretroVariable(k) != lr.applied[k] {
			pending = true
		}
	}
	if pending && !lr.warnedOptions {
		libretroMessage("Ikemen GO: core option changes apply after restarting the frontend")
	}
	lr.warnedOptions = pending
}

// libretroResolutionSize turns a "480p"-style option value into a window size
// at the game's own aspect ratio. ok is false for "Content config".
func libretroResolutionSize(value string, gw, gh int32) (w, h int, ok bool) {
	h, err := strconv.Atoi(strings.TrimSuffix(value, "p"))
	if err != nil || h <= 0 {
		return 0, 0, false
	}
	if gw <= 0 || gh <= 0 {
		gw, gh = 4, 3
	}
	w = h * int(gw) / int(gh)
	w += w & 1 // keep the width even: 853x480 -> 854x480
	return w, h, true
}

// libretroForceResolution honours the "Resolution" core option: the frame
// handed to the frontend is rendered at the chosen height, width following
// the content's aspect ratio, without editing its config file. The game's
// logical resolution (GameWidth/GameHeight) is untouched -- stages and
// motifs keep the coordinates they were built for, only the output scales.
func libretroForceResolution() {
	choice := libretroVariable("ikemen_go_resolution")
	if _, _, ok := libretroResolutionSize(choice, 0, 0); !ok {
		return // "Content config"
	}
	prev := libretroConfigOverride
	libretroConfigOverride = func(cfg *Config) {
		if prev != nil {
			prev(cfg)
		}
		w, h, _ := libretroResolutionSize(choice, cfg.Video.GameWidth, cfg.Video.GameHeight)
		cfg.Video.WindowWidth, cfg.Video.WindowHeight = w, h
		cfg.Video.Fullscreen = false // the chosen size must win over the content's
	}
}

// libretroRebasePath repoints an engine-owned relative path at root -- but
// only when the engine tree actually carries the file. The content still
// answers for the rest: an old pack may use a layout the current release has
// moved (data/gofx.def vs data/gofx/gofx.def), and blindly rebasing those
// turns a working reference into a missing file at fight load.
func libretroRebasePath(p, root string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	switch strings.SplitN(filepath.ToSlash(p), "/", 2)[0] {
	case "data", "external", "font":
		if abs := filepath.Join(root, p); FileExist(abs) != "" {
			return abs
		}
	}
	return p
}

// libretroRebaseConfig repoints every engine-owned path at root. Motif is
// deliberately left alone: that one belongs to the content.
func libretroRebaseConfig(cfg *Config, root string) {
	rebase := func(p string) string { return libretroRebasePath(p, root) }
	rebaseAll := func(l []string) {
		for i := range l {
			l[i] = rebase(l[i])
		}
	}
	for _, m := range []map[string][]string{
		cfg.Common.Air, cfg.Common.Cmd, cfg.Common.Const,
		cfg.Common.States, cfg.Common.Fx, cfg.Common.Modules, cfg.Common.Lua,
	} {
		for _, l := range m {
			rebaseAll(l)
		}
	}
	rebaseAll(cfg.Config.WindowIcon)
	cfg.Config.System = rebase(cfg.Config.System)
	cfg.Config.GamepadMappings = rebase(cfg.Config.GamepadMappings)
	cfg.Debug.Font = rebase(cfg.Debug.Font)

	// Anything with a working config.ini is left alone; otherwise hunt for the
	// content's own motif before the default one -- which points into the
	// engine tree -- wins and loads the wrong screenpack, or panics.
	if _, err := os.Stat(cfg.Config.Motif); err != nil {
		if m := libretroFindMotif(); m != "" {
			cfg.Config.Motif = m
		} else {
			libretroMessage("Ikemen GO: no system.def found in the game folder")
		}
	}
}

// libretroFindMotif locates the content's screenpack when its config does not:
// a folder from an older Ikemen keeps the motif path in save/config.json, which
// this engine no longer reads, and M.U.G.E.N packs keep it at data/system.def
// or data/<pack>/system.def.
func libretroFindMotif() string {
	if b, err := os.ReadFile(filepath.Join("save", "config.json")); err == nil {
		var c struct{ Motif string }
		if json.Unmarshal(b, &c) == nil && c.Motif != "" {
			if p := FileExist(c.Motif); p != "" {
				return p
			}
		}
	}
	if p := FileExist("data/system.def"); p != "" { // FileExist: case-insensitive
		return p
	}
	if m, _ := filepath.Glob("data/*/system.def"); len(m) > 0 {
		return m[0]
	}
	return FileExist("system.def")
}

func libretroMessage(text string) {
	msg := C.struct_retro_message{
		msg:    C.CString(text),
		frames: 180,
	}
	defer C.free(unsafe.Pointer(msg.msg))
	C.ik_env(C.uint(C.RETRO_ENVIRONMENT_SET_MESSAGE), unsafe.Pointer(&msg))
	fmt.Fprintln(os.Stderr, text)
}

// The desktop build pops these through GTK (see util_dialog_desktop.go). A core
// has no desktop and no window of its own, so they go to the frontend's OSD.
func ShowInfoDialog(message, title string) {
	libretroMessage(title + ": " + message)
}

func ShowErrorDialog(message string) {
	libretroMessage("I.K.E.M.E.N Error: " + message)
}

// --- input ---------------------------------------------------------------

// RetroPad uses the SNES layout (B bottom, A right, Y left, X top); SDL's
// game controller uses the Xbox one. Ikemen's default joystick config is
// written in SDL button names, so the mapping happens here.
var libretroPadMap = [...]struct {
	id  C.uint
	btn sdl.GameControllerButton
}{
	{C.RETRO_DEVICE_ID_JOYPAD_B, sdl.CONTROLLER_BUTTON_A},
	{C.RETRO_DEVICE_ID_JOYPAD_A, sdl.CONTROLLER_BUTTON_B},
	{C.RETRO_DEVICE_ID_JOYPAD_Y, sdl.CONTROLLER_BUTTON_X},
	{C.RETRO_DEVICE_ID_JOYPAD_X, sdl.CONTROLLER_BUTTON_Y},
	{C.RETRO_DEVICE_ID_JOYPAD_SELECT, sdl.CONTROLLER_BUTTON_BACK},
	{C.RETRO_DEVICE_ID_JOYPAD_START, sdl.CONTROLLER_BUTTON_START},
	{C.RETRO_DEVICE_ID_JOYPAD_L, sdl.CONTROLLER_BUTTON_LEFTSHOULDER},
	{C.RETRO_DEVICE_ID_JOYPAD_R, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER},
	{C.RETRO_DEVICE_ID_JOYPAD_L3, sdl.CONTROLLER_BUTTON_LEFTSTICK},
	{C.RETRO_DEVICE_ID_JOYPAD_R3, sdl.CONTROLLER_BUTTON_RIGHTSTICK},
	{C.RETRO_DEVICE_ID_JOYPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_UP},
	{C.RETRO_DEVICE_ID_JOYPAD_DOWN, sdl.CONTROLLER_BUTTON_DPAD_DOWN},
	{C.RETRO_DEVICE_ID_JOYPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_LEFT},
	{C.RETRO_DEVICE_ID_JOYPAD_RIGHT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT},
}

func libretroReadInput() {
	for port := 0; port < libretroPads && port < len(input.controllerstate); port++ {
		cs := input.controllerstate[port]
		if cs == nil {
			continue
		}
		p := C.uint(port)
		for _, m := range libretroPadMap {
			var v byte
			if C.ik_input_state(p, C.RETRO_DEVICE_JOYPAD, 0, m.id) != 0 {
				v = 1
			}
			cs.Buttons[m.btn] = v
		}
		// Ikemen reads the triggers as axes 4 and 5.
		cs.Axes[4] = libretroTrigger(p, C.RETRO_DEVICE_ID_JOYPAD_L2)
		cs.Axes[5] = libretroTrigger(p, C.RETRO_DEVICE_ID_JOYPAD_R2)
		cs.Axes[0] = libretroAnalog(p, C.RETRO_DEVICE_INDEX_ANALOG_LEFT, C.RETRO_DEVICE_ID_ANALOG_X)
		cs.Axes[1] = libretroAnalog(p, C.RETRO_DEVICE_INDEX_ANALOG_LEFT, C.RETRO_DEVICE_ID_ANALOG_Y)
		cs.Axes[2] = libretroAnalog(p, C.RETRO_DEVICE_INDEX_ANALOG_RIGHT, C.RETRO_DEVICE_ID_ANALOG_X)
		cs.Axes[3] = libretroAnalog(p, C.RETRO_DEVICE_INDEX_ANALOG_RIGHT, C.RETRO_DEVICE_ID_ANALOG_Y)
	}
	libretroReadKeyboard()
}

func libretroTrigger(port, id C.uint) int8 {
	if C.ik_input_state(port, C.RETRO_DEVICE_JOYPAD, 0, id) != 0 {
		return 127
	}
	return 0
}

func libretroAnalog(port, index, id C.uint) int8 {
	return convertI16toI8(int16(C.ik_input_state(port, C.RETRO_DEVICE_ANALOG, index, id)))
}

// RETROK_* values are the old SDL 1.2 keysyms, so they have to be translated to
// the SDL 2 keycodes the engine stores in sys.keyState. ASCII matches on both
// sides; only the named keys need an entry.
//
// F12 (screenshot) and the Alt keys (Alt+Enter toggles fullscreen) are left out
// on purpose: both would run SDL/GL calls on the frontend's thread.
var libretroKeys = func() []struct {
	retro int
	key   Key
} {
	type entry = struct {
		retro int
		key   Key
	}
	out := []entry{
		{8, sdl.K_BACKSPACE}, {9, sdl.K_TAB}, {13, sdl.K_RETURN}, {27, sdl.K_ESCAPE},
		{32, sdl.K_SPACE}, {39, sdl.K_QUOTE}, {44, sdl.K_COMMA}, {45, sdl.K_MINUS},
		{46, sdl.K_PERIOD}, {47, sdl.K_SLASH}, {59, sdl.K_SEMICOLON}, {61, sdl.K_EQUALS},
		{91, sdl.K_LEFTBRACKET}, {92, sdl.K_BACKSLASH}, {93, sdl.K_RIGHTBRACKET},
		{96, sdl.K_BACKQUOTE}, {127, sdl.K_DELETE},
		{266, sdl.K_KP_PERIOD}, {267, sdl.K_KP_DIVIDE}, {268, sdl.K_KP_MULTIPLY},
		{269, sdl.K_KP_MINUS}, {270, sdl.K_KP_PLUS}, {271, sdl.K_KP_ENTER},
		{273, sdl.K_UP}, {274, sdl.K_DOWN}, {275, sdl.K_RIGHT}, {276, sdl.K_LEFT},
		{277, sdl.K_INSERT}, {278, sdl.K_HOME}, {279, sdl.K_END},
		{280, sdl.K_PAGEUP}, {281, sdl.K_PAGEDOWN},
		{300, sdl.K_NUMLOCKCLEAR}, {301, sdl.K_CAPSLOCK}, {302, sdl.K_SCROLLLOCK},
		{303, sdl.K_RSHIFT}, {304, sdl.K_LSHIFT}, {305, sdl.K_RCTRL}, {306, sdl.K_LCTRL},
	}
	for i := 0; i <= 9; i++ { // digits
		out = append(out, entry{48 + i, Key(sdl.K_0 + sdl.Keycode(i))})
	}
	for i := 0; i <= 9; i++ { // keypad digits
		out = append(out, entry{256 + i, Key(sdl.K_KP_0 + sdl.Keycode(i))})
	}
	for i := 0; i < 26; i++ { // letters
		out = append(out, entry{97 + i, Key(sdl.K_a + sdl.Keycode(i))})
	}
	for i := 0; i < 11; i++ { // F1..F11
		out = append(out, entry{282 + i, Key(sdl.K_F1 + sdl.Keycode(i))})
	}
	return out
}()

// libretroReadKeyboard turns the frontend's key levels into press/release
// events. They are queued rather than delivered here: eventUpdate() clears
// sys.esc before calling pollEvents, so an Escape delivered any earlier would
// be wiped out in the same frame. libretroDrainKeys does the delivery.
func libretroReadKeyboard() {
	var mods ModifierKey
	if lr.keys[303] || lr.keys[304] {
		mods |= sdl.KMOD_SHIFT
	}
	if lr.keys[305] || lr.keys[306] {
		mods |= sdl.KMOD_CTRL
	}
	for _, k := range libretroKeys {
		down := C.ik_input_state(0, C.RETRO_DEVICE_KEYBOARD, 0, C.uint(k.retro)) != 0
		if down == lr.keys[k.retro] {
			continue
		}
		lr.keys[k.retro] = down
		lr.keyq = append(lr.keyq, libretroKeyEvent{k.key, mods, down})
	}
}

// libretroDrainKeys runs on the game thread, from Window.pollEvents. retro_run
// only appends while that thread is parked, so no lock is needed.
func libretroDrainKeys() {
	for _, e := range lr.keyq {
		if e.down {
			OnKeyPressed(e.key, e.mods)
		} else {
			OnKeyReleased(e.key, e.mods)
		}
	}
	lr.keyq = lr.keyq[:0]
}

// --- audio ---------------------------------------------------------------

func libretroSampleRate() int {
	if lr.speaker != nil && lr.speaker.rate > 0 {
		return lr.speaker.rate
	}
	return 44100
}

func libretroPushAudio() {
	if lr.speaker == nil || lr.speaker.mixer == nil {
		return
	}
	// The sample count per frame is not an integer at 44.1kHz/60fps, so carry
	// the remainder instead of drifting.
	lr.audioAcc += float64(lr.speaker.rate) / libretroFPS
	n := int(lr.audioAcc)
	lr.audioAcc -= float64(n)
	if n <= 0 {
		return
	}
	if cap(lr.pcm) < n*2 {
		lr.pcm = make([]int16, n*2)
	}
	lr.pcm = lr.pcm[:n*2]
	lr.speaker.Read(lr.pcm)
	C.ik_audio_batch((*C.int16_t)(unsafe.Pointer(&lr.pcm[0])), C.size_t(n))
}

// LibretroSpeaker replaces SDLSpeaker: instead of pushing to an audio device on
// its own goroutine, the mixer is pulled once per retro_run.
type LibretroSpeaker struct {
	mixer *beep.Mixer
	mu    sync.Mutex
	rate  int
	buf   [][2]float64
}

func (s *LibretroSpeaker) Init(sampleRate beep.SampleRate, bufferSize int) error {
	s.mixer = &beep.Mixer{}
	s.rate = int(sampleRate)
	return nil
}

func (s *LibretroSpeaker) Play(st beep.Streamer) {
	s.mu.Lock()
	s.mixer.Add(st)
	s.mu.Unlock()
}

func (s *LibretroSpeaker) Lock()      { s.mu.Lock() }
func (s *LibretroSpeaker) Unlock()    { s.mu.Unlock() }
func (s *LibretroSpeaker) Close()     {}
func (s *LibretroSpeaker) FillAudio() {}

// Read fills out with len(out)/2 interleaved stereo frames from the mixer.
func (s *LibretroSpeaker) Read(out []int16) {
	n := len(out) / 2
	if cap(s.buf) < n {
		s.buf = make([][2]float64, n)
	}
	s.buf = s.buf[:n]

	s.mu.Lock()
	got, _ := s.mixer.Stream(s.buf)
	s.mu.Unlock()

	for i := 0; i < got; i++ {
		out[i*2] = floatToS16(s.buf[i][0])
		out[i*2+1] = floatToS16(s.buf[i][1])
	}
	for i := got * 2; i < len(out); i++ {
		out[i] = 0
	}
}
