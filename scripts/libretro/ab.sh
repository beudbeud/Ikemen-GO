#!/bin/sh
# A/B two builds of the libretro core on the Pi.
#
#   ab.sh [options] A.so B.so
#     -s title|fight  scenario (default: fight)
#     -r <res>        ikemen_go_resolution value (default: "1920x1080 (16:9)")
#     -n <rounds>     measured runs per build (default: 3)
#     -V              vsync off: fps measures throughput instead of capping at 60
#     -p              also compare dumped frames pixel for pixel
#     -S              self-check: run A twice for the pixel comparison, to prove
#                     the scenario is deterministic before trusting an A/B diff
#     -H <host>       ssh host of the Pi (default: crt)
#     -o <dir>        results directory (default: /tmp/ikemen-ab/<timestamp>)
#
# Runs alternate A B B A A B ... so slow drift -- the Pi 5 heats up and
# throttles over a session -- lands on both builds equally. Every run starts
# from a fresh process: no warm cache from the previous build.
#
# EmulationStation must be stopped on the Pi first; it takes the display back.
set -eu

scenario=fight res="1920x1080 (16:9)" rounds=3 novsync= pixels= selfcheck=
host=crt outdir=
while getopts s:r:n:VpSH:o: opt; do
	case $opt in
	s) scenario=$OPTARG ;; r) res=$OPTARG ;; n) rounds=$OPTARG ;; V) novsync=-V ;;
	p) pixels=1 ;; S) selfcheck=1 pixels=1 ;; H) host=$OPTARG ;; o) outdir=$OPTARG ;;
	*) sed -n '2,20p' "$0" >&2; exit 2 ;;
	esac
done
shift $((OPTIND - 1))
[ $# -eq 2 ] || { sed -n '2,20p' "$0" >&2; exit 2; }
coreA=$1 coreB=$2
for c in "$coreA" "$coreB"; do [ -f "$c" ] || { echo "ab: no such core: $c" >&2; exit 2; }; done

# Frame numbers count presented frames since boot, and the engine is lockstep
# under libretro, so these windows land on the same content every run. Found
# for the Ultimate Cosmos pack; another pack needs its own numbers -- dump a few
# frames with -p and look at them.
case $scenario in
fight)
	# Sanctuaire: 39 background layers, 10 of them tiled, no 3D model -- the
	# draw-call-heavy 2D case. Frames 60/200 are the round intro, 600+ is play.
	args="-p1 SEIYA -p2 SHIRYU -s Sanctuaire(openGl) -p1.ai 8 -p2.ai 8"
	bench=600:2400 dump=60,200,600,1200,2400 timeout=400 ;;
title)
	args=
	bench=2100:3300 dump=2400 timeout=300 ;;
*) echo "ab: unknown scenario $scenario" >&2; exit 2 ;;
esac

here=$(cd "$(dirname "$0")" && pwd)
outdir=${outdir:-${TMPDIR:-/tmp}/ikemen-ab/$(date +%Y%m%d-%H%M%S)}
mkdir -p "$outdir"
remote=/tmp/ikab

ssh "$host" "pgrep emulationstatio >/dev/null" && {
	echo "ab: EmulationStation is running on $host; quit it first" >&2; exit 3
}
ssh "$host" "rm -rf $remote && mkdir -p $remote"
scp -q "$here/pi-run.sh" "$host:$remote/pi-run.sh"
scp -q "$coreA" "$host:$remote/A.so"
scp -q "$coreB" "$host:$remote/B.so"
ssh "$host" "chmod +x $remote/pi-run.sh"

{
	echo "A=$coreA $(md5sum <"$coreA" | cut -c1-12)"
	echo "B=$coreB $(md5sum <"$coreB" | cut -c1-12)"
	echo "scenario=$scenario resolution=$res rounds=$rounds vsync=${novsync:+off}"
} | tee "$outdir/setup.txt"

# run <label> <core> <runid> [extra pi-run options]
run() {
	label=$1 core=$2 id=$3
	shift 3
	echo "ab: $id ($label)"
	if ! ssh "$host" "$remote/pi-run.sh -c $remote/$core -o $remote/$id -r '$res' -a '$args' -t $timeout $novsync $*"; then
		echo "ab: run $id failed; its log follows" >&2
		ssh "$host" "tail -20 $remote/$id/log.txt" >&2
		exit 1
	fi
	mkdir -p "$outdir/$id"
	scp -q "$host:$remote/$id/summary.env" "$host:$remote/$id/log.txt" "$outdir/$id/"
}

i=1
while [ $i -le "$rounds" ]; do
	if [ $((i % 2)) -eq 1 ]; then order="A B"; else order="B A"; fi
	for l in $order; do run "$l" "$l.so" "bench-$l-$i" -b "$bench"; done
	i=$((i + 1))
done

# Mean and standard deviation of one summary key across a build's runs.
stat() {
	for f in "$outdir"/bench-"$1"-*/summary.env; do sed -n "s/^$2=//p" "$f"; done |
		awk '{ s += $1; q += $1 * $1; n++ } END {
			if (n == 0) { print "- - 0"; exit }
			m = s / n; v = q / n - m * m; if (v < 0) v = 0
			printf "%.3f %.3f %d\n", m, sqrt(v), n }'
}

if [ "$rounds" -gt 0 ]; then
echo
printf '%-14s %22s %22s %9s\n' metric A B "B vs A"
for key in fps step_ms_mean step_ms_p50 step_ms_p95 draws_mean; do
	set -- $(stat A "$key") $(stat B "$key")
	delta=$(awk -v a="$1" -v b="$4" 'BEGIN { if (a == 0) print "-"; else printf "%+.1f%%", 100 * (b - a) / a }')
	printf '%-14s %13s ± %-6s %13s ± %-6s %9s\n' "$key" "$1" "$2" "$4" "$5" "$delta"
done | tee "$outdir/table.txt"
echo "(n=$rounds runs per build; results in $outdir)"
grep -h '^throttled_end=' "$outdir"/bench-*/summary.env | grep -qv '=0x0' &&
	echo "ab: WARNING: the Pi reported throttling during a run -- the numbers are suspect"
fi

if [ -n "$pixels" ]; then
	second=B
	[ -n "$selfcheck" ] && second=A
	run A A.so dump-A -d "$dump"
	run "$second" "$second.so" "dump-$second-2" -d "$dump"
	scp -q "$host:$remote/dump-A/frame_*.ppm" "$outdir/dump-A/"
	scp -q "$host:$remote/dump-$second-2/frame_*.ppm" "$outdir/dump-$second-2/"
	echo
	for f in $(echo "$dump" | tr ',' ' '); do
		printf 'frame %-6s A vs %s: ' "$f" "$second"
		python3 "$here/ppmdiff.py" "$outdir/dump-A/frame_$f.ppm" "$outdir/dump-$second-2/frame_$f.ppm" || true
	done | tee "$outdir/pixels.txt"
fi
