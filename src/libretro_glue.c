//go:build libretro

/* Frontend callback storage plus the trivial libretro entry points that carry
 * no engine state. Everything with behaviour lives in libretro.go. */
#include "libretro_glue.h"

static retro_environment_t        environ_cb;
static retro_video_refresh_t      video_cb;
static retro_audio_sample_t       audio_cb;
static retro_audio_sample_batch_t audio_batch_cb;
static retro_input_poll_t         input_poll_cb;
static retro_input_state_t        input_state_cb;

/* Core options. The frontend wants these while it is still setting callbacks,
 * before any content exists, so they are declared here rather than from Go.
 * The v2 tables carry the descriptions; the retro_variable table is the
 * fallback for frontends predating options v2. Keep both in sync. */
static struct retro_core_option_v2_definition ik_option_defs[] = {
	{ "ikemen_go_engine_files",
	  "Engine files", NULL,
	  "Where the engine's own data/, external/ and font/ come from. 'System "
	  "directory' reads them from <system>/ikemen, so packs built for older "
	  "engine releases run on this core's scripts. Applied when content loads.",
	  NULL, NULL,
	  { { "Content folder", NULL }, { "System directory", NULL }, { NULL, NULL } },
	  "Content folder" },
	{ "ikemen_go_resolution",
	  "Output resolution", NULL,
	  "Size of the image handed to the frontend, width following the game's "
	  "aspect ratio. Does NOT change the game's internal resolution or "
	  "framing -- see 'Game resolution' for that. 'Content config' keeps the "
	  "pack's own window size. Applied when content loads.",
	  NULL, NULL,
	  { { "Content config", NULL }, { "240p", NULL }, { "480p", NULL },
	    { "720p", NULL }, { "1080p", NULL }, { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_game_resolution",
	  "Game resolution", NULL,
	  "Force the game's internal resolution (GameWidth/GameHeight), the way "
	  "editing the pack's config.ini would: framing, aspect and screenpack "
	  "layout follow. A pack laid out for another aspect may misplace "
	  "elements. Applied when content loads.",
	  NULL, NULL,
	  { { "Content config", NULL }, { "320x240 (4:3)", NULL },
	    { "640x480 (4:3)", NULL }, { "854x480 (16:9)", NULL },
	    { "1280x720 (16:9)", NULL }, { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_renderer",
	  "Renderer", NULL,
	  "Override the content's RenderMode, for packs whose config asks for a "
	  "renderer this machine does not have. The GL ES core has a single "
	  "renderer and ignores this. Applied when content loads.",
	  NULL, NULL,
	  { { "Content config", NULL }, { "OpenGL 3.3", NULL },
	    { "Vulkan 1.3", NULL }, { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_sprite_detail",
	  "Sprite detail", NULL,
	  "Resolution of large sprite textures, which on a shared-memory GPU live "
	  "in system RAM. 'Auto' matches the 'Resolution' option: a 720p pack "
	  "shown at 480p uploads at half detail (a quarter of the RAM), invisible "
	  "at that output. Fonts and UI always keep full detail. Applied when "
	  "content loads.",
	  NULL, NULL,
	  { { "Auto", NULL }, { "Full", NULL }, { "Half", NULL }, { NULL, NULL } },
	  "Auto" },
	{ NULL, NULL, NULL, NULL, NULL, NULL, { { NULL, NULL } }, NULL }
};

static struct retro_core_option_v2_category ik_option_cats[] = {
	{ NULL, NULL, NULL }
};

static struct retro_core_options_v2 ik_options_v2 = {
	ik_option_cats, ik_option_defs
};

static const struct retro_variable ik_options[] = {
	{ "ikemen_go_engine_files",
	  "Engine files; Content folder|System directory" },
	{ "ikemen_go_resolution",
	  "Output resolution; Content config|240p|480p|720p|1080p" },
	{ "ikemen_go_game_resolution",
	  "Game resolution; Content config|320x240 (4:3)|640x480 (4:3)|854x480 (16:9)|1280x720 (16:9)" },
	{ "ikemen_go_renderer",
	  "Renderer; Content config|OpenGL 3.3|Vulkan 1.3" },
	{ "ikemen_go_sprite_detail",
	  "Sprite detail; Auto|Full|Half" },
	{ NULL, NULL }
};

IK_API void retro_set_environment(retro_environment_t cb)
{
	unsigned ver = 0;
	environ_cb = cb;
	cb(RETRO_ENVIRONMENT_GET_CORE_OPTIONS_VERSION, &ver);
	if (ver < 2 || !cb(RETRO_ENVIRONMENT_SET_CORE_OPTIONS_V2, &ik_options_v2))
		cb(RETRO_ENVIRONMENT_SET_VARIABLES, (void *)ik_options);
}

const char *ik_get_variable(const char *key)
{
	struct retro_variable var = { key, NULL };
	if (!environ_cb || !environ_cb(RETRO_ENVIRONMENT_GET_VARIABLE, &var))
		return NULL;
	return var.value;
}

IK_API void retro_set_video_refresh(retro_video_refresh_t cb)      { video_cb = cb; }
IK_API void retro_set_audio_sample(retro_audio_sample_t cb)        { audio_cb = cb; }
IK_API void retro_set_audio_sample_batch(retro_audio_sample_batch_t cb) { audio_batch_cb = cb; }
IK_API void retro_set_input_poll(retro_input_poll_t cb)            { input_poll_cb = cb; }
IK_API void retro_set_input_state(retro_input_state_t cb)          { input_state_cb = cb; }

bool ik_env(unsigned cmd, void *data)
{
	return environ_cb ? environ_cb(cmd, data) : false;
}

void ik_video(const void *data, unsigned width, unsigned height, size_t pitch)
{
	if (video_cb)
		video_cb(data, width, height, pitch);
}

size_t ik_audio_batch(const int16_t *data, size_t frames)
{
	return audio_batch_cb ? audio_batch_cb(data, frames) : 0;
}

void ik_input_poll(void)
{
	if (input_poll_cb)
		input_poll_cb();
}

int16_t ik_input_state(unsigned port, unsigned device, unsigned index, unsigned id)
{
	return input_state_cb ? input_state_cb(port, device, index, id) : 0;
}

/* Static table, so it stays valid after the call returns. */
void ik_set_input_descriptors(void)
{
	static struct retro_input_descriptor desc[] = {
#define IK_PORT(p) \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_LEFT,   "Left" },  \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_UP,     "Up" },    \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_DOWN,   "Down" },  \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_RIGHT,  "Right" }, \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_B,      "Light Punch (a)" },  \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_A,      "Medium Punch (b)" }, \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_R2,     "Strong Punch (c)" }, \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_Y,      "Light Kick (x)" },   \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_X,      "Medium Kick (y)" },  \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_R,      "Strong Kick (z)" },  \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_L,      "Taunt (d)" },        \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_L2,     "Assist (w)" },       \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_START,  "Start" },            \
		{ p, RETRO_DEVICE_JOYPAD, 0, RETRO_DEVICE_ID_JOYPAD_SELECT, "Menu" },
		IK_PORT(0)
		IK_PORT(1)
		IK_PORT(2)
		IK_PORT(3)
#undef IK_PORT
		{ 0 },
	};
	ik_env(RETRO_ENVIRONMENT_SET_INPUT_DESCRIPTORS, desc);
}

/* No-op entry points required by the API. */
IK_API unsigned retro_api_version(void)                                { return RETRO_API_VERSION; }
IK_API unsigned retro_get_region(void)                                 { return RETRO_REGION_NTSC; }
IK_API void    *retro_get_memory_data(unsigned id)                     { (void)id; return NULL; }
IK_API size_t   retro_get_memory_size(unsigned id)                     { (void)id; return 0; }
IK_API size_t   retro_serialize_size(void)                             { return 0; }
IK_API bool     retro_serialize(void *d, size_t s)                     { (void)d; (void)s; return false; }
IK_API bool     retro_unserialize(const void *d, size_t s)             { (void)d; (void)s; return false; }
IK_API void     retro_cheat_reset(void)                                {}
IK_API void     retro_cheat_set(unsigned i, bool e, const char *c)     { (void)i; (void)e; (void)c; }
IK_API bool     retro_load_game_special(unsigned t, const struct retro_game_info *i, size_t n)
{
	(void)t; (void)i; (void)n;
	return false;
}

IK_API void retro_get_system_info(struct retro_system_info *info)
{
	info->library_name     = "Ikemen GO";
	info->library_version  = "git";
	info->valid_extensions = "def|ikemen";
	info->need_fullpath    = true;
	info->block_extract    = true;
}
