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
#cgo pkg-config: egl
#include <EGL/egl.h>
#include <stdlib.h>

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
)

func init() {
	libretroHeadlessGL = libretroEGLContext
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
	return nil
}

func eglErr(what string) error {
	return fmt.Errorf("%s failed: EGL error 0x%04x", what, uint32(C.eglGetError()))
}
