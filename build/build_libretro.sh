#!/usr/bin/env bash
# Builds the libretro core. Same sources as the standalone build, plus the
# `libretro` tag which swaps the window/audio/input host (see src/libretro.go).
set -euo pipefail

cd "$(dirname "$0")/.."

export CGO_ENABLED=1
export GOEXPERIMENT=arenas # required by src/state.go, same as build.sh

case "$(go env GOOS)" in
	darwin)  ext=dylib ;;
	windows) ext=dll ;;
	*)       ext=so ;;
esac

out="bin/ikemen_go_libretro.$ext"
version="$(git describe --tags --always 2>/dev/null || echo development)"

# IKEMEN_GLES=1 builds the OpenGL ES core: the renderer talks to GL ES 3.0 and
# the engine gets its context straight from EGL, with no window and no SDL video
# driver. That is what a KMS/DRM frontend needs -- it holds the DRM master, so
# SDL has nothing left to open -- and it drops the desktop-GL dependency, which
# most embedded GPUs either lack or cap below 3.3. `egl` is the GL bindings'
# own tag: without it they link libGL for nothing.
tags="libretro"
if [ "${IKEMEN_GLES:-0}" != "0" ]; then
	tags="libretro gles egl"
fi

mkdir -p bin
go build -tags "$tags" -buildmode=c-shared -trimpath \
	-ldflags "-X 'main.Version=$version' -X 'main.BuildTime=$(date -u +%FT%TZ)'" \
	-o "$out" ./src

# c-shared drops a C header next to the library; the frontend has no use for it.
rm -f "${out%.*}.h"

cp build/ikemen_go_libretro.info bin/
echo "Built $out"
