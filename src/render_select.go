//go:build !android && !gles

package main

import "fmt"

func selectRenderer(cfgVal string) (Renderer, FontRenderer) {
	var gfx Renderer
	var gfxFont FontRenderer

	// Now we proceed to init the render.
	switch cfgVal {
	case "OpenGL 3.3":
		gfx = &Renderer_GL33{}
		gfxFont = &FontRenderer_GL33{}
	case "Vulkan 1.3":
		gfx = &Renderer_VK{}
		gfxFont = &FontRenderer_VK{}
	default:
		fmt.Printf("Error: Invalid RenderMode '%s'. Defaulting to OpenGL 3.3.\n", cfgVal)
		gfx = &Renderer_GL33{}
		gfxFont = &FontRenderer_GL33{}
	}

	return gfx, gfxFont
}
