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

package zkprover

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ForkConfig parameterizes EVM fork rules for the guest program.
// Fields must be kept in sync with params.Rules — see forkToChainRules() in
// guest/apply_tx.go and BuildGuestInput() in input_builder.go.
type ForkConfig struct {
	IsHomestead           bool
	IsEIP150              bool // TangerineWhistle
	IsEIP155              bool // SpuriousDragon
	IsEIP158              bool // SpuriousDragon
	IsByzantium           bool
	IsConstantinople      bool
	IsPetersburg          bool
	IsIstanbul            bool
	IsBerlin              bool
	IsLondon              bool
	IsShanghai            bool
	IsCancun              bool
	IsBeijing             bool
	IsPrague              bool
	IsPectra              bool
	IsOsaka               bool
	IsFusaka              bool
	IsNano                bool
	IsMoran               bool
	IsEip1559FeeCollector bool
	IsParlia              bool
	IsAura                bool
	IsPQPrecompiles       bool // Post-quantum precompiles (Falcon, Dilithium)
}

// GuestInput is the input data for the zkVM guest program.
type GuestInput struct {
	ChainID      uint64
	BlockHeader  []byte
	ParentHeader []byte
	Transactions [][]byte
	Witness      []byte
	ForkConfig   ForkConfig
}

// GuestOutput is the result produced by the zkVM guest program.
type GuestOutput struct {
	PostStateRoot [32]byte
	ReceiptsRoot  [32]byte
	GasUsed       uint64
	Success       bool
	Error         string
}

// EncodeGuestInput serializes a GuestInput to bytes.
func EncodeGuestInput(input *GuestInput) ([]byte, error) {
	buf := make([]byte, 0, 1024)

	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, input.ChainID)
	buf = append(buf, b...)

	buf, err := guestAppendLenPrefixed(buf, input.BlockHeader)
	if err != nil {
		return nil, fmt.Errorf("block header: %w", err)
	}
	buf, err = guestAppendLenPrefixed(buf, input.ParentHeader)
	if err != nil {
		return nil, fmt.Errorf("parent header: %w", err)
	}

	txCount := make([]byte, 4)
	binary.LittleEndian.PutUint32(txCount, uint32(len(input.Transactions)))
	buf = append(buf, txCount...)
	for i, tx := range input.Transactions {
		buf, err = guestAppendLenPrefixed(buf, tx)
		if err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}
	}

	buf, err = guestAppendLenPrefixed(buf, input.Witness)
	if err != nil {
		return nil, fmt.Errorf("witness: %w", err)
	}

	var flags uint32
	if input.ForkConfig.IsHomestead {
		flags |= 1 << 0
	}
	if input.ForkConfig.IsEIP150 {
		flags |= 1 << 1
	}
	if input.ForkConfig.IsEIP155 {
		flags |= 1 << 2
	}
	if input.ForkConfig.IsEIP158 {
		flags |= 1 << 3
	}
	if input.ForkConfig.IsByzantium {
		flags |= 1 << 4
	}
	if input.ForkConfig.IsConstantinople {
		flags |= 1 << 5
	}
	if input.ForkConfig.IsPetersburg {
		flags |= 1 << 6
	}
	if input.ForkConfig.IsIstanbul {
		flags |= 1 << 7
	}
	if input.ForkConfig.IsBerlin {
		flags |= 1 << 8
	}
	if input.ForkConfig.IsLondon {
		flags |= 1 << 9
	}
	if input.ForkConfig.IsShanghai {
		flags |= 1 << 10
	}
	if input.ForkConfig.IsCancun {
		flags |= 1 << 11
	}
	if input.ForkConfig.IsBeijing {
		flags |= 1 << 12
	}
	if input.ForkConfig.IsPrague {
		flags |= 1 << 13
	}
	if input.ForkConfig.IsPectra {
		flags |= 1 << 14
	}
	if input.ForkConfig.IsOsaka {
		flags |= 1 << 15
	}
	if input.ForkConfig.IsFusaka {
		flags |= 1 << 16
	}
	if input.ForkConfig.IsNano {
		flags |= 1 << 17
	}
	if input.ForkConfig.IsMoran {
		flags |= 1 << 18
	}
	if input.ForkConfig.IsEip1559FeeCollector {
		flags |= 1 << 19
	}
	if input.ForkConfig.IsParlia {
		flags |= 1 << 20
	}
	if input.ForkConfig.IsAura {
		flags |= 1 << 21
	}
	if input.ForkConfig.IsPQPrecompiles {
		flags |= 1 << 22
	}
	fb := make([]byte, 4)
	binary.LittleEndian.PutUint32(fb, flags)
	buf = append(buf, fb...)

	return buf, nil
}

// DecodeGuestInput deserializes a GuestInput from bytes.
func DecodeGuestInput(data []byte) (*GuestInput, error) {
	if len(data) < 8 {
		return nil, errors.New("guest input too short")
	}
	r := &guestByteReader{data: data}
	input := &GuestInput{}

	chainIDBytes, err := r.readN(8)
	if err != nil {
		return nil, err
	}
	input.ChainID = binary.LittleEndian.Uint64(chainIDBytes)

	input.BlockHeader, err = r.readLenPrefixed()
	if err != nil {
		return nil, fmt.Errorf("block header: %w", err)
	}
	input.ParentHeader, err = r.readLenPrefixed()
	if err != nil {
		return nil, fmt.Errorf("parent header: %w", err)
	}

	txCountBytes, err := r.readN(4)
	if err != nil {
		return nil, err
	}
	txCount := binary.LittleEndian.Uint32(txCountBytes)
	input.Transactions = make([][]byte, txCount)
	for i := uint32(0); i < txCount; i++ {
		input.Transactions[i], err = r.readLenPrefixed()
		if err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}
	}

	input.Witness, err = r.readLenPrefixed()
	if err != nil {
		return nil, fmt.Errorf("witness: %w", err)
	}

	flagBytes, err := r.readN(4)
	if err != nil {
		return nil, fmt.Errorf("fork config: %w", err)
	}
	flags := binary.LittleEndian.Uint32(flagBytes)
	input.ForkConfig = ForkConfig{
		IsHomestead:           flags&(1<<0) != 0,
		IsEIP150:              flags&(1<<1) != 0,
		IsEIP155:              flags&(1<<2) != 0,
		IsEIP158:              flags&(1<<3) != 0,
		IsByzantium:           flags&(1<<4) != 0,
		IsConstantinople:      flags&(1<<5) != 0,
		IsPetersburg:          flags&(1<<6) != 0,
		IsIstanbul:            flags&(1<<7) != 0,
		IsBerlin:              flags&(1<<8) != 0,
		IsLondon:              flags&(1<<9) != 0,
		IsShanghai:            flags&(1<<10) != 0,
		IsCancun:              flags&(1<<11) != 0,
		IsBeijing:             flags&(1<<12) != 0,
		IsPrague:              flags&(1<<13) != 0,
		IsPectra:              flags&(1<<14) != 0,
		IsOsaka:               flags&(1<<15) != 0,
		IsFusaka:              flags&(1<<16) != 0,
		IsNano:                flags&(1<<17) != 0,
		IsMoran:               flags&(1<<18) != 0,
		IsEip1559FeeCollector: flags&(1<<19) != 0,
		IsParlia:              flags&(1<<20) != 0,
		IsAura:                flags&(1<<21) != 0,
		IsPQPrecompiles:       flags&(1<<22) != 0,
	}

	return input, nil
}

// EncodeGuestOutput serializes a GuestOutput to bytes.
func EncodeGuestOutput(output *GuestOutput) ([]byte, error) {
	buf := make([]byte, 0, 32+32+8+1+4+len(output.Error))
	buf = append(buf, output.PostStateRoot[:]...)
	buf = append(buf, output.ReceiptsRoot[:]...)
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, output.GasUsed)
	buf = append(buf, b...)
	if output.Success {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	buf, err := guestAppendLenPrefixed(buf, []byte(output.Error))
	if err != nil {
		return nil, fmt.Errorf("error field: %w", err)
	}
	return buf, nil
}

// DecodeGuestOutput deserializes a GuestOutput from bytes.
func DecodeGuestOutput(data []byte) (*GuestOutput, error) {
	if len(data) < 32+32+8+1+4 {
		return nil, errors.New("guest output too short")
	}
	r := &guestByteReader{data: data}
	output := &GuestOutput{}

	rootBytes, _ := r.readN(32)
	copy(output.PostStateRoot[:], rootBytes)
	receiptsBytes, _ := r.readN(32)
	copy(output.ReceiptsRoot[:], receiptsBytes)
	gasBytes, _ := r.readN(8)
	output.GasUsed = binary.LittleEndian.Uint64(gasBytes)
	successByte, _ := r.readN(1)
	output.Success = successByte[0] == 1
	errBytes, err := r.readLenPrefixed()
	if err != nil {
		return nil, err
	}
	output.Error = string(errBytes)

	return output, nil
}

func guestAppendLenPrefixed(buf, data []byte) ([]byte, error) {
	if len(data) > maxGuestFieldSize {
		return nil, fmt.Errorf("field size %d exceeds maximum %d", len(data), maxGuestFieldSize)
	}
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(len(data)))
	buf = append(buf, b...)
	return append(buf, data...), nil
}

type guestByteReader struct {
	data []byte
	pos  int
}

func (r *guestByteReader) readN(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]byte, n)
	copy(out, r.data[r.pos:r.pos+n])
	r.pos += n
	return out, nil
}

const maxGuestFieldSize = 32 * 1024 * 1024 // 32 MB safety limit

func (r *guestByteReader) readLenPrefixed() ([]byte, error) {
	lenBytes, err := r.readN(4)
	if err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(lenBytes)
	if n > maxGuestFieldSize {
		return nil, fmt.Errorf("field size %d exceeds maximum %d", n, maxGuestFieldSize)
	}
	return r.readN(int(n))
}
