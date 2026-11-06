#!/bin/sh
# server-observe.sh runs `replay observe` on a server's magpie, for
# development: it reads magpie's records and changes nothing.
#
#   sh server-observe.sh [--dir <magpie config folder>]
#
# Run it from the folder it was unpacked in, beside replay-linux-amd64 and
# replay-linux-arm64. It finds the config folder of the magpie running
# here (its process's XDG_CONFIG_HOME or HOME, or a portable data/ beside
# its executable), else ~/.config/magpie, unless --dir names one. It
# writes report.txt and report.json, accounts named by a hash salted for
# the run, into replay-report-<host>-<time>.tar.gz here, which is what to
# bring back. Reading another user's magpie needs that user, or root.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
dir=""
while [ $# -gt 0 ]; do
	case "$1" in
	--dir) dir="$2"; shift 2 ;;
	--dir=*) dir="${1#--dir=}"; shift ;;
	-h|--help) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "unknown argument: $1 (try --help)" >&2; exit 2 ;;
	esac
done

case "$(uname -m)" in
x86_64|amd64) bin="$here/replay-linux-amd64" ;;
aarch64|arm64) bin="$here/replay-linux-arm64" ;;
*) echo "no replay build for $(uname -m)" >&2; exit 1 ;;
esac
[ -x "$bin" ] || chmod +x "$bin"

# has says whether a folder holds any of magpie's records.
has() {
	[ -f "$1/quota-history.json" ] || [ -f "$1/usage.jsonl" ] || [ -d "$1/routing" ]
}

# envOf prints variable $2 of process $1, empty when unset or unreadable.
envOf() {
	tr '\0' '\n' <"/proc/$1/environ" 2>/dev/null | sed -n "s/^$2=//p" | head -n 1
}

found=""
if [ -z "$dir" ]; then
	for pid in $(pgrep -x magpie 2>/dev/null; pgrep -f '[/ ]magpie( |$)' 2>/dev/null); do
		[ -r "/proc/$pid/environ" ] || { echo "note: can't read magpie (pid $pid)'s environment; run as its user or root" >&2; continue; }
		exe=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
		xdg=$(envOf "$pid" XDG_CONFIG_HOME)
		home=$(envOf "$pid" HOME)
		for c in \
			"${exe:+$(dirname "$exe")/data}" \
			"${xdg:+$xdg/magpie}" \
			"${home:+$home/.config/magpie}"; do
			if [ -n "$c" ] && has "$c"; then
				dir=$c
				found="magpie pid $pid"
				break 2
			fi
		done
	done
fi
if [ -z "$dir" ]; then
	dir="${XDG_CONFIG_HOME:-$HOME/.config}/magpie"
	found="default"
fi
if ! has "$dir"; then
	echo "no magpie records in $dir ($found)." >&2
	if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | grep -qi magpie; then
		echo "magpie runs in Docker here: find its config mount with" >&2
		echo "  docker inspect -f '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{\"\\n\"}}{{end}}' <container>" >&2
		echo "and pass the host side of it with --dir." >&2
	fi
	echo "name the folder with --dir." >&2
	exit 1
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
host=$(hostname -s 2>/dev/null || hostname)
out="$here/replay-report-$host-$stamp"
mkdir -p "$out"
{
	echo "host:   $host"
	echo "when:   $stamp (UTC)"
	echo "folder: $dir ($found)"
	echo "sizes:"
	for f in quota-history.json usage.jsonl routing; do
		[ -e "$dir/$f" ] && du -sh "$dir/$f" | sed 's/^/  /'
	done
	echo
} >"$out/report.txt"
"$bin" observe -dir "$dir" -json-out "$out/report.json" >>"$out/report.txt"
# the folder's path names the user magpie runs as; keep it out of the JSON
sed -i "s#\"dir\": \".*\"#\"dir\": \"(magpie's config folder)\"#" "$out/report.json"
tar -czf "$out.tar.gz" -C "$here" "$(basename "$out")"
rm -rf "$out"
echo "wrote $out.tar.gz: bring this file back."
