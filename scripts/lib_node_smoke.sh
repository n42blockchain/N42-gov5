#!/usr/bin/env bash

set -euo pipefail

SMOKE_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SMOKE_GO_FLAGS=(-tags nosqlite,noboltdb -trimpath -buildvcs=false)

smoke_timestamp() {
  date -u +"%Y%m%d-%H%M%SZ"
}

smoke_build_binary() {
  local output="$1"
  local pkg="$2"
  mkdir -p "$(dirname "$output")"
  (
    cd "$SMOKE_REPO_ROOT"
    go build "${SMOKE_GO_FLAGS[@]}" -o "$output" "$pkg"
  )
}

smoke_create_account() {
  local bin="$1"
  local datadir="$2"
  local password_file="$3"
  local output

  output="$("$bin" account new --data.dir "$datadir" --password "$password_file" 2>&1)"
  printf '%s\n' "$output" >&2

  local address
  address="$(printf '%s\n' "$output" | grep -oE '0x[0-9a-fA-F]{40}' | head -1)"
  if [ -z "$address" ]; then
    echo "failed to extract account address" >&2
    return 1
  fi
  printf '%s\n' "$address"
}

smoke_start_dev_node() {
  local bin="$1"
  local datadir="$2"
  local password_file="$3"
  local etherbase="$4"
  local http_port="$5"
  local metrics_port="$6"
  local pprof_port="$7"
  local log_file="$8"

  "$bin" \
    --dev \
    --mine \
    --etherbase "$etherbase" \
    --data.dir "$datadir" \
    --http \
    --http.addr 127.0.0.1 \
    --http.port "$http_port" \
    --http.api "eth,net,web3,txpool,debug" \
    --metrics \
    --metrics.addr 127.0.0.1 \
    --metrics.port "$metrics_port" \
    --pprof \
    --pprof.port "$pprof_port" \
    --password "$password_file" \
    --log.level warn \
    >"$log_file" 2>&1 &
  printf '%s\n' "$!"
}

smoke_start_ethdev_node() {
  local bin="$1"
  local datadir="$2"
  local password_file="$3"
  local etherbase="$4"
  local http_port="$5"
  local metrics_port="$6"
  local pprof_port="$7"
  local log_file="$8"

  "$bin" \
    --ethdev \
    --mine \
    --etherbase "$etherbase" \
    --data.dir "$datadir" \
    --http \
    --http.addr 127.0.0.1 \
    --http.port "$http_port" \
    --http.api "eth,net,web3,txpool,debug" \
    --metrics \
    --metrics.addr 127.0.0.1 \
    --metrics.port "$metrics_port" \
    --pprof \
    --pprof.port "$pprof_port" \
    --password "$password_file" \
    --log.level warn \
    >"$log_file" 2>&1 &
  printf '%s\n' "$!"
}

smoke_wait_for_rpc() {
  local rpc_url="$1"
  local pid="$2"
  local log_file="$3"
  local timeout="${4:-60}"
  local response

  for _ in $(seq 1 "$timeout"); do
    if response="$(smoke_rpc_request "$rpc_url" "eth_blockNumber" "[]")" && printf '%s\n' "$response" | grep -q '"result"'; then
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      cat "$log_file" >&2 || true
      return 1
    fi
    sleep 1
  done

  echo "rpc endpoint did not become ready within ${timeout}s" >&2
  cat "$log_file" >&2 || true
  return 1
}

smoke_wait_for_http() {
  local url="$1"
  local pid="$2"
  local log_file="$3"
  local timeout="${4:-60}"

  for _ in $(seq 1 "$timeout"); do
    if curl -sf "$url" >/dev/null; then
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      cat "$log_file" >&2 || true
      return 1
    fi
    sleep 1
  done

  echo "http endpoint $url did not become ready within ${timeout}s" >&2
  cat "$log_file" >&2 || true
  return 1
}

smoke_rpc_request() {
  local rpc_url="$1"
  local method="$2"
  local params="${3:-[]}"

  curl -sf -X POST \
    -H "Content-Type: application/json" \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}" \
    "$rpc_url"
}

smoke_rpc_assert_result() {
  local rpc_url="$1"
  local method="$2"
  local params="${3:-[]}"
  local response

  response="$(smoke_rpc_request "$rpc_url" "$method" "$params")"
  if ! printf '%s\n' "$response" | grep -q '"result"'; then
    printf '%s\n' "$response" >&2
    return 1
  fi
  printf '%s\n' "$response"
}

smoke_stop_process() {
  local pid="$1"
  if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
    return 0
  fi

  kill -INT "$pid" 2>/dev/null || true
  for _ in $(seq 1 30); do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done

  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 10); do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done

  kill -KILL "$pid" 2>/dev/null || true
}

smoke_extract_hex_result() {
  local response="$1"
  printf '%s\n' "$response" | sed -n 's/.*"result":"\([^"]*\)".*/\1/p'
}

smoke_hex_to_dec() {
  local hex_value="${1#0x}"
  if [ -z "$hex_value" ]; then
    printf '0\n'
    return 0
  fi
  printf '%d\n' "$((16#$hex_value))"
}
