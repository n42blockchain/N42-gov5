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

package native

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers"
)

func TestMuxTracerRegistered(t *testing.T) {
	if _, err := tracers.DefaultDirectory.New("muxTracer", nil, nil); err != nil {
		t.Fatalf("muxTracer not registered: %v", err)
	}
}

func TestMuxTracerFanOut(t *testing.T) {
	cfg := json.RawMessage(`{"callTracer":{},"4byteTracer":{},"noopTracer":{}}`)
	tr, err := newMuxTracer(nil, cfg)
	if err != nil {
		t.Fatalf("newMuxTracer error: %v", err)
	}
	mt := tr.(*muxTracer)
	if len(mt.tracers) != 3 {
		t.Fatalf("expected 3 sub-tracers, got %d", len(mt.tracers))
	}

	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	mt.CaptureTxStart(100000)
	mt.CaptureStart(newTestEVM(), from, to, false, []byte{0x01, 0x02, 0x03, 0x04}, 90000, uint256.NewInt(0))
	mt.CaptureState(0, 0, 0, 0, nil, nil, 0, nil)
	mt.CaptureFault(0, 0, 0, 0, nil, 0, nil)
	mt.CaptureEnter(0x01, from, to, nil, 1000, uint256.NewInt(0))
	mt.CaptureExit(nil, 100, nil)
	mt.CaptureEnd(nil, 1000, nil)
	mt.CaptureTxEnd(89000)

	res, err := mt.GetResult()
	if err != nil {
		t.Fatalf("GetResult error: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(res, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, name := range []string{"callTracer", "4byteTracer", "noopTracer"} {
		if _, ok := obj[name]; !ok {
			t.Errorf("missing sub-tracer result for %s", name)
		}
	}
}

func TestMuxTracerInvalidChildConfig(t *testing.T) {
	cfg := json.RawMessage(`not json`)
	if _, err := newMuxTracer(nil, cfg); err == nil {
		t.Fatalf("expected error for invalid config")
	}
}

func TestMuxTracerUnknownChild(t *testing.T) {
	cfg := json.RawMessage(`{"doesNotExistTracer":{}}`)
	if _, err := newMuxTracer(nil, cfg); err == nil {
		t.Fatalf("expected error for unknown child tracer")
	}
}

func TestMuxTracerStopPropagates(t *testing.T) {
	cfg := json.RawMessage(`{"callTracer":{}}`)
	tr, err := newMuxTracer(nil, cfg)
	if err != nil {
		t.Fatalf("newMuxTracer: %v", err)
	}
	mt := tr.(*muxTracer)
	mt.CaptureStart(nil, common.Address{}, common.Address{}, false, nil, 0, uint256.NewInt(0))
	mt.CaptureEnd(nil, 0, nil)
	stopErr := errors.New("halt")
	mt.Stop(stopErr)
	_, err = mt.GetResult()
	if !errors.Is(err, stopErr) {
		t.Fatalf("expected stop reason propagated through mux, got %v", err)
	}
}
