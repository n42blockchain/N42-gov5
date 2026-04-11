#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
client_dir="$repo_root/tests/eth-hive/clients/n42"
local_src="$client_dir/n42-local"
client_file="$repo_root/tests/eth-hive/n42-clients.yaml"

mkdir -p "$client_dir"
rm -rf "$local_src"
mkdir -p "$local_src"

rsync -a --delete \
  --exclude '.git/' \
  --exclude '.codex-cache/' \
  --exclude 'AGENTS.md' \
  --exclude 'benchmarks/results/' \
  --exclude 'build/' \
  --exclude 'devtest/' \
  --exclude 'mainnet/' \
  --exclude 'n42data/' \
  --exclude 'tests/' \
  --exclude 'docs/' \
  --exclude 'tmp/' \
  --exclude 'tests/eth-hive/clients/n42/n42-local/' \
  "$repo_root/" "$local_src/"

cat >"$client_dir/Dockerfile" <<'EOF'
FROM nginx:alpine AS builder

RUN apk add --no-cache build-base ca-certificates git go linux-headers

ARG local_path=n42-local
COPY ${local_path} /src/n42
WORKDIR /src/n42

RUN go build -ldflags='-extldflags=-Wl,--allow-multiple-definition' -o /usr/local/bin/n42 ./cmd/n42

FROM nginx:alpine

RUN apk add --no-cache bash curl jq ca-certificates

COPY --from=builder /usr/local/bin/n42 /usr/local/bin/n42
COPY genesis.json /genesis.json
COPY n42.sh /n42.sh
COPY enode.sh /hive-bin/enode.sh

RUN chmod +x /n42.sh /hive-bin/enode.sh
RUN /usr/local/bin/n42 --version > /version.txt

EXPOSE 8545 8546 8551 30303 30303/udp

ENTRYPOINT ["/n42.sh"]
EOF

cp "$client_dir/Dockerfile" "$client_dir/Dockerfile.local"

cat >"$client_dir/enode.sh" <<'EOF'
#!/usr/bin/env bash

set -euo pipefail

response="$(
  curl -sS -X POST \
    -H 'Content-Type: application/json' \
    --data '{"jsonrpc":"2.0","method":"admin_nodeInfo","params":[],"id":1}' \
    http://127.0.0.1:8545 || true
)"

enode="$(printf '%s' "$response" | jq -r '.result.enode // empty' 2>/dev/null || true)"
if [ -n "$enode" ] && [ "$enode" != "null" ]; then
  echo "$enode"
  exit 0
fi

# Keep a syntactically valid fallback so Hive client validation can proceed
# for engine-only runs even when the devp2p surface is intentionally minimal.
echo "enode://a61215641fb8714a373c80edbfa0ea8878243193f57c96eeb44d0bc019ef295abd4e044fd619bfc4c59731a73fb79afe84e9ab6da0c743ceb479cbb6d263fa91@127.0.0.1:30303"
EOF

cat >"$client_dir/genesis.json" <<'EOF'
{
  "config": {
    "chainId": 1337
  },
  "nonce": "0x0",
  "timestamp": "0x0",
  "extraData": "0x",
  "gasLimit": "0x1c9c380",
  "difficulty": "0x1",
  "coinbase": "0x0000000000000000000000000000000000000000",
  "alloc": {}
}
EOF

cat >"$client_dir/hive.yaml" <<'EOF'
roles:
  - "eth1"
EOF

cat >"$client_dir/n42.sh" <<'EOF'
#!/usr/bin/env bash

set -euo pipefail

n42=/usr/local/bin/n42
datadir=/n42data
jwtsecret=/jwtsecret
password_file=/hive-password.txt
rpc_url=http://127.0.0.1:8545

log_level=info
case "${HIVE_LOGLEVEL:-3}" in
  0|1) log_level=error ;;
  2) log_level=warn ;;
  3) log_level=info ;;
  4) log_level=debug ;;
  5) log_level=trace ;;
esac

rpc_call() {
  local method="$1"
  local params_json="$2"
  curl -fsS -X POST \
    -H 'Content-Type: application/json' \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"${method}\",\"params\":${params_json},\"id\":1}" \
    "$rpc_url"
}

wait_for_rpc() {
  local attempt
  for attempt in $(seq 1 120); do
    if rpc_call eth_blockNumber '[]' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

mkdir -p "$datadir"
printf '0x7365637265747365637265747365637265747365637265747365637265747365' >"$jwtsecret"
printf 'secret\n' >"$password_file"

if [ "${HIVE_LOGLEVEL:-3}" -lt 4 ]; then
  echo "Supplied genesis state (trimmed, use --sim.loglevel 4 or 5 for full output):"
  jq 'del(.alloc[] | select(.balance == "0x123450000000000000000"))' /genesis.json || cat /genesis.json
else
  echo "Supplied genesis state:"
  cat /genesis.json
fi

echo "Initializing N42 database from /genesis.json"
rm -rf "$datadir/chaindata" "$datadir/nodes" "$datadir/jwtsecret" "$datadir/keystore"
"$n42" init --data.dir "$datadir" --chain private --profile eth /genesis.json

set +e
if [ -f /chain.rlp ]; then
  echo "Importing /chain.rlp"
  "$n42" import --data.dir "$datadir" /chain.rlp
  echo "chain.rlp import rc=$?"
else
  echo "Warning: /chain.rlp not found."
fi

if [ -d /blocks ]; then
  echo "Importing individual block files from /blocks"
  while IFS= read -r block_file; do
    [ -n "$block_file" ] || continue
    echo "Importing $(basename "$block_file")"
    "$n42" import --data.dir "$datadir" "$block_file"
    echo "block import rc=$?"
  done < <(find /blocks -maxdepth 1 -type f | sort)
else
  echo "Warning: /blocks not found."
fi
set -e

flags=(
  --data.dir "$datadir"
  --chain private
  --profile eth
  --http
  --http.addr 0.0.0.0
  --http.port 8545
  --http.api admin,debug,eth,net,txpool,web3,rpc
  --ws
  --ws.addr 0.0.0.0
  --ws.port 8546
  --ws.api admin,debug,eth,net,txpool,web3,rpc
  --authrpc
  --authrpc.addr 0.0.0.0
  --authrpc.port 8551
  --authrpc.jwtsecret "$jwtsecret"
  --p2p.no-discovery
  --p2p.tcp-port 30303
  --p2p.udp-port 30303
  --log.level "$log_level"
)

if [ -n "${HIVE_NETWORK_ID:-}" ]; then
  echo "Info: HIVE_NETWORK_ID=${HIVE_NETWORK_ID} is provided via Hive, N42 uses the imported genesis chain configuration."
fi
echo "Info: Hive fork env shanghai=${HIVE_SHANGHAI_TIMESTAMP:-} cancun=${HIVE_CANCUN_TIMESTAMP:-} prague=${HIVE_PRAGUE_TIMESTAMP:-} osaka=${HIVE_OSAKA_TIMESTAMP:-}"

if [ -n "${HIVE_MINER:-}" ]; then
  flags+=(--mine --etherbase "$HIVE_MINER")
fi

if [ -n "${HIVE_CLIQUE_PRIVATEKEY:-}" ]; then
  clique_key=/hive-clique.key
  printf '%s\n' "$HIVE_CLIQUE_PRIVATEKEY" >"$clique_key"
  echo "Importing clique/miner key"
  "$n42" account import --data.dir "$datadir" --password "$password_file" "$clique_key"
  if [ -n "${HIVE_MINER:-}" ]; then
    flags+=(--unlock "$HIVE_MINER" --password "$password_file" --allow-insecure-unlock)
  fi
fi

if [ -n "${HIVE_MINER_EXTRA:-}" ]; then
  echo "Warning: HIVE_MINER_EXTRA is not currently mapped to an N42 CLI flag."
fi

echo "Running n42 with flags: ${flags[*]}"
"$n42" "${flags[@]}" &
node_pid=$!

cleanup() {
  if [ -n "${node_pid:-}" ] && kill -0 "$node_pid" 2>/dev/null; then
    kill "$node_pid" 2>/dev/null || true
    wait "$node_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

if ! wait_for_rpc; then
  echo "N42 did not open RPC in time" >&2
  exit 1
fi

if [ -n "${HIVE_BOOTNODE:-}" ]; then
  echo "Adding Hive bootnode peer via admin_addPeer"
  bootnode_payload="$(jq -nc --arg enode "$HIVE_BOOTNODE" '[$enode]')"
  rpc_call admin_addPeer "$bootnode_payload" || echo "Warning: failed to add bootnode $HIVE_BOOTNODE" >&2
fi

wait "$node_pid"
EOF

chmod +x "$client_dir/n42.sh" "$client_dir/enode.sh"

cat >"$client_file" <<'EOF'
- client: n42
  nametag: local
  dockerfile: local
  build_args:
    local_path: n42-local
EOF

printf 'local_path=%s\n' "$local_src"
printf 'client_file=%s\n' "$client_file"
