// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package tracers

import (
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	common "github.com/n42blockchain/N42/common/types"
)

func TestFlatTraceConfig(t *testing.T) {
	cfg := flatTraceConfig()
	if cfg.Tracer == nil || *cfg.Tracer != flatCallTracerName {
		t.Fatalf("expected tracer %s, got %v", flatCallTracerName, cfg.Tracer)
	}
	var m map[string]bool
	if err := json.Unmarshal(cfg.TracerConfig, &m); err != nil {
		t.Fatalf("unmarshal tracer config: %v", err)
	}
	if !m["convertParityErrors"] {
		t.Errorf("expected convertParityErrors true")
	}
}

func TestRawTraceArrayNilAndEmpty(t *testing.T) {
	arr, err := rawTraceArray(nil)
	if err != nil || arr != nil {
		t.Fatalf("expected nil,nil for nil input, got %v %v", arr, err)
	}
	arr, err = rawTraceArray(json.RawMessage(``))
	if err != nil || arr != nil {
		t.Fatalf("expected nil,nil for empty raw message, got %v %v", arr, err)
	}
}

func TestRawTraceArrayFromRawMessage(t *testing.T) {
	raw := json.RawMessage(`[{"a":1},{"b":2}]`)
	arr, err := rawTraceArray(raw)
	if err != nil {
		t.Fatalf("rawTraceArray: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(arr))
	}
}

func TestRawTraceArrayFromArbitraryValue(t *testing.T) {
	val := []map[string]int{{"x": 1}}
	arr, err := rawTraceArray(val)
	if err != nil {
		t.Fatalf("rawTraceArray: %v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("expected 1 element, got %d", len(arr))
	}
}

func TestRawTraceArrayInvalidJSON(t *testing.T) {
	if _, err := rawTraceArray(json.RawMessage(`not json`)); err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}

func TestFlattenBlockSkipsErroredAndNil(t *testing.T) {
	results := []*txTraceResult{
		nil,
		{Error: "boom"},
		{Result: json.RawMessage(`[{"a":1}]`)},
	}
	out, err := flattenBlock(results)
	if err != nil {
		t.Fatalf("flattenBlock: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(out))
	}
}

func TestFlattenBlockPropagatesError(t *testing.T) {
	results := []*txTraceResult{
		{Result: json.RawMessage(`not json`)},
	}
	if _, err := flattenBlock(results); err == nil {
		t.Fatalf("expected error propagated from malformed frame")
	}
}

func TestTraceTypeWanted(t *testing.T) {
	if !traceTypeWanted(nil, "trace") {
		t.Errorf("expected empty list to default to trace")
	}
	if traceTypeWanted(nil, "vmTrace") {
		t.Errorf("expected empty list to reject vmTrace")
	}
	if !traceTypeWanted([]string{"vmTrace", "trace"}, "trace") {
		t.Errorf("expected trace to be found in list")
	}
	if traceTypeWanted([]string{"vmTrace"}, "trace") {
		t.Errorf("expected trace not found when absent")
	}
}

func TestTopLevelOutputFromCallResult(t *testing.T) {
	out := hexutil.Bytes{0xde, 0xad}
	frame, _ := json.Marshal(map[string]any{
		"traceAddress": []int{},
		"result":       map[string]any{"output": out},
	})
	got := topLevelOutput([]json.RawMessage{frame})
	if len(got) != 2 || got[0] != 0xde {
		t.Fatalf("expected output bytes, got %v", got)
	}
}

func TestTopLevelOutputFromCreateCode(t *testing.T) {
	code := hexutil.Bytes{0x60, 0x60}
	frame, _ := json.Marshal(map[string]any{
		"traceAddress": []int{},
		"result":       map[string]any{"code": code},
	})
	got := topLevelOutput([]json.RawMessage{frame})
	if len(got) != 2 || got[0] != 0x60 {
		t.Fatalf("expected code bytes, got %v", got)
	}
}

func TestTopLevelOutputSkipsNonRoot(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{
		"traceAddress": []int{0},
		"result":       map[string]any{"output": hexutil.Bytes{0x01}},
	})
	got := topLevelOutput([]json.RawMessage{frame})
	if len(got) != 0 {
		t.Fatalf("expected empty output for non-root frame, got %v", got)
	}
}

func TestTopLevelOutputEmptyOnNoResult(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{
		"traceAddress": []int{},
	})
	got := topLevelOutput([]json.RawMessage{frame})
	if len(got) != 0 {
		t.Fatalf("expected empty bytes, got %v", got)
	}
}

func TestTopLevelOutputMalformedFrameIsSkipped(t *testing.T) {
	got := topLevelOutput([]json.RawMessage{json.RawMessage(`not json`)})
	if len(got) != 0 {
		t.Fatalf("expected empty result for malformed frame, got %v", got)
	}
}

func TestBuildReplayIncludesTraceWhenRequested(t *testing.T) {
	frames := []json.RawMessage{json.RawMessage(`{"traceAddress":[]}`)}
	hash := common.HexToHash("0x01")
	r := buildReplay(frames, nil, &hash)
	if len(r.Trace) != 1 {
		t.Fatalf("expected trace included by default, got %d entries", len(r.Trace))
	}
	if r.TransactionHash == nil || *r.TransactionHash != hash {
		t.Fatalf("expected tx hash propagated")
	}
}

func TestBuildReplayOmitsTraceWhenNotRequested(t *testing.T) {
	frames := []json.RawMessage{json.RawMessage(`{"traceAddress":[]}`)}
	r := buildReplay(frames, []string{"vmTrace"}, nil)
	if len(r.Trace) != 0 {
		t.Fatalf("expected no trace entries, got %d", len(r.Trace))
	}
}

func TestAddressSetNilAndPopulated(t *testing.T) {
	if addressSet(nil) != nil {
		t.Fatalf("expected nil set for empty input")
	}
	a := common.HexToAddress("0x01")
	set := addressSet([]common.Address{a})
	if _, ok := set[a]; !ok {
		t.Fatalf("expected address present in set")
	}
}

func TestFrameMatchesAddresses(t *testing.T) {
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	frame, _ := json.Marshal(map[string]any{
		"action": map[string]any{"from": from, "to": to},
	})

	if !frameMatchesAddresses(frame, nil, nil) {
		t.Errorf("expected match with nil filters")
	}
	if !frameMatchesAddresses(frame, addressSet([]common.Address{from}), nil) {
		t.Errorf("expected match on from filter")
	}
	if frameMatchesAddresses(frame, addressSet([]common.Address{common.HexToAddress("0x99")}), nil) {
		t.Errorf("expected no match for unrelated from filter")
	}
	if !frameMatchesAddresses(frame, nil, addressSet([]common.Address{to})) {
		t.Errorf("expected match on to filter")
	}
	if frameMatchesAddresses(frame, nil, addressSet([]common.Address{common.HexToAddress("0x99")})) {
		t.Errorf("expected no match for unrelated to filter")
	}
}

func TestFrameMatchesAddressesMalformed(t *testing.T) {
	if frameMatchesAddresses(json.RawMessage(`not json`), addressSet([]common.Address{{}}), nil) {
		t.Fatalf("expected malformed frame to not match")
	}
}

func TestFrameMatchesAddressesMissingFields(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{"action": map[string]any{}})
	fromSet := addressSet([]common.Address{common.HexToAddress("0x01")})
	if frameMatchesAddresses(frame, fromSet, nil) {
		t.Fatalf("expected no match when from is absent but filter set")
	}
	toSet := addressSet([]common.Address{common.HexToAddress("0x01")})
	if frameMatchesAddresses(frame, nil, toSet) {
		t.Fatalf("expected no match when to is absent but filter set")
	}
}

func TestIntSliceEqual(t *testing.T) {
	if !intSliceEqual(nil, nil) {
		t.Errorf("expected nil slices equal")
	}
	if !intSliceEqual([]int{1, 2}, []int{1, 2}) {
		t.Errorf("expected equal slices to match")
	}
	if intSliceEqual([]int{1, 2}, []int{1, 3}) {
		t.Errorf("expected mismatched slices to differ")
	}
	if intSliceEqual([]int{1}, []int{1, 2}) {
		t.Errorf("expected different-length slices to differ")
	}
}
