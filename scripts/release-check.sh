#!/bin/sh
# Compare a new clifleet build with the installed one on the real fleet
# before tagging a release. Only read-only commands run: --help,
# status --json, and upgrade --dry-run --json. last-report.json is put
# back afterwards, so the previous real run is not lost.
#
#   scripts/release-check.sh NEW_BINARY [OLD_BINARY] [-- extra flags]
#
# extra flags go to status and upgrade, e.g. -- --hosts mbp --only codex.
# Exit 0 when both builds print the same, 1 when they differ (read the
# diff: an intended change is fine), 2 when a command could not run.
set -u

new=${1:?usage: scripts/release-check.sh NEW_BINARY [OLD_BINARY] [-- flags]}
shift
old=$(command -v clifleet || true)
if [ $# -gt 0 ] && [ "$1" != "--" ]; then
	old=$1
	shift
fi
[ "${1:-}" = "--" ] && shift
for b in "$new" "$old"; do
	if [ -z "$b" ] || [ ! -x "$b" ]; then
		echo "release-check: not an executable: '$b'" >&2
		exit 2
	fi
done

work=$(mktemp -d "${TMPDIR:-/tmp}/clifleet-release-check.XXXXXX") || exit 2
report="${XDG_CACHE_HOME:-$HOME/.cache}/clifleet/last-report.json"
saved=""
if [ -f "$report" ]; then
	saved="$work/last-report.json.saved"
	cp -p "$report" "$saved" || exit 2
fi
restore() {
	if [ -n "$saved" ]; then
		cp -p "$saved" "$report"
	fi
}
trap 'restore; exit 2' INT TERM
export CLIFLEET_NO_UPDATE_CHECK=1

echo "old: $old ($("$old" version))"
echo "new: $new ($("$new" version))"
echo "out: $work"

broken=0
# run LABEL BINARY ARGS... — exit 2 (a host failed) is a result, not breakage.
run() {
	label=$1
	bin=$2
	shift 2
	"$bin" "$@" >"$work/$label.out" 2>"$work/$label.err"
	code=$?
	echo "exit $code" >>"$work/$label.out"
	if [ "$code" -ne 0 ] && [ "$code" -ne 2 ]; then
		echo "release-check: $label exited $code: $(head -n 3 "$work/$label.err")" >&2
		broken=1
	fi
}

differ=0
for side in old new; do
	if [ "$side" = old ]; then bin=$old; else bin=$new; fi
	for cmd in "" status upgrade doctor history update; do
		label="$side.help${cmd:+.$cmd}"
		if [ -z "$cmd" ]; then
			"$bin" --help >"$work/$label.out" 2>&1
		else
			"$bin" "$cmd" --help >"$work/$label.out" 2>&1
		fi
	done
	run "$side.status" "$bin" status --json "$@"
	run "$side.dryrun" "$bin" upgrade --dry-run --json "$@"
done
restore
trap - INT TERM

for f in "$work"/old.*.out; do
	base=${f#"$work/old."}
	if ! diff -u "$f" "$work/new.$base" >"$work/diff.$base"; then
		echo
		echo "== ${base%.out} differs"
		cat "$work/diff.$base"
		differ=1
	fi
done

if [ "$broken" -ne 0 ]; then
	exit 2
fi
if [ "$differ" -ne 0 ]; then
	echo
	echo "release-check: output differs; review the diff above"
	exit 1
fi
echo "release-check: same output on both builds"
