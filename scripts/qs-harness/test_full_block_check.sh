#!/usr/bin/env bash
# test_full_block_check.sh -- offline test for full_block_check.sh's
# is_full_block, run with canned JSON, no fleet required.
#
#   bash scripts/qs-harness/test_full_block_check.sh
#
# S25 (docs/QS_BLOCK_TIME_BUDGET.md 6cx): this campaign's in-window capture
# has now failed live twice; this proves the detection function offline
# before it is ever wired back into a runner script. gasLimit is fixed at
# 0x3b9aca00 (1,000,000,000) across cases so the expected ratios are exact
# and easy to check by eye; the threshold under test throughout is 45
# (this round's actual value).
set -u
cd "$(dirname "${BASH_SOURCE[0]}")"
# shellcheck source=./full_block_check.sh
source ./full_block_check.sh

pass=0
fail=0

check() {
	local name="$1" json="$2" threshold="$3" want_status="$4"
	local got_status
	is_full_block "$json" "$threshold"
	got_status=$?
	if [ "$got_status" = "$want_status" ]; then
		echo "PASS: $name (status=$got_status)"
		pass=$((pass + 1))
	else
		echo "FAIL: $name (status=$got_status, want $want_status)"
		fail=$((fail + 1))
	fi
}

GL='"gasLimit":"0x3b9aca00"' # 1,000,000,000

# 1. Empty block: gasUsed=0 -> 0%, not full (status 1).
check "empty block (0%)" \
	'{"number":"0x1","gasUsed":"0x0",'"$GL"'}' \
	45 1

# 2. 48.7%-full block -> full at threshold 45 (status 0).
check "48.7% full block" \
	'{"number":"0x2","gasUsed":"0x1d0707c0",'"$GL"'}' \
	45 0

# 3. 22%-full block -> not full at threshold 45 (status 1).
check "22% full block" \
	'{"number":"0x3","gasUsed":"0xd1cef00",'"$GL"'}' \
	45 1

# 4. Error/empty response -> status 2 (must retry, not "not full").
check "RPC error response" \
	'{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"header not found"}}' \
	45 2
check "completely empty body" \
	'' \
	45 2

# 5. Field order independence: gasUsed before gasLimit (the production
#    shape) and gasLimit before gasUsed (defensive -- eth_getBlockByNumber
#    does not guarantee key order, and the old anchored-`$` bug would have
#    been order-sensitive in a way this must not be).
check "gasUsed before gasLimit" \
	'{"number":"0x4","gasUsed":"0x1d0707c0","gasLimit":"0x3b9aca00"}' \
	45 0
check "gasLimit before gasUsed" \
	'{"number":"0x5","gasLimit":"0x3b9aca00","gasUsed":"0x1d0707c0"}' \
	45 0

# 6. Boundary: exactly at threshold (45%) must be full (>=, not >).
check "exactly 45% (boundary, inclusive)" \
	'{"number":"0x6","gasUsed":"0x1ad27480",'"$GL"'}' \
	45 0

# 7. Just under threshold (44%) must not be full.
check "44% (just under threshold)" \
	'{"number":"0x7","gasUsed":"0x1a39de00",'"$GL"'}' \
	45 1

# 8. Regression check for the ORIGINAL bug: with the anchored pattern,
#    NEITHER of these would ever have parsed (gu/gl both empty -> status
#    2 forever, the exact live failure mode in round 35zzzg). Confirm the
#    fixed function reads a real, non-boundary full block correctly one
#    more time using the literal shape eth_getBlockByNumber actually
#    returns (extra trailing fields after gasLimit, as a real block has).
check "realistic trailing-fields response" \
	'{"number":"0x8","hash":"0xabc","parentHash":"0xdef","gasUsed":"0x1d0707c0","gasLimit":"0x3b9aca00","timestamp":"0x68d0"}' \
	45 0

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
