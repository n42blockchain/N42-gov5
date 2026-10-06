#!/usr/bin/env bash
# dry_run_capture.sh -- end-to-end dry run of the win1/win2 capture
# sequence (S25, docs/QS_BLOCK_TIME_BUDGET.md 6cx), against a stubbed
# `curl` shell function that serves a rising block series, with NO live
# fleet. Proves the sequence "poll for the first full block -> wait to
# win1_start+15s -> capture -> wait to win2_start(+60s)+15s -> capture"
# actually fires at the right times and writes the right files, using the
# SAME is_full_block detection (full_block_check.sh) the production runner
# script inlines. Timings are scaled down via env vars (default: real
# production values) so this completes in seconds, not minutes; the
# runner script itself always uses the real values (POLL_INTERVAL=3,
# WIN1_OFFSET=15, WIN_SEC=60, WIN2_OFFSET=75 -- i.e. WIN1_OFFSET+WIN_SEC).
#
#   bash scripts/qs-harness/dry_run_capture.sh
#
set -u
cd "$(dirname "${BASH_SOURCE[0]}")"
# shellcheck source=./full_block_check.sh
source ./full_block_check.sh

# Scaled-down timings for a fast dry run (override with real values to
# check the production timing arithmetic instead: WIN1_OFFSET=15
# WIN_SEC=60 WIN2_OFFSET=75 POLL_INTERVAL=3).
POLL_INTERVAL=${POLL_INTERVAL:-1}
WIN1_OFFSET=${WIN1_OFFSET:-2}
WIN_SEC=${WIN_SEC:-4}
WIN2_OFFSET=$((WIN1_OFFSET + WIN_SEC))
THRESHOLD=${THRESHOLD:-45}
EMPTY_POLLS=${EMPTY_POLLS:-2} # how many polls return an empty block before the flood "takes hold"

OUT_DIR=$(mktemp -d)
echo "writing to $OUT_DIR"

# --- stub curl: a shell function shadows the real `curl` command for the
# rest of this script. Handles exactly the two request shapes the
# production capture code makes: JSON-RPC POSTs (eth_blockNumber /
# eth_getBlockByNumber) and pprof-style GETs with -o <file>. ---
# poll_count lives in a file, not a plain variable: every caller of this
# function does `x=$(curl ...)`, and a command substitution runs the
# function in a SUBSHELL -- a plain variable's increment would be lost the
# instant that subshell exits, silently freezing the counter at 0 forever.
poll_count_file=$(mktemp)
echo 0 >"$poll_count_file"
trap 'rm -rf "$OUT_DIR" "$poll_count_file"' EXIT
curl() {
	local out="" url="" is_post=0
	while [ $# -gt 0 ]; do
		case "$1" in
		-o)
			out="$2"
			shift 2
			;;
		-X | -H | -m | --data)
			shift 2
			;;
		-s)
			shift
			;;
		http*)
			url="$1"
			shift
			;;
		*)
			shift
			;;
		esac
	done
	if [ -n "$out" ]; then
		# A pprof-style capture GET: simulate a successful download.
		printf 'stub capture payload for %s at t=%s\n' "$url" "$SECONDS" >"$out"
		return 0
	fi
	# A JSON-RPC POST. We cannot see --data's CONTENTS with the simple
	# parser above (it is discarded like every other two-word flag), but
	# the production code only ever POSTs to port 20012 with one of two
	# methods, and always reads the reply the same way regardless -- for
	# eth_blockNumber it wants {"result":"0xN"}, for eth_getBlockByNumber
	# it wants a block body. We distinguish by call order: the FIRST call
	# each poll iteration is eth_blockNumber, the SECOND is
	# eth_getBlockByNumber (matching full_block_check's own two-curl
	# pattern) -- tracked via poll_count_file (see above) since each call
	# is its own subshell.
	local poll_count
	poll_count=$(($(cat "$poll_count_file") + 1))
	echo "$poll_count" >"$poll_count_file"
	if [ $((poll_count % 2)) -eq 1 ]; then
		printf '{"jsonrpc":"2.0","id":1,"result":"0x%x"}\n' $(((poll_count + 1) / 2))
		return 0
	fi
	local n=$((poll_count / 2))
	if [ "$n" -le "$EMPTY_POLLS" ]; then
		echo '{"number":"0x1","gasUsed":"0x0","gasLimit":"0x3b9aca00"}'
	else
		echo '{"number":"0x2","gasUsed":"0x1d0707c0","gasLimit":"0x3b9aca00"}'
	fi
}

# --- the sequence under test, extracted from run-r35zzzf.sh's own
# capture block (S22/6ct) with the S25 fix (unanchored grep, via
# is_full_block) and the timings parameterized as above. ---
win1_start=0
deadline=$((SECONDS + 30))
while [ "$win1_start" = 0 ] && [ $SECONDS -lt $deadline ]; do
	sleep "$POLL_INTERVAL"
	hexnum=$(curl -s -X POST -H 'content-type: application/json' --data '{}' http://127.0.0.1:20012 | grep -o '"result":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*')
	[ -z "$hexnum" ] && continue
	blk=$(curl -s -X POST -H 'content-type: application/json' --data '{}' http://127.0.0.1:20012)
	if is_full_block "$blk" "$THRESHOLD"; then
		win1_start=$SECONDS
		echo "t=$SECONDS: first full block detected (poll returned a %$THRESHOLD+ block); win1 start"
	fi
done
if [ "$win1_start" = 0 ]; then
	echo "FAIL: no full block detected within the poll deadline"
	exit 1
fi

capture_win() {
	local win=$1 offset=$2
	local target=$((win1_start + offset)) now=$SECONDS remain
	remain=$((target - now))
	[ $remain -gt 0 ] && sleep $remain
	echo "t=$SECONDS: capturing $win (target was win1_start+${offset}s = $target)"
	curl -s -m 40 "http://127.0.0.1:6091/debug/pprof/profile?seconds=20" -o "$OUT_DIR/$win-node1-cpu.pb.gz"
	curl -s -m 40 "http://127.0.0.1:6092/debug/pprof/heap" -o "$OUT_DIR/$win-node2-heap.pb.gz"
}

capture_win win1 "$WIN1_OFFSET"
t_win1_capture=$SECONDS
capture_win win2 "$WIN2_OFFSET"
t_win2_capture=$SECONDS

echo
echo "=== checks ==="
ok=1

files_expected="win1-node1-cpu.pb.gz win1-node2-heap.pb.gz win2-node1-cpu.pb.gz win2-node2-heap.pb.gz"
for f in $files_expected; do
	if [ -s "$OUT_DIR/$f" ]; then
		echo "PASS: $f written and non-empty"
	else
		echo "FAIL: $f missing or empty"
		ok=0
	fi
done

gap=$((t_win2_capture - t_win1_capture))
want_gap=$WIN_SEC
if [ "$gap" -ge $((want_gap - 1)) ] && [ "$gap" -le $((want_gap + 3)) ]; then
	echo "PASS: win2 capture landed ${gap}s after win1 capture (want ~${want_gap}s)"
else
	echo "FAIL: win2 capture landed ${gap}s after win1 capture (want ~${want_gap}s)"
	ok=0
fi

if [ "$ok" = 1 ]; then
	echo
	echo "dry run OK"
	exit 0
fi
echo
echo "dry run FAILED"
exit 1
