//go:build libretro && gles

#include "libretro_egl_glue.h"

#include <EGL/eglext.h>
#include <GLES3/gl3.h>
#include <GLES2/gl2ext.h>
#include <stddef.h>
#include <string.h>

static const char *dmabuf_err = "";

const char *ik_dmabuf_error(void) { return dmabuf_err; }

#define PROC(type, name) type name = (type)eglGetProcAddress(#name); \
	if (!name) { dmabuf_err = #name " missing"; return false; }

bool ik_dmabuf_export(EGLDisplay dpy, EGLContext ctx, unsigned tex, ik_dmabuf *out)
{
	PROC(PFNEGLCREATEIMAGEKHRPROC, eglCreateImageKHR)
	PROC(PFNEGLEXPORTDMABUFIMAGEQUERYMESAPROC, eglExportDMABUFImageQueryMESA)
	PROC(PFNEGLEXPORTDMABUFIMAGEMESAPROC, eglExportDMABUFImageMESA)

	const EGLint attrs[] = { EGL_GL_TEXTURE_LEVEL_KHR, 0, EGL_NONE };
	/* Kept alive on purpose: it pins the texture to this storage. */
	EGLImageKHR img = eglCreateImageKHR(dpy, ctx, EGL_GL_TEXTURE_2D_KHR,
	                                    (EGLClientBuffer)(uintptr_t)tex, attrs);
	if (img == EGL_NO_IMAGE_KHR) {
		dmabuf_err = "eglCreateImageKHR(texture) failed";
		return false;
	}
	int fourcc = 0, planes = 0;
	EGLuint64KHR modifier = 0;
	if (!eglExportDMABUFImageQueryMESA(dpy, img, &fourcc, &planes, &modifier) || planes != 1) {
		dmabuf_err = "eglExportDMABUFImageQueryMESA failed or multi-planar";
		return false;
	}
	int fd = -1;
	EGLint stride = 0, offset = 0;
	if (!eglExportDMABUFImageMESA(dpy, img, &fd, &stride, &offset) || fd < 0) {
		dmabuf_err = "eglExportDMABUFImageMESA failed";
		return false;
	}
	out->fd = fd;
	out->fourcc = fourcc;
	out->stride = stride;
	out->offset = offset;
	out->modifier = modifier;
	return true;
}

/* The frontend side lives and dies with the frontend's context: a new
 * generation means the old objects are already gone, so just re-import. */
static struct {
	unsigned generation;
	int      fd;
	GLuint   tex, fbo;
} slots[2];

bool ik_dmabuf_blit(int slot, const ik_dmabuf *buf, unsigned width, unsigned height,
                    unsigned generation, uintptr_t target_fbo)
{
	if (slot < 0 || slot > 1) {
		dmabuf_err = "bad slot";
		return false;
	}
	if (slots[slot].generation != generation || slots[slot].fd != buf->fd) {
		PROC(PFNEGLCREATEIMAGEKHRPROC, eglCreateImageKHR)
		PROC(PFNGLEGLIMAGETARGETTEXTURE2DOESPROC, glEGLImageTargetTexture2DOES)

		EGLDisplay dpy = eglGetCurrentDisplay();
		const EGLint attrs[] = {
			EGL_WIDTH, (EGLint)width,
			EGL_HEIGHT, (EGLint)height,
			EGL_LINUX_DRM_FOURCC_EXT, buf->fourcc,
			EGL_DMA_BUF_PLANE0_FD_EXT, buf->fd,
			EGL_DMA_BUF_PLANE0_OFFSET_EXT, buf->offset,
			EGL_DMA_BUF_PLANE0_PITCH_EXT, buf->stride,
			EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT, (EGLint)(buf->modifier & 0xffffffffu),
			EGL_DMA_BUF_PLANE0_MODIFIER_HI_EXT, (EGLint)(buf->modifier >> 32),
			EGL_NONE
		};
		EGLImageKHR img = eglCreateImageKHR(dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT, NULL, attrs);
		if (img == EGL_NO_IMAGE_KHR) {
			dmabuf_err = "eglCreateImageKHR(dma-buf) failed";
			return false;
		}
		GLuint tex = 0, fbo = 0;
		glGenTextures(1, &tex);
		glBindTexture(GL_TEXTURE_2D, tex);
		glEGLImageTargetTexture2DOES(GL_TEXTURE_2D, img);
		glGenFramebuffers(1, &fbo);
		glBindFramebuffer(GL_READ_FRAMEBUFFER, fbo);
		glFramebufferTexture2D(GL_READ_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0);
		if (glCheckFramebufferStatus(GL_READ_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE) {
			dmabuf_err = "imported framebuffer incomplete";
			return false;
		}
		slots[slot].generation = generation;
		slots[slot].fd = buf->fd;
		slots[slot].tex = tex;
		slots[slot].fbo = fbo;
	}
	glDisable(GL_SCISSOR_TEST);
	glBindFramebuffer(GL_READ_FRAMEBUFFER, slots[slot].fbo);
	glBindFramebuffer(GL_DRAW_FRAMEBUFFER, (GLuint)target_fbo);
	glBlitFramebuffer(0, 0, (GLint)width, (GLint)height, 0, 0, (GLint)width, (GLint)height,
	                  GL_COLOR_BUFFER_BIT, GL_NEAREST);
	return true;
}
