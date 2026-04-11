#!/usr/bin/env bash
# Rewrite Erigon import paths to N42 depshim/cl paths inside internal/cl.
# Idempotent: safe to re-run after pulling fresh files from upstream Caplin.
#
# Mapping:
#   github.com/erigontech/erigon/cl/...                      -> github.com/n42blockchain/N42/internal/cl/...
#   github.com/erigontech/erigon/common/hexutil              -> .../internal/cl/depshim/hexutil
#   github.com/erigontech/erigon/common/length               -> .../internal/cl/depshim/length
#   github.com/erigontech/erigon/common/clonable             -> .../internal/cl/depshim/clonable
#   github.com/erigontech/erigon/common/dbg                  -> .../internal/cl/depshim/dbg
#   github.com/erigontech/erigon/common/empty                -> .../internal/cl/depshim/empty
#   github.com/erigontech/erigon/common/maphash              -> .../internal/cl/depshim/maphash
#   github.com/erigontech/erigon/common/ssz                  -> .../internal/cl/depshim/sszh   (renamed to avoid collision with cl/ssz)
#   github.com/erigontech/erigon/common/log/v3               -> .../internal/cl/depshim/log
#   github.com/erigontech/erigon/common/crypto               -> .../internal/cl/depshim/crypto
#   github.com/erigontech/erigon/common                      -> .../internal/cl/depshim/common
#   github.com/erigontech/erigon/db/kv                       -> .../internal/cl/depshim/kv
#   github.com/erigontech/erigon/execution/types             -> .../internal/cl/depshim/types
#   github.com/erigontech/erigon/execution/chain/networkname -> .../internal/cl/depshim/networkname
#   github.com/erigontech/erigon/execution/chain/spec        -> .../internal/cl/depshim/chainspec
#   github.com/erigontech/erigon/execution/protocol/rules/merge -> .../internal/cl/depshim/merge
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="$ROOT/internal/cl"

if [[ ! -d "$TARGET" ]]; then
  echo "no internal/cl directory at $TARGET" >&2
  exit 1
fi

find "$TARGET" -name "*.go" -print0 | while IFS= read -r -d '' f; do
  sed -i \
    -e 's|github.com/erigontech/erigon/cl/|github.com/n42blockchain/N42/internal/cl/|g' \
    -e 's|github.com/erigontech/erigon/common/hexutil|github.com/n42blockchain/N42/internal/cl/depshim/hexutil|g' \
    -e 's|github.com/erigontech/erigon/common/length|github.com/n42blockchain/N42/internal/cl/depshim/length|g' \
    -e 's|github.com/erigontech/erigon/common/clonable|github.com/n42blockchain/N42/internal/cl/depshim/clonable|g' \
    -e 's|github.com/erigontech/erigon/common/dbg|github.com/n42blockchain/N42/internal/cl/depshim/dbg|g' \
    -e 's|github.com/erigontech/erigon/common/empty|github.com/n42blockchain/N42/internal/cl/depshim/empty|g' \
    -e 's|github.com/erigontech/erigon/common/maphash|github.com/n42blockchain/N42/internal/cl/depshim/maphash|g' \
    -e 's|github.com/erigontech/erigon/common/ssz|github.com/n42blockchain/N42/internal/cl/depshim/sszh|g' \
    -e 's|github.com/erigontech/erigon/common/log/v3|github.com/n42blockchain/N42/internal/cl/depshim/log|g' \
    -e 's|github.com/erigontech/erigon/common/crypto|github.com/n42blockchain/N42/internal/cl/depshim/crypto|g' \
    -e 's|github.com/erigontech/erigon/db/kv|github.com/n42blockchain/N42/internal/cl/depshim/kv|g' \
    -e 's|github.com/erigontech/erigon/execution/types|github.com/n42blockchain/N42/internal/cl/depshim/types|g' \
    -e 's|github.com/erigontech/erigon/execution/chain/networkname|github.com/n42blockchain/N42/internal/cl/depshim/networkname|g' \
    -e 's|github.com/erigontech/erigon/execution/chain/spec|github.com/n42blockchain/N42/internal/cl/depshim/chainspec|g' \
    -e 's|github.com/erigontech/erigon/execution/protocol/rules/merge|github.com/n42blockchain/N42/internal/cl/depshim/merge|g' \
    -e 's|github.com/erigontech/erigon/execution/engineapi/engine_types|github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types|g' \
    -e 's|github.com/erigontech/erigon/node/gointerfaces/typesproto|github.com/n42blockchain/N42/internal/cl/depshim/typesproto|g' \
    -e 's|github.com/erigontech/erigon/common/math|github.com/n42blockchain/N42/internal/cl/depshim/math|g' \
    -e 's|github.com/erigontech/erigon/diagnostics/metrics|github.com/n42blockchain/N42/internal/cl/depshim/metrics|g' \
    -e 's|github.com/erigontech/erigon/p2p/event|github.com/n42blockchain/N42/internal/cl/depshim/event|g' \
    -e 's|github.com/erigontech/erigon/common|github.com/n42blockchain/N42/internal/cl/depshim/common|g' \
    "$f"
done

remaining=$(grep -rln "github.com/erigontech" "$TARGET" || true)
if [[ -n "$remaining" ]]; then
  echo "WARNING: files still referencing erigontech:" >&2
  echo "$remaining" >&2
fi
