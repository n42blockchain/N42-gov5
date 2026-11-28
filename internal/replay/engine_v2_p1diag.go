// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Per-transaction outcome diagnostics of a profile-1 replay. Every source
// transaction ends in one of three outcomes under the profile-1 rules:
// included and successful, included and failed (status 0, the nonce
// advances), or dropped (the block context refuses it: nonce, funds, gas).
// The diagnostics compare each outcome with the source receipt status and
// split the drops into root failures (the first drop of a sender) and
// cascaded ones (a later drop of a sender that already lost a transaction,
// whose nonce is then out of step).

package replay

import (
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
)

// DiagCounters is a set of named counters, serialized sorted by name.
type DiagCounters struct {
	mu sync.Mutex
	m  map[string]uint64
}

// Add adds n to counter name.
func (d *DiagCounters) Add(name string, n uint64) {
	d.mu.Lock()
	if d.m == nil {
		d.m = make(map[string]uint64)
	}
	d.m[name] += n
	d.mu.Unlock()
}

// Get returns counter name.
func (d *DiagCounters) Get(name string) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m[name]
}

// Snapshot returns a copy of every counter.
func (d *DiagCounters) Snapshot() map[string]uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]uint64, len(d.m))
	for k, v := range d.m {
		out[k] = v
	}
	return out
}

// Names returns the counter names, sorted.
func (d *DiagCounters) Names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	names := make([]string, 0, len(d.m))
	for k := range d.m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// p1TxState is what the diagnostics need of one transaction before it runs.
type p1TxState struct {
	from       types.Address
	known      bool // sender known
	nonceOK    bool // the sender's nonce equals the transaction's
	newAccount bool // a top-level value transfer into an empty account
	shortGas   bool // ... whose gas limit cannot cover intrinsic + new-account state gas
}

// p1NewAccountShort reports whether tx is a top-level value transfer into an
// account that is empty (EIP-161: no nonce, balance or code) and whether its
// gas limit cannot pay the intrinsic cost plus the EIP-8037 new-account state
// gas. Such a transaction halts before its first frame
// (state_transition_amsterdam.go chargeCallRecipient): status 0, the value
// stays with the sender.
func p1NewAccountShort(ibs *state.IntraBlockState, tx *transaction.Transaction, from types.Address) (newAccount, short bool) {
	to := tx.To()
	value := tx.Value()
	if to == nil || value == nil || value.IsZero() || *to == from {
		return false, false
	}
	if !ibs.Empty(*to) {
		return false, false
	}
	intrinsic, err := internal.IntrinsicGas(tx.Data(), tx.AccessList(), tx.AuthList(), false, true, true, true, true, true, true, false)
	if err != nil {
		return true, false
	}
	return true, tx.Gas() < intrinsic+vm2.NewAccountStateGas
}

// p1PreTx inspects tx before it runs.
func (e *EngineV2) p1PreTx(ibs *state.IntraBlockState, tx *transaction.Transaction) p1TxState {
	var s p1TxState
	f := tx.From()
	if f == nil {
		return s
	}
	s.from, s.known = *f, true
	s.nonceOK = ibs.GetNonce(s.from) == tx.Nonce()
	if s.nonceOK {
		s.newAccount, s.shortGas = p1NewAccountShort(ibs, tx, s.from)
	}
	return s
}

// p1PostTx records the outcome of source transaction i: receipt is nil when
// the transaction was dropped with dropErr.
func (e *EngineV2) p1PostTx(i int, tx *transaction.Transaction, pre p1TxState, receipt *block.Receipt, dropErr error) {
	d := &e.stats.P1Diag
	srcStatus, haveSrc := uint64(0), false
	if i < len(e.p1SrcReceipts) && e.p1SrcReceipts[i] != nil {
		srcStatus, haveSrc = e.p1SrcReceipts[i].Status, true
	}
	if !haveSrc {
		d.Add("src_receipt_missing", 1)
	}
	srcOK := srcStatus == block.ReceiptStatusSuccessful

	if receipt == nil {
		d.Add("tx_dropped", 1)
		if haveSrc {
			if srcOK {
				d.Add("dropped_src_ok", 1)
			} else {
				d.Add("dropped_src_failed", 1)
			}
		}
		class := txErrorClass(dropErr)
		if !pre.known {
			d.Add("root_drop_"+class, 1)
			return
		}
		if _, cascaded := e.p1Dropped[pre.from]; cascaded {
			d.Add("cascade_drop_"+class, 1)
			return
		}
		e.p1Dropped[pre.from] = struct{}{}
		d.Add("root_drop_"+class, 1)
		// A sender that was meant to be funded by a transfer that halted on
		// the new-account state gas: the root failure is that transfer.
		if _, unfunded := e.p1Unfunded[pre.from]; unfunded {
			d.Add("root_drop_"+class+"_after_newaccount_halt", 1)
		}
		return
	}

	ok := receipt.Status == block.ReceiptStatusSuccessful
	if ok {
		d.Add("tx_ok", 1)
	} else {
		d.Add("tx_status0", 1)
		if pre.shortGas {
			d.Add("status0_newaccount_state_gas", 1)
			if to := tx.To(); to != nil {
				e.p1Unfunded[*to] = struct{}{}
			}
		} else {
			d.Add("status0_other", 1)
		}
	}
	if haveSrc {
		switch {
		case ok == srcOK:
			d.Add("status_match", 1)
		case srcOK:
			d.Add("status_src_ok_now_failed", 1)
		default:
			d.Add("status_src_failed_now_ok", 1)
		}
	}
}
