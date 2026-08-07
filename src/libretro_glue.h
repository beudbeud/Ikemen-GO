/* Minimal subset of libretro.h, only what the Ikemen GO core actually uses.
 * cgo forbids definitions in the preamble of a file using //export, so the
 * function bodies live in libretro_glue.c. */
#ifndef IKEMEN_LIBRETRO_GLUE_H
#define IKEMEN_LIBRETRO_GLUE_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include <stdlib.h>

#define IK_API __attribute__((visibility("default")))

#define RETRO_API_VERSION 1

#define RETRO_ENVIRONMENT_SET_MESSAGE            6
#define RETRO_ENVIRONMENT_SHUTDOWN               7
#define RETRO_ENVIRONMENT_GET_SYSTEM_DIRECTORY   9
#define RETRO_ENVIRONMENT_SET_PIXEL_FORMAT       10
#define RETRO_ENVIRONMENT_SET_INPUT_DESCRIPTORS  11
#define RETRO_ENVIRONMENT_GET_VARIABLE           15
#define RETRO_ENVIRONMENT_SET_VARIABLES          16
#define RETRO_ENVIRONMENT_GET_VARIABLE_UPDATE    17
#define RETRO_ENVIRONMENT_GET_RUMBLE_INTERFACE   23
#define RETRO_ENVIRONMENT_GET_SAVE_DIRECTORY     31
#define RETRO_ENVIRONMENT_SET_GEOMETRY           37
#define RETRO_ENVIRONMENT_GET_CORE_OPTIONS_VERSION 52
#define RETRO_ENVIRONMENT_SET_CORE_OPTIONS_V2      67

#define RETRO_PIXEL_FORMAT_XRGB8888 1

#define RETRO_DEVICE_JOYPAD   1
#define RETRO_DEVICE_KEYBOARD 3
#define RETRO_DEVICE_ANALOG   5

#define RETRO_DEVICE_ID_JOYPAD_B      0
#define RETRO_DEVICE_ID_JOYPAD_Y      1
#define RETRO_DEVICE_ID_JOYPAD_SELECT 2
#define RETRO_DEVICE_ID_JOYPAD_START  3
#define RETRO_DEVICE_ID_JOYPAD_UP     4
#define RETRO_DEVICE_ID_JOYPAD_DOWN   5
#define RETRO_DEVICE_ID_JOYPAD_LEFT   6
#define RETRO_DEVICE_ID_JOYPAD_RIGHT  7
#define RETRO_DEVICE_ID_JOYPAD_A      8
#define RETRO_DEVICE_ID_JOYPAD_X      9
#define RETRO_DEVICE_ID_JOYPAD_L      10
#define RETRO_DEVICE_ID_JOYPAD_R      11
#define RETRO_DEVICE_ID_JOYPAD_L2     12
#define RETRO_DEVICE_ID_JOYPAD_R2     13
#define RETRO_DEVICE_ID_JOYPAD_L3     14
#define RETRO_DEVICE_ID_JOYPAD_R3     15

#define RETRO_DEVICE_INDEX_ANALOG_LEFT  0
#define RETRO_DEVICE_INDEX_ANALOG_RIGHT 1
#define RETRO_DEVICE_ID_ANALOG_X        0
#define RETRO_DEVICE_ID_ANALOG_Y        1

#define RETRO_REGION_NTSC 0

enum retro_rumble_effect {
	RETRO_RUMBLE_STRONG = 0,
	RETRO_RUMBLE_WEAK   = 1,
};

typedef bool (*retro_set_rumble_state_t)(unsigned port, enum retro_rumble_effect effect, uint16_t strength);

struct retro_rumble_interface {
	retro_set_rumble_state_t set_rumble_state;
};

struct retro_message {
	const char *msg;
	unsigned    frames;
};

struct retro_variable {
	const char *key;
	const char *value;
};

/* Core options v2 (RETRO_ENVIRONMENT_SET_CORE_OPTIONS_V2). The layout matches
 * libretro.h; values arrays are NULL-terminated and sized to the API's max. */
struct retro_core_option_value {
	const char *value;
	const char *label;
};

struct retro_core_option_v2_category {
	const char *key;
	const char *desc;
	const char *info;
};

struct retro_core_option_v2_definition {
	const char *key;
	const char *desc;
	const char *desc_categorized;
	const char *info;
	const char *info_categorized;
	const char *category_key;
	struct retro_core_option_value values[128];
	const char *default_value;
};

struct retro_core_options_v2 {
	struct retro_core_option_v2_category   *categories;
	struct retro_core_option_v2_definition *definitions;
};

struct retro_system_info {
	const char *library_name;
	const char *library_version;
	const char *valid_extensions;
	bool        need_fullpath;
	bool        block_extract;
};

struct retro_game_geometry {
	unsigned base_width;
	unsigned base_height;
	unsigned max_width;
	unsigned max_height;
	float    aspect_ratio;
};

struct retro_system_timing {
	double fps;
	double sample_rate;
};

struct retro_system_av_info {
	struct retro_game_geometry geometry;
	struct retro_system_timing timing;
};

struct retro_game_info {
	const char *path;
	const void *data;
	size_t      size;
	const char *meta;
};

struct retro_input_descriptor {
	unsigned    port;
	unsigned    device;
	unsigned    index;
	unsigned    id;
	const char *description;
};

typedef bool    (*retro_environment_t)(unsigned cmd, void *data);
typedef void    (*retro_video_refresh_t)(const void *data, unsigned width, unsigned height, size_t pitch);
typedef void    (*retro_audio_sample_t)(int16_t left, int16_t right);
typedef size_t  (*retro_audio_sample_batch_t)(const int16_t *data, size_t frames);
typedef void    (*retro_input_poll_t)(void);
typedef int16_t (*retro_input_state_t)(unsigned port, unsigned device, unsigned index, unsigned id);

/* Thin wrappers so Go can reach the frontend callbacks (Go cannot call C
 * function pointers directly). Defined in libretro_glue.c. */
bool    ik_env(unsigned cmd, void *data);
void    ik_video(const void *data, unsigned width, unsigned height, size_t pitch);
size_t  ik_audio_batch(const int16_t *data, size_t frames);
void    ik_input_poll(void);
int16_t ik_input_state(unsigned port, unsigned device, unsigned index, unsigned id);
void    ik_set_input_descriptors(void);
/* Current value of a core option, or NULL if the frontend has none. */
const char *ik_get_variable(const char *key);
/* Rumble: query the frontend for the interface (retro_load_game context),
 * then drive both motors. SDL naming: lo = big/low-frequency motor
 * (RETRO_RUMBLE_STRONG), hi = small/high-frequency one (RETRO_RUMBLE_WEAK). */
bool ik_init_rumble(void);
void ik_set_rumble(unsigned port, uint16_t lo, uint16_t hi);

#endif
