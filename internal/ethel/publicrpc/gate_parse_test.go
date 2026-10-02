// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package publicrpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseBlockArg covers the quoted-string, object and malformed/empty
// input branches of parseBlockArg, which gate.go uses to decide whether a
// request targets a specific (gate-checkable) height or an implicit
// "latest" that the capability gate always allows through.
func TestParseBlockArg(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantN  uint64
		wantOK bool
	}{
		{"empty", "", 0, false},
		{"null literal", "null", 0, false},
		{"hex number", `"0x1a"`, 0x1a, true},
		{"earliest tag", `"earliest"`, 0, true},
		{"latest tag", `"latest"`, 0, false},
		{"pending tag", `"pending"`, 0, false},
		{"safe tag", `"safe"`, 0, false},
		{"finalized tag", `"finalized"`, 0, false},
		{"bogus string", `"not-hex"`, 0, false},
		{"object with blockNumber", `{"blockNumber":"0x5"}`, 5, true},
		{"object with blockHash only", `{"blockHash":"0xdead"}`, 0, false},
		{"empty object", `{}`, 0, false},
		{"malformed json", `{`, 0, false},
		{"array (neither quote nor brace)", `[1,2]`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, ok := parseBlockArg(json.RawMessage(c.raw))
			require.Equal(t, c.wantOK, ok)
			if ok {
				require.Equal(t, c.wantN, n)
			}
		})
	}
}

// TestParseBlockTag directly exercises the tag/hex-number switch.
func TestParseBlockTag(t *testing.T) {
	n, ok := parseBlockTag("0xff")
	require.True(t, ok)
	require.Equal(t, uint64(0xff), n)

	_, ok = parseBlockTag("latest")
	require.False(t, ok)

	n, ok = parseBlockTag("earliest")
	require.True(t, ok)
	require.Zero(t, n)

	_, ok = parseBlockTag("not-a-number")
	require.False(t, ok)
}

// TestFromBlockOfFilter exercises the eth_getLogs filter-param extraction:
// fromBlockOfFilter takes the raw JSON-RPC params array and unwraps the
// first element's fromBlock field (or reports ok=false when absent, the
// params aren't an array, or the array is empty).
func TestFromBlockOfFilter(t *testing.T) {
	n, ok := fromBlockOfFilter(json.RawMessage(`[{"fromBlock":"0x10"}]`))
	require.True(t, ok)
	require.Equal(t, uint64(0x10), n)

	_, ok = fromBlockOfFilter(json.RawMessage(`[{}]`))
	require.False(t, ok)

	_, ok = fromBlockOfFilter(json.RawMessage(`[]`))
	require.False(t, ok)

	_, ok = fromBlockOfFilter(json.RawMessage(`not-json`))
	require.False(t, ok)
}
