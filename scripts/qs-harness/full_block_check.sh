#!/usr/bin/env bash
# full_block_check.sh -- shared helper for detecting a FULL block from an
# eth_getBlockByNumber JSON response, factored out of the qs runner scripts
# (run-r35zzzf.sh and earlier) so it can be unit-tested offline instead of
# only ever being exercised live against the fleet.
#
# S25 (docs/QS_BLOCK_TIME_BUDGET.md 6cx): the in-window capture has now
# failed in two rounds for two different reasons -- 35zzzf's own captures
# landed in the baseFee-decay ramp (wrong trigger entirely, fixed in S22/6ct
# by keying off the first full block instead of a fixed sleep), and
# 35zzzg's fix had a parsing bug that made the SAME first-full-block
# detector spin forever: the second `grep -o '0x[0-9a-f]*$'` anchored to
# end-of-line, but the text `grep -o` hands it is `"gasUsed":"0x1234"`
# (the FIRST grep's own match, quotes and all) -- the hex digits are never
# the last characters of that string (a closing `"` always follows), so the
# anchored pattern never matches, gu/gl stay empty forever, and the poll
# loop just spins until the leg's own deadline. Dropped the anchor here.
#
# Usage: is_full_block <json-blob> <threshold-percent>
#   Returns 0 (true / "this is a full block") when gasUsed/gasLimit*100 is
#     at or above threshold-percent.
#   Returns 1 (false / "not full, but parsed cleanly") when both fields
#     parsed and the ratio is below threshold-percent.
#   Returns 2 (error / "could not parse") when gasUsed or gasLimit is
#     missing (an RPC error response, an empty body, a malformed blob, or
#     gasLimit is zero) -- the caller's poll loop must treat this as "try
#     again", not as "not full", or a single bad response would be
#     indistinguishable from a genuinely empty block.
#
# Threshold: this campaign's shape sets the builder's fill cap
# (N42_MINER_FILL_GAS) to HALF the header gas ceiling (gasceil), so a truly
# full block's own gasUsed tops out around 50% of gasLimit, not 100% --
# the historical `>= 95` threshold (copied from measure-tps.sh's own
# occupancy definition, which uses it for a DIFFERENT purpose against a
# DIFFERENT ratio convention) never matched a single block in this shape,
# which is 35zzzf's own capture failure (6ct/6cv): the poll loop spun for
# the whole leg waiting for a threshold this harness's own gas-ceiling
# doubling made unreachable. 45 is comfortably below the ~48-50% a full
# block actually reaches and comfortably above an empty or lightly-loaded
# block (typically single-digit percent or exactly 0).
is_full_block() {
	local json="$1" threshold="$2" gu gl
	gu=$(printf '%s' "$json" | grep -o '"gasUsed":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*')
	gl=$(printf '%s' "$json" | grep -o '"gasLimit":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*')
	if [ -z "$gu" ] || [ -z "$gl" ]; then
		return 2
	fi
	if [ $((gl)) -le 0 ]; then
		return 2
	fi
	if [ $((100 * (gu) / (gl))) -ge "$threshold" ]; then
		return 0
	fi
	return 1
}
