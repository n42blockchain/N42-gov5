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
	"bytes"
	"container/heap"
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/n42blockchain/N42/lib/log/v3"
	"golang.org/x/sync/errgroup"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/background"
	"github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/bitmapdb"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	"github.com/n42blockchain/N42/lib/seg"
)

type HistoryCollation struct {
	historyComp  *seg.Compressor
	indexBitmaps map[string]*roaring64.Bitmap
	historyPath  string
	historyCount int
}

func (c HistoryCollation) Close() {
	if c.historyComp != nil {
		c.historyComp.Close()
	}
	for _, b := range c.indexBitmaps {
		bitmapdb.ReturnToPool64(b)
	}
}

type HistoryFiles struct {
	historyDecomp   *seg.Decompressor
	historyIdx      *recsplit.Index
	efHistoryDecomp *seg.Decompressor
	efHistoryIdx    *recsplit.Index
}

func (sf HistoryFiles) Close() {
	if sf.historyDecomp != nil {
		sf.historyDecomp.Close()
	}
	if sf.historyIdx != nil {
		sf.historyIdx.Close()
	}
	if sf.efHistoryDecomp != nil {
		sf.efHistoryDecomp.Close()
	}
	if sf.efHistoryIdx != nil {
		sf.efHistoryIdx.Close()
	}
}

func (h *History) missedIdxFiles() (l []*filesItem) {
	h.dirtyFiles.Walk(func(items []*filesItem) bool { // don't run slow logic while iterating on btree
		for _, item := range items {
			fromStep, toStep := item.startTxNum/h.aggregationStep, item.endTxNum/h.aggregationStep
			if !dir.FileExist(filepath.Join(h.dir, fmt.Sprintf("%s.%d-%d.vi", h.filenameBase, fromStep, toStep))) {
				l = append(l, item)
			}
		}
		return true
	})
	return l
}

// BuildOptionalMissedIndices - no-op placeholder for optional index building
func (ht *HistoryRoTx) BuildOptionalMissedIndices(ctx context.Context) (err error) {
	return nil
}

// BuildMissedIndices - produce .efi/.vi/.kvi from .ef/.v/.kv
func (h *History) BuildMissedIndices(ctx context.Context, g *errgroup.Group, ps *background.ProgressSet) {
	h.InvertedIndex.BuildMissedIndices(ctx, g, ps)
	missedFiles := h.missedIdxFiles()
	for _, item := range missedFiles {
		item := item
		g.Go(func() error {
			p := &background.Progress{}
			ps.Add(p)
			defer ps.Delete(p)
			return h.buildVi(ctx, item, p)
		})
	}
}

func (h *History) buildVi(ctx context.Context, item *filesItem, p *background.Progress) (err error) {
	search := &filesItem{startTxNum: item.startTxNum, endTxNum: item.endTxNum}
	iiItem, ok := h.InvertedIndex.dirtyFiles.Get(search)
	if !ok {
		return nil
	}

	fromStep, toStep := item.startTxNum/h.aggregationStep, item.endTxNum/h.aggregationStep
	fName := fmt.Sprintf("%s.%d-%d.vi", h.filenameBase, fromStep, toStep)
	idxPath := filepath.Join(h.dir, fName)

	p.Name.Store(&fName)
	p.Total.Store(uint64(iiItem.decompressor.Count()) * 2)

	count, err := iterateForVi(item, iiItem, p, h.compressVals, func(v []byte) error { return nil })
	if err != nil {
		return err
	}
	return buildVi(ctx, item, iiItem, idxPath, h.tmpdir, count, p, h.compressVals, h.logger)
}

func iterateForVi(historyItem, iiItem *filesItem, p *background.Progress, compressVals bool, f func(v []byte) error) (count int, err error) {
	var cp CursorHeap
	heap.Init(&cp)
	g := iiItem.decompressor.MakeGetter()
	g.Reset(0)
	if g.HasNext() {
		g2 := historyItem.decompressor.MakeGetter()
		key, _ := g.NextUncompressed()
		val, _ := g.NextUncompressed()
		heap.Push(&cp, &CursorItem{
			t:        FILE_CURSOR,
			dg:       g,
			dg2:      g2,
			key:      key,
			val:      val,
			endTxNum: iiItem.endTxNum,
			reverse:  false,
		})
	}

	// In the loop below, the pair `keyBuf=>valBuf` is always 1 item behind `lastKey=>lastVal`.
	// `lastKey` and `lastVal` are taken from the top of the multi-way merge (assisted by the CursorHeap cp), but not processed right away
	// instead, the pair from the previous iteration is processed first - `keyBuf=>valBuf`. After that, `keyBuf` and `valBuf` are assigned
	// to `lastKey` and `lastVal` correspondingly, and the next step of multi-way merge happens. Therefore, after the multi-way merge loop
	// (when CursorHeap cp is empty), there is a need to process the last pair `keyBuf=>valBuf`, because it was one step behind
	var valBuf []byte
	for cp.Len() > 0 {
		lastKey := common.Copy(cp[0].key)
		// Advance all the items that have this key (including the top)
		for cp.Len() > 0 && bytes.Equal(cp[0].key, lastKey) {
			ci1 := cp[0]
			keysCount := eliasfano32.Count(ci1.val)
			for i := uint64(0); i < keysCount; i++ {
				if compressVals {
					valBuf, _ = ci1.dg2.Next(valBuf[:0])
				} else {
					valBuf, _ = ci1.dg2.NextUncompressed()
				}
				if err = f(valBuf); err != nil {
					return count, err
				}
			}
			count += int(keysCount)
			if ci1.dg.HasNext() {
				ci1.key, _ = ci1.dg.NextUncompressed()
				ci1.val, _ = ci1.dg.NextUncompressed()
				heap.Fix(&cp, 0)
			} else {
				heap.Remove(&cp, 0)
			}

			p.Processed.Add(1)
		}
	}
	return count, nil
}

func buildVi(ctx context.Context, historyItem, iiItem *filesItem, historyIdxPath, tmpdir string, count int, p *background.Progress, compressVals bool, logger log.Logger) error {
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   count,
		Enums:      false,
		BucketSize: 2000,
		LeafSize:   8,
		TmpDir:     tmpdir,
		IndexFile:  historyIdxPath,
	}, logger)
	if err != nil {
		return fmt.Errorf("create recsplit: %w", err)
	}
	rs.LogLvl(log.LvlTrace)
	defer rs.Close()
	var historyKey []byte
	var txKey [8]byte
	var valOffset uint64

	defer iiItem.decompressor.EnableMadvNormal().DisableReadAhead()
	defer historyItem.decompressor.EnableMadvNormal().DisableReadAhead()

	g := iiItem.decompressor.MakeGetter()
	g2 := historyItem.decompressor.MakeGetter()
	var keyBuf, valBuf []byte
	for {
		g.Reset(0)
		g2.Reset(0)
		valOffset = 0
		for g.HasNext() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			keyBuf, _ = g.NextUncompressed()
			valBuf, _ = g.NextUncompressed()
			ef, _ := eliasfano32.ReadEliasFano(valBuf)
			efIt := ef.Iterator()
			for efIt.HasNext() {
				txNum, _ := efIt.Next()
				binary.BigEndian.PutUint64(txKey[:], txNum)
				historyKey = append(append(historyKey[:0], txKey[:]...), keyBuf...)
				if err = rs.AddKey(historyKey, valOffset); err != nil {
					return err
				}
				if compressVals {
					valOffset, _ = g2.Skip()
				} else {
					valOffset, _ = g2.SkipUncompressed()
				}
			}

			p.Processed.Add(1)
		}
		if err = rs.Build(ctx); err != nil {
			if rs.Collision() {
				logger.Info("Building recsplit. Collision happened. It's ok. Restarting...")
				rs.ResetNextSalt()
			} else {
				return fmt.Errorf("build %s idx: %w", historyIdxPath, err)
			}
		} else {
			break
		}
	}
	return nil
}

func (h *History) collate(step, txFrom, txTo uint64, roTx kv.Tx) (HistoryCollation, error) {
	var historyComp *seg.Compressor
	var err error
	closeComp := true
	defer func() {
		if closeComp {
			if historyComp != nil {
				historyComp.Close()
			}
		}
	}()
	historyPath := filepath.Join(h.dir, fmt.Sprintf("%s.%d-%d.v", h.filenameBase, step, step+1))
	if historyComp, err = seg.NewCompressor(context.Background(), "collate history", historyPath, h.tmpdir, seg.MinPatternScore, h.compressWorkers, log.LvlTrace, h.logger); err != nil {
		return HistoryCollation{}, fmt.Errorf("create %s history compressor: %w", h.filenameBase, err)
	}
	keysCursor, err := roTx.CursorDupSort(h.indexKeysTable)
	if err != nil {
		return HistoryCollation{}, fmt.Errorf("create %s history cursor: %w", h.filenameBase, err)
	}
	defer keysCursor.Close()
	indexBitmaps := map[string]*roaring64.Bitmap{}
	var txKey [8]byte
	binary.BigEndian.PutUint64(txKey[:], txFrom)
	var k, v []byte
	for k, v, err = keysCursor.Seek(txKey[:]); err == nil && k != nil; k, v, err = keysCursor.Next() {
		txNum := binary.BigEndian.Uint64(k)
		if txNum >= txTo {
			break
		}
		var bitmap *roaring64.Bitmap
		var ok bool
		if bitmap, ok = indexBitmaps[string(v)]; !ok {
			bitmap = bitmapdb.NewBitmap64()
			indexBitmaps[string(v)] = bitmap
		}
		bitmap.Add(txNum)
	}
	if err != nil {
		return HistoryCollation{}, fmt.Errorf("iterate over %s history cursor: %w", h.filenameBase, err)
	}
	keys := make([]string, 0, len(indexBitmaps))
	for key := range indexBitmaps {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	historyCount := 0
	keyBuf := make([]byte, 256)

	var c kv.Cursor
	var cd kv.CursorDupSort
	if h.largeValues {
		c, err = roTx.Cursor(h.historyValsTable)
		if err != nil {
			return HistoryCollation{}, err
		}
		defer c.Close()
	} else {
		cd, err = roTx.CursorDupSort(h.historyValsTable)
		if err != nil {
			return HistoryCollation{}, err
		}
		defer cd.Close()
	}
	for _, key := range keys {
		bitmap := indexBitmaps[key]
		it := bitmap.Iterator()
		copy(keyBuf, key)
		keyBuf = keyBuf[:len(key)+8]
		for it.HasNext() {
			txNum := it.Next()
			binary.BigEndian.PutUint64(keyBuf[len(key):], txNum)
			//TODO: use cursor range
			if h.largeValues {
				val, err := roTx.GetOne(h.historyValsTable, keyBuf)
				if err != nil {
					return HistoryCollation{}, fmt.Errorf("get %s history val [%x]: %w", h.filenameBase, k, err)
				}
				if len(val) == 0 {
					val = nil
				}
				if err = historyComp.AddUncompressedWord(val); err != nil {
					return HistoryCollation{}, fmt.Errorf("add %s history val [%x]=>[%x]: %w", h.filenameBase, k, val, err)
				}
			} else {
				val, err := cd.SeekBothRange(keyBuf[:len(key)], keyBuf[len(key):])
				if err != nil {
					return HistoryCollation{}, err
				}
				if val != nil && binary.BigEndian.Uint64(val) == txNum {
					val = val[8:]
				} else {
					val = nil
				}
				if err = historyComp.AddUncompressedWord(val); err != nil {
					return HistoryCollation{}, fmt.Errorf("add %s history val [%x]=>[%x]: %w", h.filenameBase, k, val, err)
				}
			}
			historyCount++
		}
	}
	closeComp = false
	return HistoryCollation{
		historyPath:  historyPath,
		historyComp:  historyComp,
		historyCount: historyCount,
		indexBitmaps: indexBitmaps,
	}, nil
}

// buildFiles performs potentially resource intensive operations of creating
// static files and their indices
func (h *History) buildFiles(ctx context.Context, step uint64, collation HistoryCollation, ps *background.ProgressSet) (HistoryFiles, error) {
	historyComp := collation.historyComp
	if h.noFsync {
		historyComp.DisableFsync()
	}
	var historyDecomp, efHistoryDecomp *seg.Decompressor
	var historyIdx, efHistoryIdx *recsplit.Index
	var efHistoryComp *seg.Compressor
	var rs *recsplit.RecSplit
	closeComp := true
	defer func() {
		if closeComp {
			if historyComp != nil {
				historyComp.Close()
			}
			if historyDecomp != nil {
				historyDecomp.Close()
			}
			if historyIdx != nil {
				historyIdx.Close()
			}
			if efHistoryComp != nil {
				efHistoryComp.Close()
			}
			if efHistoryDecomp != nil {
				efHistoryDecomp.Close()
			}
			if efHistoryIdx != nil {
				efHistoryIdx.Close()
			}
			if rs != nil {
				rs.Close()
			}
		}
	}()

	var historyIdxPath, efHistoryPath string

	{
		historyIdxFileName := fmt.Sprintf("%s.%d-%d.vi", h.filenameBase, step, step+1)
		p := ps.AddNew(historyIdxFileName, 1)
		defer ps.Delete(p)
		historyIdxPath = filepath.Join(h.dir, historyIdxFileName)
		if err := historyComp.Compress(); err != nil {
			return HistoryFiles{}, fmt.Errorf("compress %s history: %w", h.filenameBase, err)
		}
		historyComp.Close()
		historyComp = nil
		ps.Delete(p)
	}

	keys := make([]string, 0, len(collation.indexBitmaps))
	for key := range collation.indexBitmaps {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	{
		var err error
		if historyDecomp, err = seg.NewDecompressor(collation.historyPath); err != nil {
			return HistoryFiles{}, fmt.Errorf("open %s history decompressor: %w", h.filenameBase, err)
		}

		// Build history ef
		efHistoryFileName := fmt.Sprintf("%s.%d-%d.ef", h.filenameBase, step, step+1)

		p := ps.AddNew(efHistoryFileName, 1)
		defer ps.Delete(p)
		efHistoryPath = filepath.Join(h.dir, efHistoryFileName)
		efHistoryComp, err = seg.NewCompressor(ctx, "ef history", efHistoryPath, h.tmpdir, seg.MinPatternScore, h.compressWorkers, log.LvlTrace, h.logger)
		if err != nil {
			return HistoryFiles{}, fmt.Errorf("create %s ef history compressor: %w", h.filenameBase, err)
		}
		if h.noFsync {
			efHistoryComp.DisableFsync()
		}
		var buf []byte
		for _, key := range keys {
			if err = efHistoryComp.AddUncompressedWord([]byte(key)); err != nil {
				return HistoryFiles{}, fmt.Errorf("add %s ef history key [%x]: %w", h.InvertedIndex.filenameBase, key, err)
			}
			bitmap := collation.indexBitmaps[key]
			ef := eliasfano32.NewEliasFano(bitmap.GetCardinality(), bitmap.Maximum())
			it := bitmap.Iterator()
			for it.HasNext() {
				txNum := it.Next()
				ef.AddOffset(txNum)
			}
			ef.Build()
			buf = ef.AppendBytes(buf[:0])
			if err = efHistoryComp.AddUncompressedWord(buf); err != nil {
				return HistoryFiles{}, fmt.Errorf("add %s ef history val: %w", h.filenameBase, err)
			}
		}
		if err = efHistoryComp.Compress(); err != nil {
			return HistoryFiles{}, fmt.Errorf("compress %s ef history: %w", h.filenameBase, err)
		}
		efHistoryComp.Close()
		efHistoryComp = nil
		ps.Delete(p)
	}

	var err error
	if efHistoryDecomp, err = seg.NewDecompressor(efHistoryPath); err != nil {
		return HistoryFiles{}, fmt.Errorf("open %s ef history decompressor: %w", h.filenameBase, err)
	}
	efHistoryIdxFileName := fmt.Sprintf("%s.%d-%d.efi", h.filenameBase, step, step+1)
	efHistoryIdxPath := filepath.Join(h.dir, efHistoryIdxFileName)
	p := ps.AddNew(efHistoryIdxFileName, uint64(len(keys)*2))
	defer ps.Delete(p)
	if efHistoryIdx, err = buildIndexThenOpen(ctx, efHistoryDecomp, efHistoryIdxPath, h.tmpdir, len(keys), false /* values */, p, h.logger, h.noFsync); err != nil {
		return HistoryFiles{}, fmt.Errorf("build %s ef history idx: %w", h.filenameBase, err)
	}
	if rs, err = recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   collation.historyCount,
		Enums:      false,
		BucketSize: 2000,
		LeafSize:   8,
		TmpDir:     h.tmpdir,
		IndexFile:  historyIdxPath,
	}, h.logger); err != nil {
		return HistoryFiles{}, fmt.Errorf("create recsplit: %w", err)
	}
	rs.LogLvl(log.LvlTrace)
	if h.noFsync {
		rs.DisableFsync()
	}
	var historyKey []byte
	var txKey [8]byte
	var valOffset uint64
	g := historyDecomp.MakeGetter()
	for {
		g.Reset(0)
		valOffset = 0
		for _, key := range keys {
			bitmap := collation.indexBitmaps[key]
			it := bitmap.Iterator()
			for it.HasNext() {
				txNum := it.Next()
				binary.BigEndian.PutUint64(txKey[:], txNum)
				historyKey = append(append(historyKey[:0], txKey[:]...), key...)
				if err = rs.AddKey(historyKey, valOffset); err != nil {
					return HistoryFiles{}, fmt.Errorf("add %s history idx [%x]: %w", h.filenameBase, historyKey, err)
				}
				valOffset, _ = g.Skip()
			}
		}
		if err = rs.Build(ctx); err != nil {
			if rs.Collision() {
				log.Info("Building recsplit. Collision happened. It's ok. Restarting...")
				rs.ResetNextSalt()
			} else {
				return HistoryFiles{}, fmt.Errorf("build idx: %w", err)
			}
		} else {
			break
		}
	}
	rs.Close()
	rs = nil
	if historyIdx, err = recsplit.OpenIndex(historyIdxPath); err != nil {
		return HistoryFiles{}, fmt.Errorf("open idx: %w", err)
	}
	closeComp = false
	return HistoryFiles{
		historyDecomp:   historyDecomp,
		historyIdx:      historyIdx,
		efHistoryDecomp: efHistoryDecomp,
		efHistoryIdx:    efHistoryIdx,
	}, nil
}

func (h *History) integrateFiles(sf HistoryFiles, txNumFrom, txNumTo uint64) {
	h.InvertedIndex.integrateFiles(InvertedFiles{
		decomp: sf.efHistoryDecomp,
		index:  sf.efHistoryIdx,
	}, txNumFrom, txNumTo)

	fi := newFilesItem(txNumFrom, txNumTo, h.aggregationStep)
	fi.decompressor = sf.historyDecomp
	fi.index = sf.historyIdx
	h.dirtyFiles.Set(fi)

	h.reCalcRoFiles()
}

func (h *History) reCalcRoFiles() {
	roFiles := calcVisibleFiles(h.dirtyFiles)
	h.visibleFiles.Store(&roFiles)
}
