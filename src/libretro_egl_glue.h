/* dma-buf hand-over between the engine's EGL context and the frontend's.
 * Bodies in libretro_egl_glue.c (cgo forbids them in a preamble). */
#ifndef IKEMEN_LIBRETRO_EGL_GLUE_H
#define IKEMEN_LIBRETRO_EGL_GLUE_H

#include <stdbool.h>
#include <stdint.h>
#include <EGL/egl.h>

typedef struct {
	int      fd;
	int      fourcc;
	int      stride;
	int      offset;
	uint64_t modifier;
} ik_dmabuf;

/* Engine thread, engine context current: export the storage of a GL texture.
 * The texture must keep that storage, so never re-specify it afterwards. */
bool ik_dmabuf_export(EGLDisplay dpy, EGLContext ctx, unsigned tex, ik_dmabuf *out);

/* Frontend thread, frontend context current: blit an exported texture into
 * target_fbo. slot selects the import cache entry (one per exported texture);
 * generation is the frontend's context generation, a change re-imports. */
bool ik_dmabuf_blit(int slot, const ik_dmabuf *buf, unsigned width, unsigned height,
                    unsigned generation, uintptr_t target_fbo);

/* Last failure of the two calls above, for the log. */
const char *ik_dmabuf_error(void);

#endif
