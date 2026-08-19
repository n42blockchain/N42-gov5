/*
   Copyright 2021 Erigon contributors

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

package mdbx

import (
	"fmt"
	"runtime"

	"github.com/n42blockchain/N42/lib/kv"
)

func (tx *MdbxTx) IsRo() bool     { return tx.readOnly }
func (tx *MdbxTx) ViewID() uint64 { return tx.tx.ID() }

// cleanup releases all resources held by the transaction.
// It must be called exactly once when a transaction ends (commit or rollback).
func (tx *MdbxTx) cleanup() {
	tx.tx = nil
	tx.db.trackTxEnd()
	if tx.readOnly {
		tx.db.roTxsLimiter.Release(1)
	} else {
		runtime.UnlockOSThread()
	}
	tx.db.leakDetector.Del(tx.id)
}

func (tx *MdbxTx) Commit() error {
	if tx.tx == nil {
		return nil
	}
	defer tx.cleanup()
	tx.closeCursors()
	tx.CollectMetrics()
	tx.logWriteProbe()

	latency, err := tx.tx.Commit()
	if err != nil {
		return fmt.Errorf("label: %s, %w", tx.db.opts.label, err)
	}

	if tx.db.opts.label == kv.ChainDB {
		kv.DbCommitPreparation.Observe(latency.Preparation.Seconds())
		kv.DbCommitWrite.Observe(latency.Write.Seconds())
		kv.DbCommitSync.Observe(latency.Sync.Seconds())
		kv.DbCommitEnding.Observe(latency.Ending.Seconds())
		kv.DbCommitTotal.Observe(latency.Whole.Seconds())
	}

	return nil
}

func (tx *MdbxTx) Rollback() {
	if tx.tx == nil {
		return
	}
	defer tx.cleanup()
	tx.closeCursors()
	tx.tx.Abort()
}

func (tx *MdbxTx) SpaceDirty() (uint64, uint64, error) {
	txInfo, err := tx.tx.Info(true)
	if err != nil {
		return 0, 0, err
	}

	return txInfo.SpaceDirty, tx.db.txSize, nil
}

func (tx *MdbxTx) PrintDebugInfo() {
	// no-op: reserved for future debug instrumentation
}

func (tx *MdbxTx) closeCursors() {
	for _, c := range tx.cursors {
		if c != nil {
			c.Close()
		}
	}
	tx.cursors = nil
	for _, c := range tx.streams {
		if c != nil {
			c.Close()
		}
	}
	tx.streams = nil
	tx.statelessCursors = nil
}
