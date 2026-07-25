//go:build gles && !android

package main

/*
#cgo pkg-config: egl
#include <EGL/egl.h>
#include <stdlib.h>
*/
import "C"

import "unsafe"

// The GLES renderer resolves its entry points through EGL. On Android that
// comes from util_android.go via SDL's headers; everywhere else libEGL provides
// it directly.
func eglGetProcAddress(name string) unsafe.Pointer {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	return unsafe.Pointer(C.eglGetProcAddress(cname))
}
