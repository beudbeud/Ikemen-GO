#if __VERSION__ >= 450
	// VULKAN PATH
	#define COMPAT_TEXTURE texture
	layout(binding = 1) uniform UniformBufferObject  {
		vec4 x1x2x4x3;
		vec4 tint;
		vec3 add;
		vec3 mult;
		float alpha, gray, hue;
		int mask;
		bool isFlat, isRgba, isTrapez, neg;
	};
	layout(push_constant, std430) uniform u {
		vec4 palUV;
	};
	layout(binding = 2) uniform sampler2D tex;
	layout(binding = 3) uniform sampler2D pal;
	layout(location = 0) in vec2 texcoord;
	layout(location = 0) out vec4 FragColor;
#else
	// OPENGL / GLES PATH
	#define COMPAT_VARYING in
	#define COMPAT_TEXTURE texture
	#ifdef GL_ES
		precision highp float;
		precision highp int;
	#endif
	out vec4 FragColor;

	uniform sampler2D tex;
	uniform sampler2D pal;
	uniform vec4 x1x2x4x3;
	uniform vec4 tint;
	uniform vec3 add, mult;
	uniform float alpha, gray, hue;
	uniform int mask;
	uniform bool isFlat, isRgba, isTrapez, neg;
	COMPAT_VARYING vec2 texcoord;
#endif

vec3 rgb2hsv(vec3 c)
{
    vec4 K = vec4(0.0, -1.0 / 3.0, 2.0 / 3.0, -1.0);
    vec4 p = mix(vec4(c.bg, K.wz), vec4(c.gb, K.xy), step(c.b, c.g));
    vec4 q = mix(vec4(p.xyw, c.r), vec4(c.r, p.yzx), step(p.x, c.r));

    float d = q.x - min(q.w, q.y);
    float e = 1.0e-10;
    return vec3(abs(q.z + (q.w - q.y) / (6.0 * d + e)), d / (q.x + e), q.x);
}

vec3 hsv2rgb(vec3 c)
{
    vec4 K = vec4(1.0, 2.0 / 3.0, 1.0 / 3.0, 3.0);
    vec3 p = abs(fract(c.xxx + K.xyz) * 6.0 - K.www);
    return c.z * mix(K.xxx, clamp(p - K.xxx, 0.0, 1.0), c.y);
}

vec3 hue_shift(vec3 color, float dhue) {
	vec3 colorhsv = rgb2hsv(color);
	colorhsv.x = mod(colorhsv.x+dhue, 1.0);
	return hsv2rgb(colorhsv);
}

void main(void) {
	vec4 c;
	vec3 neg_base = vec3(1.0);
	vec3 final_add = add;
	vec4 final_mul = vec4(mult, alpha);

	// Select flat color or textures
#ifdef IK_NO_FLAT
	// RenderSprite never draws a flat colour -- only FillRect does, and it
	// asks for the unspecialised program. Keeping the branch here costs the
	// sprite path anyway: v3d keeps both sides live, and the extra registers
	// cut how many fragment threads it can keep in flight.
	{
#else
	if (isFlat) {
		c = tint; 

		// Treat flat colors like RGBA for math consistency
		neg_base *= c.a;
		final_add *= c.a;
		final_mul.rgb *= alpha;
	} else {
#endif
		vec2 uv = texcoord;
#ifndef IK_NO_TRAPEZ
		// Quads that are not trapezoids get a variant without this block. It
		// is a uniform branch, but v3d flattens it and every fragment then
		// pays its divide and gl_FragCoord read: 4ms a frame at 1920x1080 on
		// a Pi 5, on a screen that has no trapezoid in it at all.
		if (isTrapez) {
			vec2 bounds = mix(x1x2x4x3.zw, x1x2x4x3.xy, uv.y);
			float gap = bounds[1] - bounds[0];
			#ifdef GL_ES
				if (abs(gap) < 0.0001) gap = 0.0001;
			#endif
			uv.x = (gl_FragCoord.x - bounds[0]) / gap;
		}
#endif

		c = COMPAT_TEXTURE(tex, uv);

		// Select with or without palette.
		//
		// The caller always knows which of the two a draw needs, so it picks a
		// variant with the other one compiled out. Leaving the choice to a
		// uniform branch is what it looks like it should cost -- nothing --
		// but v3d flattens it, and then every fragment of an RGBA sprite also
		// runs the palette fetch it does not need. That alone was 5ms a frame
		// at 1920x1080 on a screen made of RGBA layers.
#if defined(IK_RGBA)
		if (mask == -1) c.a = 1.0;
		neg_base *= c.a;
		final_add *= c.a;
		final_mul.rgb *= alpha;
#elif defined(IK_INDEXED)
		// Palette lookup
		#if __VERSION__ >= 450
			c = COMPAT_TEXTURE(pal, vec2(palUV[0]+palUV[2]*c.r*0.9966, palUV[1]));
		#else
			c = COMPAT_TEXTURE(pal, vec2(c.r*0.9966, 0.5));
		#endif
		if (mask == -1) c.a = 1.0;
#else
		if (isRgba) {
			if (mask == -1) c.a = 1.0;
			neg_base *= c.a;
			final_add *= c.a;
			final_mul.rgb *= alpha;
		} else {
			// Palette lookup
			#if __VERSION__ >= 450
				c = COMPAT_TEXTURE(pal, vec2(palUV[0]+palUV[2]*c.r*0.9966, palUV[1]));
			#else
				c = COMPAT_TEXTURE(pal, vec2(c.r*0.9966, 0.5));
			#endif
			if (mask == -1) c.a = 1.0;
		}
#endif
	}

	// Apply PalFX
#ifdef IK_NO_PALFX
	// The renderer binds this variant for draws whose PalFX and tint are all
	// neutral, which on a screenpack is nearly every draw. The block below
	// would compute mix(x, y, 0), +0 and *1 on every fragment; skipping it at
	// compile time is worth 4ms a frame at 1280x720 on a Pi 5 (48 -> 60fps).
	// A runtime `if` on the uniforms does not work: v3d flattens small
	// branches into predication, so both sides run and the test is pure
	// overhead -- measured 45fps, slower than doing the math unconditionally.
	c *= final_mul;
#else
	// Hue
	if (hue != 0.0) {
		c.rgb = hue_shift(c.rgb, hue);
	}
	// Invertall
	if (neg) {
		c.rgb = neg_base - c.rgb;
	}
	// Color
	c.rgb = mix(vec3((c.r + c.g + c.b) / 3.0), c.rgb, 1.0 - gray);
	// Add
	c.rgb += final_add;
	// Mul
	c *= final_mul;

	// Apply tint
	// Sprites only, because flat colors are already tinted
#ifdef IK_NO_FLAT
	c.rgb = mix(c.rgb, tint.rgb * c.a, tint.a);
#else
	if (!isFlat) {
		c.rgb = mix(c.rgb, tint.rgb * c.a, tint.a);
	}
#endif
#endif

	FragColor = c;
}
