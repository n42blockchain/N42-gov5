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

package logger

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	types "github.com/n42blockchain/N42/common/block"
	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

func TestWriteLogsFormatsEntries(t *testing.T) {
	logs := []*types.Log{
		{
			Address:     common.HexToAddress("0x01"),
			Topics:      []common.Hash{common.HexToHash("0xaa"), common.HexToHash("0xbb")},
			Data:        []byte("payload-data-32-bytes-padding!!"),
			BlockNumber: uint256.NewInt(7),
			TxIndex:     2,
		},
	}
	var buf bytes.Buffer
	WriteLogs(&buf, logs)
	if buf.Len() == 0 {
		t.Fatalf("expected non-empty output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("LOG2:")) {
		t.Errorf("expected LOG2 prefix for 2 topics, got %s", buf.String())
	}
}

func TestJSONLoggerCaptureFlow(t *testing.T) {
	var buf bytes.Buffer
	jl := NewJSONLogger(&Config{EnableMemory: true, EnableReturnData: true}, &buf)
	evm := newLoggerEVM(t)
	addr := common.HexToAddress("0x07")

	jl.CaptureStart(evm, common.HexToAddress("0xfrom"), addr, false, nil, 1000, nil)
	scope := newLoggerScope(t, addr, []uint256.Int{*uint256.NewInt(1)}, []byte("some return/memory data here!!!"))
	jl.CaptureState(0, vm.ADD, 100, 3, scope, []byte("rd"), 0, nil)
	jl.CaptureFault(1, vm.ADD, 0, 0, scope, 0, errors.New("fault"))
	jl.CaptureEnter(vm.CALL, addr, common.HexToAddress("0x08"), nil, 0, nil)
	jl.CaptureExit(nil, 0, nil)
	jl.CaptureTxStart(21000)
	jl.CaptureTxEnd(0)
	jl.CaptureEnd([]byte{0xde, 0xad}, 1000, errors.New("boom"))

	out := buf.String()
	if out == "" {
		t.Fatalf("expected JSON log output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("gasUsed")) {
		t.Errorf("expected end-log entry with gasUsed, got %s", out)
	}
}

func TestJSONLoggerDefaultsConfig(t *testing.T) {
	var buf bytes.Buffer
	jl := NewJSONLogger(nil, &buf)
	if jl.cfg == nil {
		t.Fatalf("expected default config")
	}
}
