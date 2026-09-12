//go:build libretro && gles

package main

// A GL context with no window and no display server.
//
// The SDL path needs a video driver to hand out a GL context, and on a KMS/DRM
// frontend there is none to be had: RetroArch holds the DRM master, so SDL's
// kmsdrm driver cannot open a second one, and there is no X or Wayland server
// to fall back to. EGL has no such problem -- Mesa's surfaceless platform binds
// a render node, which needs no master and coexists with the frontend.
//
// The context gets a pbuffer rather than being made current surfaceless, so
// framebuffer 0 stays a real, readable framebuffer. The renderer draws to it
// exactly as it would to a window, and libretroPresentFrame reads it back.

/*
#cgo pkg-config: egl glesv2
#include <EGL/egl.h>
#include <stdlib.h>
#include "libretro_glue.h"
#include "libretro_egl_glue.h"

// Resolved at runtime so this builds against an EGL 1.4 header too.
#define IK_EGL_PLATFORM_SURFACELESS_MESA 0x31DD

typedef EGLDisplay (*ik_get_platform_display)(EGLenum, void *, const EGLint *);

static EGLDisplay ik_surfaceless_display(void) {
	ik_get_platform_display f =
		(ik_get_platform_display)eglGetProcAddress("eglGetPlatformDisplayEXT");
	if (f == NULL)
		return EGL_NO_DISPLAY;
	return f(IK_EGL_PLATFORM_SURFACELESS_MESA, EGL_DEFAULT_DISPLAY, NULL);
}
*/
import "C"

import (
	"fmt"
	"os"

	gl "github.com/leonkasovan/gl/v3.2/gles2"
)

func init() {
	libretroHeadlessGL = libretroEGLContext
	libretroHW.export = libretroHWExport
	libretroHW.present = libretroHWPresent
}

// The engine's own display and context, for the dma-buf export.
var libretroEGL struct {
	dpy C.EGLDisplay
	ctx C.EGLContext
}

// libretroEGLContext leaves the context current on the calling thread, which is
// the game thread -- the only thread that ever issues GL calls.
func libretroEGLContext(w, h int) error {
	dpy := C.ik_surfaceless_display()
	if dpy == C.EGLDisplay(C.EGL_NO_DISPLAY) {
		// Mesa's surfaceless platform is the normal route. Without it, ask for
		// whatever the default device is and let EGL decide.
		dpy = C.eglGetDisplay(C.EGLNativeDisplayType(C.EGL_DEFAULT_DISPLAY))
	}
	if dpy == C.EGLDisplay(C.EGL_NO_DISPLAY) {
		return fmt.Errorf("no EGL display available")
	}

	var major, minor C.EGLint
	if C.eglInitialize(dpy, &major, &minor) == C.EGL_FALSE {
		return eglErr("eglInitialize")
	}
	if C.eglBindAPI(C.EGL_OPENGL_ES_API) == C.EGL_FALSE {
		return eglErr("eglBindAPI")
	}

	cfgAttrs := []C.EGLint{
		C.EGL_SURFACE_TYPE, C.EGL_PBUFFER_BIT,
		C.EGL_RENDERABLE_TYPE, 0x0040, // EGL_OPENGL_ES3_BIT_KHR
		C.EGL_RED_SIZE, 8,
		C.EGL_GREEN_SIZE, 8,
		C.EGL_BLUE_SIZE, 8,
		C.EGL_ALPHA_SIZE, 8,
		C.EGL_DEPTH_SIZE, 24,
		C.EGL_NONE,
	}
	var cfg C.EGLConfig
	var n C.EGLint
	if C.eglChooseConfig(dpy, &cfgAttrs[0], &cfg, 1, &n) == C.EGL_FALSE || n < 1 {
		return eglErr("eglChooseConfig (no pbuffer-capable GLES 3 config)")
	}

	pbAttrs := []C.EGLint{C.EGL_WIDTH, C.EGLint(w), C.EGL_HEIGHT, C.EGLint(h), C.EGL_NONE}
	surf := C.eglCreatePbufferSurface(dpy, cfg, &pbAttrs[0])
	if surf == C.EGLSurface(C.EGL_NO_SURFACE) {
		return eglErr("eglCreatePbufferSurface")
	}

	// Ask for 3.0, the floor the renderer needs. Drivers that can do more hand
	// back a context reporting their real version, so nothing is lost by not
	// asking for 3.2 -- and boards that stop at 3.1 (Broadcom V3D, most Mali)
	// still get a context instead of a failure.
	ctxAttrs := []C.EGLint{C.EGL_CONTEXT_CLIENT_VERSION, 3, C.EGL_NONE}
	ctx := C.eglCreateContext(dpy, cfg, C.EGLContext(C.EGL_NO_CONTEXT), &ctxAttrs[0])
	if ctx == C.EGLContext(C.EGL_NO_CONTEXT) {
		return eglErr("eglCreateContext")
	}
	if C.eglMakeCurrent(dpy, surf, surf, ctx) == C.EGL_FALSE {
		return eglErr("eglMakeCurrent")
	}
	libretroEGL.dpy, libretroEGL.ctx = dpy, ctx
	return nil
}

// --- hardware rendering: frames shared with the frontend as dma-bufs -------
//
// The engine keeps the context above and draws its final pass into one of two
// textures whose storage is exported as a dma-buf. Inside retro_run, on the
// frontend's thread and context, the same buffer is imported and blitted into
// the framebuffer the frontend asked us to draw into. No readback, no CPU
// copy, and the frontend may recreate its context whenever it likes: the
// engine's GL objects never live there.
//
// ponytail: ordering between the two contexts relies on the kernel's implicit
// fencing on the shared buffer (every v3d job waits for earlier jobs on the
// buffers it touches). If a driver ever shows tearing here, the upgrade path
// is an EGL_ANDROID_native_fence_sync fence handed over with the frame.
var libretroHWState struct {
	w, h   int
	frames [2]struct {
		tex, fbo, depth uint32
		buf             C.ik_dmabuf
	}
	cur    int // the renderer draws into this one now
	handed int // handed to the frontend; read on the retro thread while the game thread is parked
	failed bool
	warned bool
}

// libretroHWExport runs on the game thread after the frame's final pass:
// it hands the current texture over and points the renderer at the other.
func libretroHWExport(w, h int) bool {
	hw := &libretroHWState
	r, ok := gfx.(*Renderer_GLES32)
	if !ok || hw.failed {
		return false
	}
	if hw.w != w || hw.h != h {
		// ponytail: the previous pair leaks on a resize; it is a rare event
		// and the frontend's import still refers to those buffers.
		for i := range hw.frames {
			f := &hw.frames[i]
			gl.GenTextures(1, &f.tex)
			gl.BindTexture(gl.TEXTURE_2D, f.tex)
			gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, int32(w), int32(h), 0, gl.RGBA, gl.UNSIGNED_BYTE, nil)
			gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
			gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
			gl.GenFramebuffers(1, &f.fbo)
			gl.BindFramebuffer(gl.FRAMEBUFFER, f.fbo)
			gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, f.tex, 0)
			// The renderer draws the scene straight in here when it can skip
			// its final copy, and that path clears and tests depth.
			gl.GenRenderbuffers(1, &f.depth)
			gl.BindRenderbuffer(gl.RENDERBUFFER, f.depth)
			gl.RenderbufferStorage(gl.RENDERBUFFER, gl.DEPTH_COMPONENT16, int32(w), int32(h))
			gl.BindRenderbuffer(gl.RENDERBUFFER, 0)
			gl.FramebufferRenderbuffer(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.RENDERBUFFER, f.depth)
			if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE ||
				!bool(C.ik_dmabuf_export(libretroEGL.dpy, libretroEGL.ctx, C.uint(f.tex), &f.buf)) {
				hw.failed = true
				fmt.Fprintln(os.Stderr, "Ikemen GO: GPU frame sharing failed:", C.GoString(C.ik_dmabuf_error()))
				return false
			}
		}
		hw.w, hw.h, hw.cur = w, h, 0
		// This frame already went to the previous target: carry it over.
		prev := r.SetPresentFramebuffer(hw.frames[0].fbo)
		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, prev)
		gl.BindFramebuffer(gl.DRAW_FRAMEBUFFER, hw.frames[0].fbo)
		gl.BlitFramebuffer(0, 0, int32(w), int32(h), 0, 0, int32(w), int32(h), gl.COLOR_BUFFER_BIT, gl.NEAREST)
	}
	gl.Flush() // submit the frame's jobs: the frontend's blit orders after them
	hw.handed = hw.cur
	hw.cur ^= 1
	r.SetPresentFramebuffer(hw.frames[hw.cur].fbo)
	return true
}

// libretroHWPresent runs on the retro thread inside retro_run, with the
// frontend's context current.
func libretroHWPresent(w, h int) {
	hw := &libretroHWState
	gen := C.ik_hw_generation()
	if gen == 0 || hw.failed || hw.w == 0 {
		C.ik_video(nil, C.uint(w), C.uint(h), 0) // nothing shareable yet: repeat the last frame
		return
	}
	f := &hw.frames[hw.handed]
	if !bool(C.ik_dmabuf_blit(C.int(hw.handed), &f.buf, C.uint(w), C.uint(h), gen, C.ik_hw_framebuffer())) {
		if !hw.warned {
			hw.warned = true
			libretroMessage("Ikemen GO: GPU frame sharing failed: " + C.GoString(C.ik_dmabuf_error()))
		}
		C.ik_video(nil, C.uint(w), C.uint(h), 0)
		return
	}
	C.ik_video_hw(C.uint(w), C.uint(h))
}

func eglErr(what string) error {
	return fmt.Errorf("%s failed: EGL error 0x%04x", what, uint32(C.eglGetError()))
}
