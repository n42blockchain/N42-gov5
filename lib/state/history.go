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
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/n42blockchain/N42/lib/log/v3"
	btree2 "github.com/tidwall/btree"

	"github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/seg"
)

type History struct {
	*InvertedIndex

	// Files:
	//  .v - list of values
	//  .vi - txNum+key -> offset in .v
	dirtyFiles *btree2.BTreeG[*filesItem] // thread-safe, but maybe need 1 RWLock for all trees in Aggregator

	// roFiles derivative from field `file`, but without garbage (canDelete=true, overlaps, etc...)
	// BeginFilesRo() using this field in zero-copy way
	visibleFiles atomic.Pointer[[]ctxItem]

	historyValsTable        string // key1+key2+txnNum -> oldValue , stores values BEFORE change
	compressWorkers         int
	compressVals            bool
	integrityFileExtensions []string

	// not large:
	//   keys: txNum -> key1+key2
	//   vals: key1+key2 -> txNum + value (DupSort)
	// large:
	//   keys: txNum -> key1+key2
	//   vals: key1+key2+txNum -> value (not DupSort)
	largeValues bool // can't use DupSort optimization (aka. prefix-compression) if values size > 4kb

	garbageFiles []*filesItem // files that exist on disk, but ignored on opening folder - because they are garbage

	wal    *historyWAL
	logger log.Logger
}

func NewHistory(dir, tmpdir string, aggregationStep uint64,
	filenameBase, indexKeysTable, indexTable, historyValsTable string,
	compressVals bool, integrityFileExtensions []string, largeValues bool, logger log.Logger) (*History, error) {
	h := History{
		dirtyFiles:              btree2.NewBTreeGOptions[*filesItem](filesItemLess, btree2.Options{Degree: 128, NoLocks: false}),
		historyValsTable:        historyValsTable,
		compressVals:            compressVals,
		compressWorkers:         1,
		integrityFileExtensions: integrityFileExtensions,
		largeValues:             largeValues,
		logger:                  logger,
	}
	h.visibleFiles.Store(&[]ctxItem{})
	var err error
	h.InvertedIndex, err = NewInvertedIndex(dir, tmpdir, aggregationStep, filenameBase, indexKeysTable, indexTable, true, append(slices.Clone(h.integrityFileExtensions), "v"), logger)
	if err != nil {
		return nil, fmt.Errorf("NewHistory: %s, %w", filenameBase, err)
	}

	return &h, nil
}

// OpenList - main method to open list of files.
// It's ok if some files was open earlier.
// If some file already open: noop.
// If some file already open but not in provided list: close and remove from `files` field.
func (h *History) OpenList(fNames []string) error {
	if err := h.InvertedIndex.OpenList(fNames); err != nil {
		return err
	}
	return h.openList(fNames)
}

func (h *History) openList(fNames []string) error {
	h.closeWhatNotInList(fNames)
	h.garbageFiles = h.scanStateFiles(fNames)
	if err := h.openFiles(); err != nil {
		return fmt.Errorf("History.OpenList: %w, %s", err, h.filenameBase)
	}
	return nil
}

func (h *History) OpenFolder() error {
	files, err := h.fileNamesOnDisk()
	if err != nil {
		return err
	}
	return h.OpenList(files)
}

// scanStateFiles
// returns `uselessFiles` where file "is useless" means: it's subset of frozen file. such files can be safely deleted. subset of non-frozen file may be useful
func (h *History) scanStateFiles(fNames []string) (garbageFiles []*filesItem) {
	re := regexp.MustCompile("^" + h.filenameBase + ".([0-9]+)-([0-9]+).v$")
	var err error
Loop:
	for _, name := range fNames {
		subs := re.FindStringSubmatch(name)
		if len(subs) != 3 {
			if len(subs) != 0 {
				h.logger.Warn("[snapshots] file ignored by inverted index scan, more than 3 submatches", "name", name, "submatches", len(subs))
			}
			continue
		}
		var startStep, endStep uint64
		if startStep, err = strconv.ParseUint(subs[1], 10, 64); err != nil {
			h.logger.Warn("[snapshots] file ignored by inverted index scan, parsing startTxNum", "error", err, "name", name)
			continue
		}
		if endStep, err = strconv.ParseUint(subs[2], 10, 64); err != nil {
			h.logger.Warn("[snapshots] file ignored by inverted index scan, parsing endTxNum", "error", err, "name", name)
			continue
		}
		if startStep > endStep {
			h.logger.Warn("[snapshots] file ignored by inverted index scan, startTxNum > endTxNum", "name", name)
			continue
		}

		startTxNum, endTxNum := startStep*h.aggregationStep, endStep*h.aggregationStep
		var newFile = newFilesItem(startTxNum, endTxNum, h.aggregationStep)

		for _, ext := range h.integrityFileExtensions {
			requiredFile := fmt.Sprintf("%s.%d-%d.%s", h.filenameBase, startStep, endStep, ext)
			if !dir.FileExist(filepath.Join(h.dir, requiredFile)) {
				h.logger.Debug(fmt.Sprintf("[snapshots] skip %s because %s doesn't exists", name, requiredFile))
				garbageFiles = append(garbageFiles, newFile)
				continue Loop
			}
		}

		if _, has := h.dirtyFiles.Get(newFile); has {
			continue
		}

		addNewFile := true
		var subSets []*filesItem
		h.dirtyFiles.Walk(func(items []*filesItem) bool {
			for _, item := range items {
				if item.isSubsetOf(newFile) {
					subSets = append(subSets, item)
					continue
				}

				if newFile.isSubsetOf(item) {
					if item.frozen {
						addNewFile = false
						garbageFiles = append(garbageFiles, newFile)
					}
					continue
				}
			}
			return true
		})
		if addNewFile {
			h.dirtyFiles.Set(newFile)
		}
	}
	return garbageFiles
}

func (h *History) openFiles() error {
	var totalKeys uint64
	var err error
	invalidFileItems := make([]*filesItem, 0)
	h.dirtyFiles.Walk(func(items []*filesItem) bool {
		for _, item := range items {
			if item.decompressor != nil {
				continue
			}
			fromStep, toStep := item.startTxNum/h.aggregationStep, item.endTxNum/h.aggregationStep
			datPath := filepath.Join(h.dir, fmt.Sprintf("%s.%d-%d.v", h.filenameBase, fromStep, toStep))
			if !dir.FileExist(datPath) {
				invalidFileItems = append(invalidFileItems, item)
				continue
			}
			if item.decompressor, err = seg.NewDecompressor(datPath); err != nil {
				h.logger.Debug("History.openFiles:", "err", err, "file", datPath)
				if errors.Is(err, &seg.ErrCompressedFileCorrupted{}) {
					err = nil
					continue
				}
				return false
			}

			if item.index != nil {
				continue
			}
			idxPath := filepath.Join(h.dir, fmt.Sprintf("%s.%d-%d.vi", h.filenameBase, fromStep, toStep))
			if dir.FileExist(idxPath) {
				if item.index, err = recsplit.OpenIndex(idxPath); err != nil {
					h.logger.Debug("History.openFiles:", "err", err, "file", idxPath)
					return false
				}
				totalKeys += item.index.KeyCount()
			}
		}
		return true
	})
	if err != nil {
		return err
	}
	for _, item := range invalidFileItems {
		h.dirtyFiles.Delete(item)
	}

	h.reCalcRoFiles()
	return nil
}

func (h *History) closeWhatNotInList(fNames []string) {
	var toDelete []*filesItem
	h.dirtyFiles.Walk(func(items []*filesItem) bool {
	Loop1:
		for _, item := range items {
			for _, protectName := range fNames {
				if item.decompressor != nil && item.decompressor.FileName() == protectName {
					continue Loop1
				}
			}
			toDelete = append(toDelete, item)
		}
		return true
	})
	for _, item := range toDelete {
		if item.decompressor != nil {
			item.decompressor.Close()
			item.decompressor = nil
		}
		if item.index != nil {
			item.index.Close()
			item.index = nil
		}
		h.dirtyFiles.Delete(item)
	}
}

func (h *History) Close() {
	h.InvertedIndex.Close()
	h.closeWhatNotInList([]string{})
	h.reCalcRoFiles()
}

func (h *History) Files() (res []string) {
	h.dirtyFiles.Walk(func(items []*filesItem) bool {
		for _, item := range items {
			if item.decompressor != nil {
				res = append(res, item.decompressor.FileName())
			}
		}
		return true
	})
	res = append(res, h.InvertedIndex.Files()...)
	return res
}
