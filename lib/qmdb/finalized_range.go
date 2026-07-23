package qmdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"lukechampine.com/blake3"
)

var finalizedRangeMagic = [8]byte{'N', '4', '2', 'F', 'R', 'N', 'G', 1}

const (
	// MaxFinalizedRangeBlocks covers a complete 1,000-view epoch while remaining
	// bounded by the cross-client decoder's aggregate 256 MiB materialization
	// limit. Live sync requests retain their independent, smaller batch bound.
	MaxFinalizedRangeBlocks = 1024
	maxFinalizedRangeBlob   = 16 << 20
)

// FinalizedRangeEntry carries the canonical execution bytes and the header
// identities needed to bind a replay segment before either client imports it.
type FinalizedRangeEntry struct {
	Number       uint64
	BlockHash    Hash
	ParentHash   Hash
	StateRoot    Hash
	ReceiptsRoot Hash
	TxRoot       Hash
	HeaderRLP    []byte
	BlockRLP     []byte
	Receipts     []byte
}

// FinalizedRange is the bounded, language-neutral replay-v2 catch-up frame.
type FinalizedRange struct {
	ChainID     uint64
	GenesisHash Hash
	FromBlock   uint64
	ToBlock     uint64
	Entries     []FinalizedRangeEntry
}

// MarshalFinalizedRange encodes v1 and authenticates all preceding bytes with
// Blake3.
func MarshalFinalizedRange(r *FinalizedRange) ([]byte, error) {
	if err := validateFinalizedRange(r); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(finalizedRangeMagic[:])
	_ = binary.Write(&out, binary.LittleEndian, r.ChainID)
	out.Write(r.GenesisHash[:])
	_ = binary.Write(&out, binary.LittleEndian, r.FromBlock)
	_ = binary.Write(&out, binary.LittleEndian, r.ToBlock)
	_ = binary.Write(&out, binary.LittleEndian, uint64(len(r.Entries)))
	for i := range r.Entries {
		e := &r.Entries[i]
		_ = binary.Write(&out, binary.LittleEndian, e.Number)
		out.Write(e.BlockHash[:])
		out.Write(e.ParentHash[:])
		out.Write(e.StateRoot[:])
		out.Write(e.ReceiptsRoot[:])
		out.Write(e.TxRoot[:])
		for _, blob := range [][]byte{e.HeaderRLP, e.BlockRLP, e.Receipts} {
			_ = binary.Write(&out, binary.LittleEndian, uint32(len(blob)))
			out.Write(blob)
		}
	}
	digest := blake3.Sum256(out.Bytes())
	out.Write(digest[:])
	return out.Bytes(), nil
}

func validateFinalizedRange(r *FinalizedRange) error {
	if r == nil || len(r.Entries) == 0 {
		return errors.New("finalized range is empty")
	}
	if len(r.Entries) > MaxFinalizedRangeBlocks {
		return fmt.Errorf("finalized range has %d blocks, max %d", len(r.Entries), MaxFinalizedRangeBlocks)
	}
	if r.FromBlock > r.ToBlock || r.ToBlock-r.FromBlock+1 != uint64(len(r.Entries)) {
		return errors.New("finalized range bounds do not match entry count")
	}
	for i := range r.Entries {
		e := &r.Entries[i]
		want := r.FromBlock + uint64(i)
		if e.Number != want {
			return fmt.Errorf("non-contiguous finalized block: got %d, want %d", e.Number, want)
		}
		if i > 0 && e.ParentHash != r.Entries[i-1].BlockHash {
			return fmt.Errorf("finalized block %d parent does not match block %d", e.Number, e.Number-1)
		}
		if len(e.HeaderRLP) == 0 || len(e.BlockRLP) == 0 {
			return fmt.Errorf("finalized block %d is missing canonical bytes", e.Number)
		}
		for _, blob := range [][]byte{e.HeaderRLP, e.BlockRLP, e.Receipts} {
			if len(blob) > maxFinalizedRangeBlob {
				return fmt.Errorf("finalized block %d blob exceeds %d bytes", e.Number, maxFinalizedRangeBlob)
			}
		}
	}
	return nil
}
