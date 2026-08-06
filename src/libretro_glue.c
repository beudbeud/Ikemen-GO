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
	  "System > Engine files", "Engine files",
	  "Where the engine's own data/, external/ and font/ come from. 'System "
	  "directory' reads them from <system>/ikemen, so packs built for older "
	  "engine releases run on this core's scripts; without that tree installed "
	  "it falls back to the content folder. Applied when content loads.",
	  NULL, "system",
	  { { "System directory", NULL }, { "Content folder", NULL }, { NULL, NULL } },
	  "System directory" },
	{ "ikemen_go_renderer",
	  "System > Renderer", "Renderer",
	  "Override the content's RenderMode, for packs whose config asks for a "
	  "renderer this machine does not have. The GL ES core has a single "
	  "renderer and ignores this. Applied when content loads.",
	  NULL, "system",
	  { { "Content config", NULL }, { "OpenGL 3.3", NULL },
	    { "Vulkan 1.3", NULL }, { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_debug",
	  "System > Debug keys", "Debug keys",
	  "The engine's keyboard debug tools (F1 kills a fighter, F4 resets the "
	  "round, Ctrl+D opens the debug overlay...). Packs usually ship them "
	  "enabled; 'Off' disables them so a stray key on a plugged-in keyboard "
	  "cannot disrupt a match. Applied when content loads.",
	  NULL, "system",
	  { { "Off", NULL }, { "On", NULL }, { NULL, NULL } },
	  "Off" },
	{ "ikemen_go_ram_saver",
	  "System > RAM saver", "RAM saver",
	  "Cap the engine limits that dominate memory on small boards: 2 players "
	  "at once, 3D model shadows off, fewer afterimages, explods, projectiles "
	  "and sound channels. Values already lower in the pack's config are kept. "
	  "Applied when content loads.",
	  NULL, "system",
	  { { "Off", NULL }, { "On", NULL }, { NULL, NULL } },
	  "Off" },
	{ "ikemen_go_resolution",
	  "Video > Resolution", "Resolution",
	  "Run the game at this resolution, the way editing GameWidth/GameHeight "
	  "in the pack's config.ini would: framing, aspect and screenpack layout "
	  "follow. With 'Sprite detail: Auto', sprite textures shrink to match "
	  "when the pack was authored bigger. Applied when content loads.",
	  NULL, "video",
	  { { "Content config", NULL }, { "320x240 (4:3)", NULL },
	    { "640x480 (4:3)", NULL }, { "720x480 (3:2)", NULL },
	    { "854x480 (16:9)", NULL }, { "1280x720 (16:9)", NULL },
	    { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_fight_aspect",
	  "Video > Fight aspect", "Fight aspect",
	  "Aspect ratio of matches. 'Stage' follows each stage's own design, so a "
	  "pack mixing 4:3 and widescreen stages changes shape between fights; a "
	  "fixed value keeps every fight framed the same. Menus always follow the "
	  "game resolution. Applied when content loads.",
	  NULL, "video",
	  { { "Content config", NULL }, { "Stage", NULL }, { "4:3", NULL },
	    { "16:9", NULL }, { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_sprite_detail",
	  "Video > Sprite detail", "Sprite detail",
	  "Resolution of large sprite textures, which on a shared-memory GPU live "
	  "in system RAM. 'Auto' shrinks true-color sprites to match the "
	  "'Resolution' option -- a 720p pack played at 480p uploads them at half "
	  "detail, a quarter of the RAM -- but never touches palette-indexed "
	  "pixel art, which halving visibly ruins and which is cheap anyway. "
	  "'Half' shrinks everything. Fonts and UI always keep full detail. "
	  "Applied when content loads.",
	  NULL, "video",
	  { { "Auto", NULL }, { "Full", NULL }, { "Half", NULL }, { NULL, NULL } },
	  "Auto" },
	{ "ikemen_go_rgb_filter",
	  "Video > Smooth true-color sprites", "Smooth true-color sprites",
	  "Bilinear filtering of true-color sprites when they are scaled. 'Off' "
	  "keeps them pixel-sharp, which suits CRTs and integer scales; palette-"
	  "indexed pixel art is never filtered either way. Applied when content "
	  "loads.",
	  NULL, "video",
	  { { "Content config", NULL }, { "On", NULL }, { "Off", NULL },
	    { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_difficulty",
	  "Gameplay > Difficulty", "Difficulty",
	  "AI difficulty, 1 (easiest) to 8 (hardest). Set here because a pack's "
	  "own options menu is not always usable. Applied when content loads.",
	  NULL, "gameplay",
	  { { "Content config", NULL }, { "1", NULL }, { "2", NULL }, { "3", NULL },
	    { "4", NULL }, { "5", NULL }, { "6", NULL }, { "7", NULL },
	    { "8", NULL }, { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_round_time",
	  "Gameplay > Round time", "Round time",
	  "Time limit per round, in seconds. Applied when content loads.",
	  NULL, "gameplay",
	  { { "Content config", NULL }, { "None", NULL }, { "60", NULL },
	    { "99", NULL }, { NULL, NULL } },
	  "Content config" },
	{ "ikemen_go_rounds_win",
	  "Gameplay > Rounds to win", "Rounds to win",
	  "Rounds needed to win a match. Applied when content loads.",
	  NULL, "gameplay",
	  { { "Content config", NULL }, { "1", NULL }, { "2", NULL }, { "3", NULL },
	    { NULL, NULL } },
	  "Content config" },
	{ NULL, NULL, NULL, NULL, NULL, NULL, { { NULL, NULL } }, NULL }
};

static struct retro_core_option_v2_category ik_option_cats[] = {
	{ "system",   "System",   "Engine data source, renderer and memory footprint." },
	{ "video",    "Video",    "Resolution, aspect and sprite rendering." },
	{ "gameplay", "Gameplay", "Match settings applied over the pack's defaults." },
	{ NULL, NULL, NULL }
};

static struct retro_core_options_v2 ik_options_v2 = {
	ik_option_cats, ik_option_defs
};

static const struct retro_variable ik_options[] = {
	{ "ikemen_go_engine_files",
	  "Engine files; System directory|Content folder" },
	{ "ikemen_go_resolution",
	  "Resolution; Content config|320x240 (4:3)|640x480 (4:3)|720x480 (3:2)|854x480 (16:9)|1280x720 (16:9)" },
	{ "ikemen_go_renderer",
	  "Renderer; Content config|OpenGL 3.3|Vulkan 1.3" },
	{ "ikemen_go_fight_aspect",
	  "Fight aspect; Content config|Stage|4:3|16:9" },
	{ "ikemen_go_sprite_detail",
	  "Sprite detail; Auto|Full|Half" },
	{ "ikemen_go_rgb_filter",
	  "Smooth true-color sprites; Content config|On|Off" },
	{ "ikemen_go_ram_saver",
	  "RAM saver; Off|On" },
	{ "ikemen_go_debug",
	  "Debug keys; Off|On" },
	{ "ikemen_go_difficulty",
	  "Difficulty; Content config|1|2|3|4|5|6|7|8" },
	{ "ikemen_go_round_time",
	  "Round time; Content config|None|60|99" },
	{ "ikemen_go_rounds_win",
	  "Rounds to win; Content config|1|2|3" },
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
