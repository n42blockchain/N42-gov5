package cs

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// WarmSource adapts the warm CS tier (cs.Warm + meta.json sidecar)
// to the Source interface. Only blocks within [BaseBlock, HeadBlock]
// are available; queries outside that window return ErrDeepReorg.
type WarmSource struct {
	w *Warm
}

func NewWarmSource(w *Warm) *WarmSource {
	return &WarmSource{w: w}
}

func (s *WarmSource) RetrieveAccount(blk uint64) ([]byte, error) {
	data, err := s.w.Retrieve(freezer.TableAccountChanges, blk)
	if err != nil && errors.Is(err, ErrOutOfWindow) {
		return nil, ErrDeepReorg
	}
	return data, err
}

func (s *WarmSource) RetrieveStorage(blk uint64) ([]byte, error) {
	data, err := s.w.Retrieve(freezer.TableStorageChanges, blk)
	if err != nil && errors.Is(err, ErrOutOfWindow) {
		return nil, ErrDeepReorg
	}
	return data, err
}

func (s *WarmSource) Available(blk uint64) bool {
	return s.w.Contains(blk)
}

func (s *WarmSource) WindowDescription() string {
	m := s.w.Meta()
	return fmt.Sprintf("warm[%d, %d] (%d blocks)", m.BaseBlock, m.HeadBlock, m.KeepBlocks)
}
