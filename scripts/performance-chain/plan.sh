#!/usr/bin/env bash
# Print the isolated performance-chain plan. This never launches or stops nodes.
set -euo pipefail
profile=${1:?usage: plan.sh /path/to/profile.env}
source "$profile"
: "${N42_RS_REPO:?missing Rust repository}"
[ "$F7_FLOOD_ALG" = ed25519 ] && [ "$N42_INGEST_VERIFY" = all ] || { echo 'strict Ed25519 profile required' >&2; exit 1; }
[ -z "${N42_FRAME_GATEWAYS:-}" ] && [ -z "${F7_FLOOD_CLAIM:-}" ] || { echo 'gateway or claimed-sender bypass is not part of this profile' >&2; exit 1; }
exec bash "$N42_RS_REPO/scripts/fleet7.sh" plan
