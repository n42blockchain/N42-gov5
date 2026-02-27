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
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/order"
	"golang.org/x/sync/errgroup"
)

type AggregatorRoTx struct {
	a          *Aggregator
	accounts   *HistoryRoTx
	storage    *HistoryRoTx
	code       *HistoryRoTx
	logAddrs   *InvertedIndexRoTx
	logTopics  *InvertedIndexRoTx
	tracesFrom *InvertedIndexRoTx
	tracesTo   *InvertedIndexRoTx
	keyBuf     []byte

	id uint64 // set only if TRACE_AGG=true
}

func (a *Aggregator) BeginFilesRo() *AggregatorRoTx {
	ac := &AggregatorRoTx{
		a:          a,
		accounts:   a.accounts.BeginFilesRo(),
		storage:    a.storage.BeginFilesRo(),
		code:       a.code.BeginFilesRo(),
		logAddrs:   a.logAddrs.BeginFilesRo(),
		logTopics:  a.logTopics.BeginFilesRo(),
		tracesFrom: a.tracesFrom.BeginFilesRo(),
		tracesTo:   a.tracesTo.BeginFilesRo(),

		id: a.leakDetector.Add(),
	}

	return ac
}
func (ac *AggregatorRoTx) Close() {
	ac.a.leakDetector.Del(ac.id)
	ac.accounts.Close()
	ac.storage.Close()
	ac.code.Close()
	ac.logAddrs.Close()
	ac.logTopics.Close()
	ac.tracesFrom.Close()
	ac.tracesTo.Close()
}

func (ac *AggregatorRoTx) BuildOptionalMissedIndices(ctx context.Context, workers int) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	if ac.accounts != nil {
		g.Go(func() error { return ac.accounts.BuildOptionalMissedIndices(ctx) })
	}
	if ac.storage != nil {
		g.Go(func() error { return ac.storage.BuildOptionalMissedIndices(ctx) })
	}
	if ac.code != nil {
		g.Go(func() error { return ac.code.BuildOptionalMissedIndices(ctx) })
	}
	return g.Wait()
}

func (ac *AggregatorRoTx) findMergeRange(maxEndTxNum, maxSpan uint64) RangesV3 {
	var r RangesV3
	r.accounts = ac.a.accounts.findMergeRange(maxEndTxNum, maxSpan)
	r.storage = ac.a.storage.findMergeRange(maxEndTxNum, maxSpan)
	r.code = ac.a.code.findMergeRange(maxEndTxNum, maxSpan)
	r.logAddrs, r.logAddrsStartTxNum, r.logAddrsEndTxNum = ac.a.logAddrs.findMergeRange(maxEndTxNum, maxSpan)
	r.logTopics, r.logTopicsStartTxNum, r.logTopicsEndTxNum = ac.a.logTopics.findMergeRange(maxEndTxNum, maxSpan)
	r.tracesFrom, r.tracesFromStartTxNum, r.tracesFromEndTxNum = ac.a.tracesFrom.findMergeRange(maxEndTxNum, maxSpan)
	r.tracesTo, r.tracesToStartTxNum, r.tracesToEndTxNum = ac.a.tracesTo.findMergeRange(maxEndTxNum, maxSpan)
	return r
}

func (ac *AggregatorRoTx) staticFilesInRange(r RangesV3) (sf SelectedStaticFilesV3, err error) {
	if r.accounts.any() {
		sf.accountsIdx, sf.accountsHist, sf.accountsI, err = ac.accounts.staticFilesInRange(r.accounts)
		if err != nil {
			return sf, err
		}
	}
	if r.storage.any() {
		sf.storageIdx, sf.storageHist, sf.storageI, err = ac.storage.staticFilesInRange(r.storage)
		if err != nil {
			return sf, err
		}
	}
	if r.code.any() {
		sf.codeIdx, sf.codeHist, sf.codeI, err = ac.code.staticFilesInRange(r.code)
		if err != nil {
			return sf, err
		}
	}
	if r.logAddrs {
		sf.logAddrs, sf.logAddrsI = ac.logAddrs.staticFilesInRange(r.logAddrsStartTxNum, r.logAddrsEndTxNum)
	}
	if r.logTopics {
		sf.logTopics, sf.logTopicsI = ac.logTopics.staticFilesInRange(r.logTopicsStartTxNum, r.logTopicsEndTxNum)
	}
	if r.tracesFrom {
		sf.tracesFrom, sf.tracesFromI = ac.tracesFrom.staticFilesInRange(r.tracesFromStartTxNum, r.tracesFromEndTxNum)
	}
	if r.tracesTo {
		sf.tracesTo, sf.tracesToI = ac.tracesTo.staticFilesInRange(r.tracesToStartTxNum, r.tracesToEndTxNum)
	}
	return sf, nil
}

func (ac *AggregatorRoTx) mergeFiles(ctx context.Context, files SelectedStaticFilesV3, r RangesV3, workers int) (MergedFilesV3, error) {
	var mf MergedFilesV3
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	closeFiles := true
	defer func() {
		if closeFiles {
			mf.Close()
		}
	}()
	if r.accounts.any() {
		g.Go(func() error {
			var err error
			mf.accountsIdx, mf.accountsHist, err = ac.a.accounts.mergeFiles(ctx, files.accountsIdx, files.accountsHist, r.accounts, workers, ac.a.ps)
			return err
		})
	}

	if r.storage.any() {
		g.Go(func() error {
			var err error
			mf.storageIdx, mf.storageHist, err = ac.a.storage.mergeFiles(ctx, files.storageIdx, files.storageHist, r.storage, workers, ac.a.ps)
			return err
		})
	}
	if r.code.any() {
		g.Go(func() error {
			var err error
			mf.codeIdx, mf.codeHist, err = ac.a.code.mergeFiles(ctx, files.codeIdx, files.codeHist, r.code, workers, ac.a.ps)
			return err
		})
	}
	if r.logAddrs {
		g.Go(func() error {
			var err error
			mf.logAddrs, err = ac.a.logAddrs.mergeFiles(ctx, files.logAddrs, r.logAddrsStartTxNum, r.logAddrsEndTxNum, workers, ac.a.ps)
			return err
		})
	}
	if r.logTopics {
		g.Go(func() error {
			var err error
			mf.logTopics, err = ac.a.logTopics.mergeFiles(ctx, files.logTopics, r.logTopicsStartTxNum, r.logTopicsEndTxNum, workers, ac.a.ps)
			return err
		})
	}
	if r.tracesFrom {
		g.Go(func() error {
			var err error
			mf.tracesFrom, err = ac.a.tracesFrom.mergeFiles(ctx, files.tracesFrom, r.tracesFromStartTxNum, r.tracesFromEndTxNum, workers, ac.a.ps)
			return err
		})
	}
	if r.tracesTo {
		g.Go(func() error {
			var err error
			mf.tracesTo, err = ac.a.tracesTo.mergeFiles(ctx, files.tracesTo, r.tracesToStartTxNum, r.tracesToEndTxNum, workers, ac.a.ps)
			return err
		})
	}
	err := g.Wait()
	if err == nil {
		closeFiles = false
	}
	return mf, err
}

func (ac *AggregatorRoTx) IndexRange(name kv.InvertedIdx, k []byte, fromTs, toTs int, asc order.By, limit int, tx kv.Tx) (timestamps iter.U64, err error) {
	switch name {
	case kv.AccountsHistoryIdx:
		return ac.accounts.IdxRange(k, fromTs, toTs, asc, limit, tx)
	case kv.StorageHistoryIdx:
		return ac.storage.IdxRange(k, fromTs, toTs, asc, limit, tx)
	case kv.CodeHistoryIdx:
		return ac.code.IdxRange(k, fromTs, toTs, asc, limit, tx)
	case kv.LogTopicIdx:
		return ac.logTopics.IdxRange(k, fromTs, toTs, asc, limit, tx)
	case kv.LogAddrIdx:
		return ac.logAddrs.IdxRange(k, fromTs, toTs, asc, limit, tx)
	case kv.TracesFromIdx:
		return ac.tracesFrom.IdxRange(k, fromTs, toTs, asc, limit, tx)
	case kv.TracesToIdx:
		return ac.tracesTo.IdxRange(k, fromTs, toTs, asc, limit, tx)
	default:
		return nil, fmt.Errorf("unexpected history name: %s", name)
	}
}

// -- range end

func (ac *AggregatorRoTx) ReadAccountDataNoStateWithRecent(addr []byte, txNum uint64, tx kv.Tx) ([]byte, bool, error) {
	return ac.accounts.GetNoStateWithRecent(addr, txNum, tx)
}

func (ac *AggregatorRoTx) ReadAccountDataNoState(addr []byte, txNum uint64) ([]byte, bool, error) {
	return ac.accounts.GetNoState(addr, txNum)
}

func (ac *AggregatorRoTx) ReadAccountStorageNoStateWithRecent(addr []byte, loc []byte, txNum uint64, tx kv.Tx) ([]byte, bool, error) {
	if cap(ac.keyBuf) < len(addr)+len(loc) {
		ac.keyBuf = make([]byte, len(addr)+len(loc))
	} else if len(ac.keyBuf) != len(addr)+len(loc) {
		ac.keyBuf = ac.keyBuf[:len(addr)+len(loc)]
	}
	copy(ac.keyBuf, addr)
	copy(ac.keyBuf[len(addr):], loc)
	return ac.storage.GetNoStateWithRecent(ac.keyBuf, txNum, tx)
}
func (ac *AggregatorRoTx) ReadAccountStorageNoStateWithRecent2(key []byte, txNum uint64, tx kv.Tx) ([]byte, bool, error) {
	return ac.storage.GetNoStateWithRecent(key, txNum, tx)
}

func (ac *AggregatorRoTx) ReadAccountStorageNoState(addr []byte, loc []byte, txNum uint64) ([]byte, bool, error) {
	if cap(ac.keyBuf) < len(addr)+len(loc) {
		ac.keyBuf = make([]byte, len(addr)+len(loc))
	} else if len(ac.keyBuf) != len(addr)+len(loc) {
		ac.keyBuf = ac.keyBuf[:len(addr)+len(loc)]
	}
	copy(ac.keyBuf, addr)
	copy(ac.keyBuf[len(addr):], loc)
	return ac.storage.GetNoState(ac.keyBuf, txNum)
}

func (ac *AggregatorRoTx) ReadAccountCodeNoStateWithRecent(addr []byte, txNum uint64, tx kv.Tx) ([]byte, bool, error) {
	return ac.code.GetNoStateWithRecent(addr, txNum, tx)
}
func (ac *AggregatorRoTx) ReadAccountCodeNoState(addr []byte, txNum uint64) ([]byte, bool, error) {
	return ac.code.GetNoState(addr, txNum)
}

func (ac *AggregatorRoTx) ReadAccountCodeSizeNoStateWithRecent(addr []byte, txNum uint64, tx kv.Tx) (int, bool, error) {
	code, noState, err := ac.code.GetNoStateWithRecent(addr, txNum, tx)
	if err != nil {
		return 0, false, err
	}
	return len(code), noState, nil
}
func (ac *AggregatorRoTx) ReadAccountCodeSizeNoState(addr []byte, txNum uint64) (int, bool, error) {
	code, noState, err := ac.code.GetNoState(addr, txNum)
	if err != nil {
		return 0, false, err
	}
	return len(code), noState, nil
}

func (ac *AggregatorRoTx) AccountHistoryRange(startTxNum, endTxNum int, asc order.By, limit int, tx kv.Tx) (iter.KV, error) {
	return ac.accounts.HistoryRange(startTxNum, endTxNum, asc, limit, tx)
}

func (ac *AggregatorRoTx) StorageHistoryRange(startTxNum, endTxNum int, asc order.By, limit int, tx kv.Tx) (iter.KV, error) {
	return ac.storage.HistoryRange(startTxNum, endTxNum, asc, limit, tx)
}

func (ac *AggregatorRoTx) CodeHistoryRange(startTxNum, endTxNum int, asc order.By, limit int, tx kv.Tx) (iter.KV, error) {
	return ac.code.HistoryRange(startTxNum, endTxNum, asc, limit, tx)
}

func (ac *AggregatorRoTx) AccountHistoricalStateRange(startTxNum uint64, from, to []byte, limit int, tx kv.Tx) iter.KV {
	return ac.accounts.WalkAsOf(startTxNum, from, to, tx, limit)
}

func (ac *AggregatorRoTx) StorageHistoricalStateRange(startTxNum uint64, from, to []byte, limit int, tx kv.Tx) iter.KV {
	return ac.storage.WalkAsOf(startTxNum, from, to, tx, limit)
}

func (ac *AggregatorRoTx) CodeHistoricalStateRange(startTxNum uint64, from, to []byte, limit int, tx kv.Tx) iter.KV {
	return ac.code.WalkAsOf(startTxNum, from, to, tx, limit)
}

// AggregatorStep is used for incremental reconstitution, it allows
// accessing history in isolated way for each step
type AggregatorStep struct {
	a        *Aggregator
	accounts *HistoryStep
	storage  *HistoryStep
	code     *HistoryStep
	keyBuf   []byte
}

func (a *Aggregator) MakeSteps() ([]*AggregatorStep, error) {
	frozenAndIndexed := a.EndTxNumFrozenAndIndexed()
	accountSteps := a.accounts.MakeSteps(frozenAndIndexed)
	codeSteps := a.code.MakeSteps(frozenAndIndexed)
	storageSteps := a.storage.MakeSteps(frozenAndIndexed)
	if len(accountSteps) != len(storageSteps) || len(storageSteps) != len(codeSteps) {
		return nil, fmt.Errorf("different limit of steps (try merge snapshots): accountSteps=%d, storageSteps=%d, codeSteps=%d", len(accountSteps), len(storageSteps), len(codeSteps))
	}
	steps := make([]*AggregatorStep, len(accountSteps))
	for i, accountStep := range accountSteps {
		steps[i] = &AggregatorStep{
			a:        a,
			accounts: accountStep,
			storage:  storageSteps[i],
			code:     codeSteps[i],
		}
	}
	return steps, nil
}

func (as *AggregatorStep) TxNumRange() (uint64, uint64) {
	return as.accounts.indexFile.startTxNum, as.accounts.indexFile.endTxNum
}

func (as *AggregatorStep) IterateAccountsTxs() *ScanIteratorInc {
	return as.accounts.iterateTxs()
}

func (as *AggregatorStep) IterateStorageTxs() *ScanIteratorInc {
	return as.storage.iterateTxs()
}

func (as *AggregatorStep) IterateCodeTxs() *ScanIteratorInc {
	return as.code.iterateTxs()
}

func (as *AggregatorStep) ReadAccountDataNoState(addr []byte, txNum uint64) ([]byte, bool, uint64) {
	return as.accounts.GetNoState(addr, txNum)
}

func (as *AggregatorStep) ReadAccountStorageNoState(addr []byte, loc []byte, txNum uint64) ([]byte, bool, uint64) {
	if cap(as.keyBuf) < len(addr)+len(loc) {
		as.keyBuf = make([]byte, len(addr)+len(loc))
	} else if len(as.keyBuf) != len(addr)+len(loc) {
		as.keyBuf = as.keyBuf[:len(addr)+len(loc)]
	}
	copy(as.keyBuf, addr)
	copy(as.keyBuf[len(addr):], loc)
	return as.storage.GetNoState(as.keyBuf, txNum)
}

func (as *AggregatorStep) ReadAccountCodeNoState(addr []byte, txNum uint64) ([]byte, bool, uint64) {
	return as.code.GetNoState(addr, txNum)
}

func (as *AggregatorStep) ReadAccountCodeSizeNoState(addr []byte, txNum uint64) (int, bool, uint64) {
	code, noState, stateTxNum := as.code.GetNoState(addr, txNum)
	return len(code), noState, stateTxNum
}

func (as *AggregatorStep) MaxTxNumAccounts(addr []byte) (bool, uint64) {
	return as.accounts.MaxTxNum(addr)
}

func (as *AggregatorStep) MaxTxNumStorage(addr []byte, loc []byte) (bool, uint64) {
	if cap(as.keyBuf) < len(addr)+len(loc) {
		as.keyBuf = make([]byte, len(addr)+len(loc))
	} else if len(as.keyBuf) != len(addr)+len(loc) {
		as.keyBuf = as.keyBuf[:len(addr)+len(loc)]
	}
	copy(as.keyBuf, addr)
	copy(as.keyBuf[len(addr):], loc)
	return as.storage.MaxTxNum(as.keyBuf)
}

func (as *AggregatorStep) MaxTxNumCode(addr []byte) (bool, uint64) {
	return as.code.MaxTxNum(addr)
}

func (as *AggregatorStep) IterateAccountsHistory(txNum uint64) *HistoryIteratorInc {
	return as.accounts.interateHistoryBeforeTxNum(txNum)
}

func (as *AggregatorStep) IterateStorageHistory(txNum uint64) *HistoryIteratorInc {
	return as.storage.interateHistoryBeforeTxNum(txNum)
}

func (as *AggregatorStep) IterateCodeHistory(txNum uint64) *HistoryIteratorInc {
	return as.code.interateHistoryBeforeTxNum(txNum)
}

func (as *AggregatorStep) Clone() *AggregatorStep {
	return &AggregatorStep{
		a:        as.a,
		accounts: as.accounts.Clone(),
		storage:  as.storage.Clone(),
		code:     as.code.Clone(),
	}
}
