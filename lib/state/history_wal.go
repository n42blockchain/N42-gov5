/*
   Copyright 2022 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package state

import (
	"context"

	"github.com/n42blockchain/N42/lib/log/v3"

	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
)

type historyFlusher struct {
	h *historyWAL
	i *invertedIndexWAL
}

func (f historyFlusher) Flush(ctx context.Context, tx kv.RwTx) error {
	if err := f.i.Flush(ctx, tx); err != nil {
		return err
	}
	return f.h.flush(ctx, tx)
}

type historyWAL struct {
	h                *History
	historyVals      *etl.Collector
	tmpdir           string
	autoIncrementBuf []byte
	historyKey       []byte
	buffered         bool
	discard          bool

	// not large:
	//   keys: txNum -> key1+key2
	//   vals: key1+key2 -> txNum + value (DupSort)
	// large:
	//   keys: txNum -> key1+key2
	//   vals: key1+key2+txNum -> value (not DupSort)
	largeValues bool
}

func (h *historyWAL) close() {
	if h == nil { // allow double-close
		return
	}
	if h.historyVals != nil {
		h.historyVals.Close()
	}
}

func (h *historyWAL) flush(ctx context.Context, tx kv.RwTx) error {
	if h.discard || !h.buffered {
		return nil
	}
	if err := h.historyVals.Load(tx, h.h.historyValsTable, loadFunc, etl.TransformArgs{Quit: ctx.Done()}); err != nil {
		return err
	}
	h.close()
	return nil
}

func (h *historyWAL) addPrevValue(key1, key2, original []byte) error {
	if h.discard {
		return nil
	}

	ii := h.h.InvertedIndex
	if h.largeValues {
		lk := len(key1) + len(key2)
		historyKey := h.historyKey[:lk+8]
		copy(historyKey, key1)
		if len(key2) > 0 {
			copy(historyKey[len(key1):], key2)
		}
		copy(historyKey[lk:], h.h.InvertedIndex.txNumBytes[:])

		if !h.buffered {
			if err := h.h.tx.Put(h.h.historyValsTable, historyKey, original); err != nil {
				return err
			}
			if err := ii.tx.Put(ii.indexKeysTable, ii.txNumBytes[:], historyKey[:lk]); err != nil {
				return err
			}
			return nil
		}
		if err := h.historyVals.Collect(historyKey, original); err != nil {
			return err
		}
		if err := ii.wal.indexKeys.Collect(ii.txNumBytes[:], historyKey[:lk]); err != nil {
			return err
		}
		return nil
	}

	lk := len(key1) + len(key2)
	historyKey := h.historyKey[:lk+8+len(original)]
	copy(historyKey, key1)
	copy(historyKey[len(key1):], key2)
	copy(historyKey[lk:], h.h.InvertedIndex.txNumBytes[:])
	copy(historyKey[lk+8:], original)
	historyKey1 := historyKey[:lk]
	historyVal := historyKey[lk:]
	invIdxVal := historyKey[:lk]

	if !h.buffered {
		if err := h.h.tx.Put(h.h.historyValsTable, historyKey1, historyVal); err != nil {
			return err
		}
		if err := ii.tx.Put(ii.indexKeysTable, ii.txNumBytes[:], invIdxVal); err != nil {
			return err
		}
		return nil
	}
	if err := h.historyVals.Collect(historyKey1, historyVal); err != nil {
		return err
	}
	if err := ii.wal.indexKeys.Collect(ii.txNumBytes[:], invIdxVal); err != nil {
		return err
	}
	return nil
}

func (h *History) newWriter(tmpdir string, buffered, discard bool) *historyWAL {
	w := &historyWAL{h: h,
		tmpdir:   tmpdir,
		buffered: buffered,
		discard:  discard,

		autoIncrementBuf: make([]byte, 8),
		historyKey:       make([]byte, 0, 128),
		largeValues:      h.largeValues,
	}
	if buffered {
		w.historyVals = etl.NewCollector(h.historyValsTable, tmpdir, etl.NewSortableBuffer(WALCollectorRAM), h.logger)
		w.historyVals.LogLvl(log.LvlTrace)
	}
	return w
}

func (h *History) AddPrevValue(key1, key2, original []byte) (err error) {
	if original == nil {
		original = []byte{}
	}
	return h.wal.addPrevValue(key1, key2, original)
}

func (h *History) DiscardHistory() {
	h.InvertedIndex.StartWrites()
	h.wal = h.newWriter(h.tmpdir, false, true)
}

func (h *History) StartUnbufferedWrites() {
	h.InvertedIndex.StartUnbufferedWrites()
	h.wal = h.newWriter(h.tmpdir, false, false)
}

func (h *History) StartWrites() {
	h.InvertedIndex.StartWrites()
	h.wal = h.newWriter(h.tmpdir, true, false)
}

func (h *History) FinishWrites() {
	h.InvertedIndex.FinishWrites()
	h.wal.close()
	h.wal = nil
}

func (h *History) Rotate() historyFlusher {
	w := h.wal
	h.wal = h.newWriter(h.wal.tmpdir, h.wal.buffered, h.wal.discard)
	return historyFlusher{h: w, i: h.InvertedIndex.Rotate()}
}

func (h *History) DisableReadAhead() {
	h.InvertedIndex.DisableReadAhead()
	h.dirtyFiles.Walk(func(items []*filesItem) bool {
		for _, item := range items {
			item.decompressor.DisableReadAhead()
			if item.index != nil {
				item.index.DisableReadAhead()
			}
		}
		return true
	})
}

func (h *History) EnableReadAhead() *History {
	h.InvertedIndex.EnableReadAhead()
	h.dirtyFiles.Walk(func(items []*filesItem) bool {
		for _, item := range items {
			item.decompressor.EnableReadAhead()
			if item.index != nil {
				item.index.EnableReadAhead()
			}
		}
		return true
	})
	return h
}

func (h *History) EnableMadvWillNeed() *History {
	h.InvertedIndex.EnableMadvWillNeed()
	h.dirtyFiles.Walk(func(items []*filesItem) bool {
		for _, item := range items {
			item.decompressor.EnableMadvWillNeed()
			if item.index != nil {
				item.index.EnableWillNeed()
			}
		}
		return true
	})
	return h
}

func (h *History) EnableMadvNormalReadAhead() *History {
	h.InvertedIndex.EnableMadvNormalReadAhead()
	h.dirtyFiles.Walk(func(items []*filesItem) bool {
		for _, item := range items {
			item.decompressor.EnableMadvNormal()
			if item.index != nil {
				item.index.EnableMadvNormal()
			}
		}
		return true
	})
	return h
}
