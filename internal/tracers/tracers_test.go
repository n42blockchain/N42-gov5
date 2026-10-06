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
	"errors"
	"testing"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

type stubTracer struct{ stopped error }

func (s *stubTracer) CaptureStart(vm.VMInterface, common.Address, common.Address, bool, []byte, uint64, *uint256.Int) {
}
func (s *stubTracer) CaptureEnd([]byte, uint64, error) {}
func (s *stubTracer) CaptureState(uint64, vm.OpCode, uint64, uint64, *vm.ScopeContext, []byte, int, error) {
}
func (s *stubTracer) CaptureFault(uint64, vm.OpCode, uint64, uint64, *vm.ScopeContext, int, error) {}
func (s *stubTracer) CaptureEnter(vm.OpCode, common.Address, common.Address, []byte, uint64, *uint256.Int) {
}
func (s *stubTracer) CaptureExit([]byte, uint64, error) {}
func (s *stubTracer) CaptureTxStart(uint64)             {}
func (s *stubTracer) CaptureTxEnd(uint64)               {}
func (s *stubTracer) GetResult() (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}
func (s *stubTracer) Stop(err error) { s.stopped = err }

func newStubTracer(ctx *Context, cfg json.RawMessage) (Tracer, error) {
	if cfg != nil {
		var m map[string]any
		if err := json.Unmarshal(cfg, &m); err != nil {
			return nil, err
		}
	}
	return &stubTracer{}, nil
}

func TestDirectoryRegisterAndNew(t *testing.T) {
	d := directory{elems: make(map[string]elem)}
	d.Register("stubTracer", newStubTracer, false)

	tr, err := d.New("stubTracer", nil, nil)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if _, ok := tr.(*stubTracer); !ok {
		t.Fatalf("expected *stubTracer")
	}
}

func TestDirectoryNewUnknownWithoutJSEval(t *testing.T) {
	d := directory{elems: make(map[string]elem)}
	if _, err := d.New("doesNotExist", nil, nil); err == nil {
		t.Fatalf("expected error for unknown tracer without JS eval")
	}
}

func TestDirectoryNewFallsBackToJSEval(t *testing.T) {
	d := directory{elems: make(map[string]elem)}
	called := false
	d.RegisterJSEval(func(code string, ctx *Context, cfg json.RawMessage) (Tracer, error) {
		called = true
		if code != "someJsCode" {
			t.Errorf("expected code passthrough, got %s", code)
		}
		return &stubTracer{}, nil
	})
	if _, err := d.New("someJsCode", nil, nil); err != nil {
		t.Fatalf("New error: %v", err)
	}
	if !called {
		t.Errorf("expected JS eval fallback to be invoked")
	}
}

func TestDirectoryIsJS(t *testing.T) {
	d := directory{elems: make(map[string]elem)}
	d.Register("nativeTracer", newStubTracer, false)
	d.Register("jsTracer", newStubTracer, true)

	if d.IsJS("nativeTracer") {
		t.Errorf("expected nativeTracer to report IsJS=false")
	}
	if !d.IsJS("jsTracer") {
		t.Errorf("expected jsTracer to report IsJS=true")
	}
	if !d.IsJS("unregisteredName") {
		t.Errorf("expected unregistered name to default IsJS=true")
	}
}

func TestDirectoryNewPropagatesCtorError(t *testing.T) {
	d := directory{elems: make(map[string]elem)}
	wantErr := errors.New("boom")
	d.Register("failing", func(*Context, json.RawMessage) (Tracer, error) {
		return nil, wantErr
	}, false)

	_, err := d.New("failing", nil, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected ctor error propagated, got %v", err)
	}
}

func TestContextZeroValue(t *testing.T) {
	var ctx Context
	if ctx.BlockHash != (common.Hash{}) || ctx.TxHash != (common.Hash{}) || ctx.TxIndex != 0 || ctx.BlockNumber != nil {
		t.Errorf("expected zero-value Context fields")
	}
}
