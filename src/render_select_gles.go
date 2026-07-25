//go:build !android && gles

package main

// The `gles` tag builds an OpenGL ES-only engine: no libGL, no Vulkan. It is
// what embedded GPUs want -- Broadcom V3D, Mali, Adreno all expose GL ES and
// either no desktop GL at all or a version below the 3.3 the GL33 renderer
// needs. There is nothing to choose between here, so RenderMode is ignored.
func selectRenderer(cfgVal string) (Renderer, FontRenderer) {
	return &Renderer_GLES32{}, &FontRenderer_GLES32{}
}
