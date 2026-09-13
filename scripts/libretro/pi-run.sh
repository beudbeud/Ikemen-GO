#!/bin/sh
# One measured run of the libretro core on the target device (a Recalbox Pi).
# Driven by ab.sh; runs on the device, busybox sh compatible.
#
#   pi-run.sh -c <core.so> -o <outdir> [options]
#     -g <dir>     game (content) folder
#     -r <res>     value for the ikemen_go_resolution core option
#     -a <args>    IKEMEN_ARGS (e.g. a quick versus)
#     -b <a:b>     IKEMEN_BENCH window: aggregate frames [a,b), then quit
#     -d <n,n>     IKEMEN_DUMP_FRAMES: dump these frames to <outdir>, then quit
#     -t <secs>    give up after this long (default 300)
#     -s <n>       IKEMEN_SEED (default 1): same AI fight on every run; 0 = unseeded
#     -V           turn vsync off, so fps measures throughput instead of 60
#     -P           with -b: CPU profile of the bench window -> <outdir>/window.pprof
#     -F           with -b: log the textures that fill the most screen (IKEMEN_BENCH_FILL)
#
# The core asks the frontend to shut down at the end of the bench window or
# after the last dump, but RetroArch may only close the content and sit in its
# menu. So the run ends when the core's closing line is in the log: the bench
# summary, or the last dump, is written by then. The timeout is only a safety
# net for a core that hangs or never reaches the frame asked for.
#
# Writes <outdir>/log.txt and <outdir>/summary.env (key=value).
set -u

core= out= game="/recalbox/share/externals/usb0/recalbox/roms/mugen/Ultimate Cosmos"
res="1920x1080 (16:9)" args= bench= dump= timeout=300 novsync= seed=1 wpprof= fill=
while getopts c:o:g:r:a:b:d:t:s:VPF opt; do
	case $opt in
	c) core=$OPTARG ;; o) out=$OPTARG ;; g) game=$OPTARG ;; r) res=$OPTARG ;;
	a) args=$OPTARG ;; b) bench=$OPTARG ;; d) dump=$OPTARG ;; t) timeout=$OPTARG ;;
	s) seed=$OPTARG ;;
	V) novsync=1 ;; P) wpprof=1 ;; F) fill=1 ;;
	*) echo "usage: see header" >&2; exit 2 ;;
	esac
done
[ -n "$core" ] && [ -n "$out" ] || { echo "pi-run: -c and -o are required" >&2; exit 2; }
[ -f "$core" ] || { echo "pi-run: no core at $core" >&2; exit 2; }
[ "$seed" = 0 ] && seed=

# One run at a time: two RetroArch instances fight over the display, and the
# loser dies two seconds in with nothing logged.
lock=/tmp/ikbench.lock
mkdir "$lock" 2>/dev/null || { echo "pi-run: another run holds $lock" >&2; exit 3; }
trap 'rmdir "$lock" 2>/dev/null' EXIT HUP INT TERM

# EmulationStation restarts itself and takes the display back mid-run.
if pgrep emulationstatio >/dev/null 2>&1; then
	echo "pi-run: EmulationStation is running; quit it first" >&2; exit 3
fi
if pgrep retroarch >/dev/null 2>&1; then
	echo "pi-run: a RetroArch instance is already running" >&2; exit 3
fi

mkdir -p "$out"
rm -f "$out"/log.txt "$out"/summary.env "$out"/frame_*.ppm

cfgdir=/recalbox/share/system/configs/retroarch
cp "$cfgdir/cores/retroarch-core-options.cfg" "$out/opts.cfg"
sed -i "s|^ikemen_go_resolution = .*|ikemen_go_resolution = \"$res\"|" "$out/opts.cfg"
{
	echo "core_options_path = \"$out/opts.cfg\""
	# Recalbox rewrites the shared config's system_directory for every game it
	# launches (bios/stv after a Saturn game), and the core looks for its engine
	# files there.
	echo 'system_directory = "/recalbox/share/bios"'
	echo 'quit_on_close_content = "1"'
	if [ -n "$novsync" ]; then
		echo 'video_vsync = "false"'
		echo 'video_frame_delay_auto = "false"'
		echo 'audio_sync = "false"'
	fi
} >"$out/ra.cfg"

thermal() {
	t=$(vcgencmd measure_temp 2>/dev/null | sed 's/[^0-9.]//g')
	th=$(vcgencmd get_throttled 2>/dev/null | sed 's/.*=//')
	echo "${t:-?} ${th:-?}"
}
set -- $(thermal); temp0=$1 thr0=$2

start=$(date +%s)
IKEMEN_SEED="$seed" IKEMEN_ARGS="$args" IKEMEN_BENCH="$bench" \
	IKEMEN_BENCH_PPROF="${wpprof:+$out/window.pprof}" IKEMEN_BENCH_FILL="$fill" \
	IKEMEN_DUMP="${dump:+$out}" IKEMEN_DUMP_FRAMES="$dump" \
	retroarch --config "$cfgdir/retroarchcustom.cfg" --appendconfig "$out/ra.cfg" \
	-L "$core" "$game" >"$out/log.txt" 2>&1 &
pid=$!

# The line that means the core has written everything this run is for.
if [ -n "$bench" ]; then
	done_re='Ikemen GO: bench frames='
elif [ -n "$dump" ]; then
	last=$(echo "$dump" | tr ',' '\n' | sort -n | tail -1)
	done_re="Ikemen GO: dump: frame $last "
else
	done_re=
fi

stop() {
	kill -TERM "$pid" 2>/dev/null
	i=0
	while kill -0 "$pid" 2>/dev/null && [ $i -lt 20 ]; do sleep 1; i=$((i + 1)); done
	kill -KILL "$pid" 2>/dev/null
}

status=ok
while kill -0 "$pid" 2>/dev/null; do
	if [ -n "$done_re" ] && grep -q "$done_re" "$out/log.txt" 2>/dev/null; then
		# Leave the core a moment to go on its own, then close RetroArch.
		i=0
		while kill -0 "$pid" 2>/dev/null && [ $i -lt 5 ]; do sleep 1; i=$((i + 1)); done
		stop
		break
	fi
	if [ $(($(date +%s) - start)) -ge "$timeout" ]; then
		status=timeout
		stop
		break
	fi
	sleep 1
done
wait "$pid" 2>/dev/null
elapsed=$(($(date +%s) - start))
set -- $(thermal); temp1=$1 thr1=$2

{
	echo "status=$status"
	echo "elapsed_s=$elapsed"
	echo "core=$core"
	echo "resolution=$res"
	echo "seed=$seed"
	echo "vsync=${novsync:+off}"
	echo "temp_start=$temp0"
	echo "temp_end=$temp1"
	echo "throttled_start=$thr0"
	echo "throttled_end=$thr1"
	# "Ikemen GO: bench frames=1800 fps=54.93 ..." -> one key=value per line
	grep -m1 'Ikemen GO: bench ' "$out/log.txt" | sed 's/^.*bench //' | tr ' ' '\n'
} >"$out/summary.env"

if [ -n "$bench" ] && ! grep -q '^frames=' "$out/summary.env"; then
	echo "pi-run: no bench line (status=$status); see $out/log.txt" >&2
	exit 1
fi
exit 0
