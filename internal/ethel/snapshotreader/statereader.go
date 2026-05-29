package snapshotreader

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// StateReader adapts a snapshot Segment to modules/state.StateReader: it serves
// account + storage values from the IMMUTABLE H0 snapshot and delegates contract
// code to a CodeSource (snapshots store only the codeHash dict, not code bytes;
// code lives in the codes freezer). This is the COLD tier — the warm overlay
// (WarmOverlayReader) sits above it for H0+1..tip changes and tombstones.
//
// All reads return (nil,0,nil) "absent" for keys not in the snapshot; the warm
// overlay is responsible for tombstones (keys deleted after H0).
type StateReader struct {
	seg  *Segment
	code state.CodeSource // optional; nil → code reads return empty
}

// NewStateReader wraps seg. code may be nil (e.g. minimal nodes that never
// execute CALLs needing bytecode); then code reads return empty.
func NewStateReader(seg *Segment, code state.CodeSource) *StateReader {
	return &StateReader{seg: seg, code: code}
}

func (r *StateReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	var a20 [20]byte
	copy(a20[:], addr[:])
	raw, ok := r.seg.AccountValueRaw(a20)
	if !ok {
		return nil, nil // not in snapshot
	}
	return r.seg.DecodeAccount(raw)
}

func (r *StateReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	var a20 [20]byte
	copy(a20[:], addr[:])
	var s32 [32]byte
	copy(s32[:], key[:])
	v, ok := r.seg.StorageValue(a20, s32)
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (r *StateReader) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	if account.IsEmptyCodeHash(codeHash) || r.code == nil {
		return nil, nil
	}
	return r.code.GetCode(addr)
}

func (r *StateReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

var _ state.StateReader = (*StateReader)(nil)
