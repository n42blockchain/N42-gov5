// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import "github.com/n42blockchain/N42/common/types"

// StartWriteRecord makes the state record every address its mutators
// write from now on (balance, nonce, code, storage, creation,
// self-destruction), until StopWriteRecord. The lag pipeline's early
// snapshot (internal/lag_early_snapshot.go) records what a block writes
// after its transactions' fold, to check that a child read none of it
// before the block's complete post-state was published.
func (sdb *IntraBlockState) StartWriteRecord() {
	sdb.writeRec = make(map[types.Address]struct{}, 8)
}

// StopWriteRecord ends the recording and returns the addresses written
// since StartWriteRecord (nil when nothing was recording).
func (sdb *IntraBlockState) StopWriteRecord() map[types.Address]struct{} {
	r := sdb.writeRec
	sdb.writeRec = nil
	return r
}

func (sdb *IntraBlockState) recordWrite(addr types.Address) {
	if sdb.writeRec != nil {
		sdb.writeRec[addr] = struct{}{}
	}
}
