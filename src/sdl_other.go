//go:build !ios

package main

import "C"

// Gets the actual device pixel size
func (w *Window) GetDrawablePixelSize() (int32, int32) {
	// w.GetSize, not w.Window.GetSize: a headless libretro window has no SDL window
	winWidth, winHeight := w.GetSize()
	return int32(winWidth), int32(winHeight)
}

func attachIOSMetalLayer(windowID uint32) {
	// NOOP
}

func setOrientationHints() {
	// NOOP
}
