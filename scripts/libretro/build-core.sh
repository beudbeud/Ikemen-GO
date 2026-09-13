#!/bin/sh
# Build the libretro core for the Pi from this working tree and copy it out.
#
#   build-core.sh <out.so>
#
# Uses a Recalbox buildroot tree whose local.mk overrides the package source
# with this checkout (LIBRETRO_IKEMENGO_OVERRIDE_SRCDIR). The override build
# runs with -mod=vendor, so vendor/ must exist and carry the reisen FFmpeg 6
# patch -- see package/libretro-ikemengo in the Recalbox tree.
#
#   RECALBOX_DIR   the buildroot tree (default: ~/Images/Darktable/recalbox)
#   ARCH           buildroot target   (default: rpi5_64)
set -eu

out=${1:?usage: build-core.sh <out.so>}
src=$(cd "$(dirname "$0")/../.." && pwd)
rb=${RECALBOX_DIR:-$HOME/Images/Darktable/recalbox}
arch=${ARCH:-rpi5_64}

grep -q "LIBRETRO_IKEMENGO_OVERRIDE_SRCDIR *= *$src" "$rb/local.mk" || {
	echo "build-core: $rb/local.mk does not override the core with $src" >&2
	exit 1
}
[ -d "$src/vendor" ] || { echo "build-core: no vendor/ in $src (go mod vendor)" >&2; exit 1; }

mkdir -p "$rb/output/build/.npm" "$rb/dl" "$rb/host"
log=$(mktemp)
echo "build-core: building $src (log: $log)"
# recaldocker.sh wants a TTY (-ti); a scripted build cannot give it one.
if ! docker run --rm --security-opt seccomp=unconfined \
	-w="$rb" -v "$rb:$rb" -v "$src:$src" \
	-v "$rb/output/build/.npm:/.npm" -v "$rb/dl:/share/dl" -v "$rb/host:/share/host" \
	-e ARCH="$arch" -e RECALBOX_VERSION=development -e FORCE_UNSAFE_CONFIGURE=1 \
	--user="$(id -u):$(id -g)" recalbox-dev make libretro-ikemengo-rebuild >"$log" 2>&1; then
	tail -20 "$log" >&2
	exit 1
fi

so="$rb/output/per-package/libretro-ikemengo/target/usr/lib/libretro/ikemen_go_libretro.so"
mkdir -p "$(dirname "$out")"
cp "$so" "$out"
echo "build-core: $out ($(git -C "$src" rev-parse --short HEAD)$(git -C "$src" diff --quiet || echo +dirty))"
