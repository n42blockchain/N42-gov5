// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Prelude blocks of a profile-1 replay (owner decision 2026-11-28). The
// genesis alloc repair (engine_v2_p1repair.go) is delivered by balances
// only, and the genesis stays lean: instead of one alloc entry per repaired
// address, the genesis holds ONE funder account, and ordinary signed
// transfers from it, in "prelude" blocks right after the genesis and before
// the first replayed source block, create and fund every delta address with
// exactly its delta amount. The delta addresses end up with the balances
// the genesis-alloc variant gave them.
//
// The transfers are deterministic: sorted by address, nonces 0..n-1, type-2
// dynamic-fee transactions with a zero tip (no coinbase credit), a gas limit
// covering the intrinsic cost plus the EIP-8037 new-account state gas, and
// a fee cap of twice the genesis base fee. Blocks are packed up to the gas
// target (gasLimit / elasticity) by summed gas limits, so the derived base
// fee never rises during the prelude and the fee cap holds.

package replay

import (
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// p1PreludeKey (QMDBMeta) records the prelude a target was built with:
// blocks(8) || txs(8) || digest(32). A resumed run checks it.
var p1PreludeKey = []byte("replay_p1_prelude")

// P1PreludeTransfer is one prelude credit.
type P1PreludeTransfer struct {
	To  types.Address
	Wei uint256.Int
}

// P1PreludeTransfers sums the alloc deltas per address and returns the
// non-zero credits sorted by address.
func P1PreludeTransfers(deltas ...*P1AllocDelta) []P1PreludeTransfer {
	sum := make(map[types.Address]*uint256.Int)
	for _, d := range deltas {
		for a, e := range d.Entries {
			s := sum[a]
			if s == nil {
				s = new(uint256.Int)
				sum[a] = s
			}
			s.Add(s, &e.Wei)
		}
	}
	out := make([]P1PreludeTransfer, 0, len(sum))
	for a, w := range sum {
		if !w.IsZero() {
			out = append(out, P1PreludeTransfer{To: a, Wei: *w})
		}
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i].To[:]) < string(out[j].To[:]) })
	return out
}

// P1PreludeGasPerTx is the gas limit of a prelude transfer: the intrinsic
// cost of a value transfer under Amsterdam plus the new-account state gas,
// so a transfer into an empty account does not halt.
func P1PreludeGasPerTx() uint64 {
	intrinsic, err := internal.IntrinsicGas(nil, nil, nil, false, true, true, true, true, true, true, false)
	if err != nil {
		panic(err) // a plain transfer always has an intrinsic cost
	}
	return intrinsic + vm2.NewAccountStateGas
}

// p1PreludeDefaultBaseFee stands in for a genesis without a base fee.
const p1PreludeDefaultBaseFee = 1_000_000_000

// P1PreludeMaxFee is the fee cap of a prelude transfer: twice the genesis
// base fee. Prelude blocks stay at or below the gas target, so the derived
// base fee never exceeds the genesis one.
func P1PreludeMaxFee(genesisBaseFee *uint256.Int) *uint256.Int {
	bf := uint256.NewInt(p1PreludeDefaultBaseFee)
	if genesisBaseFee != nil && !genesisBaseFee.IsZero() {
		bf = genesisBaseFee.Clone()
	}
	return bf.Lsh(bf, 1)
}

// P1PreludeFunderBalance is the genesis balance of the funder: every
// transfer's value plus a gas budget of n * gasPerTx * maxFee with a 10%
// margin (the upfront cost check reserves gas * maxFee per transfer).
func P1PreludeFunderBalance(transfers []P1PreludeTransfer, gasPerTx uint64, maxFee *uint256.Int) *uint256.Int {
	total := new(uint256.Int)
	for i := range transfers {
		total.Add(total, &transfers[i].Wei)
	}
	gas := new(uint256.Int).Mul(uint256.NewInt(uint64(len(transfers))), uint256.NewInt(gasPerTx))
	gas.Mul(gas, maxFee)
	gas.Add(gas, new(uint256.Int).Div(gas, uint256.NewInt(10)))
	return total.Add(total, gas)
}

// P1PreludeTxsPerBlock is how many prelude transfers one block takes: the
// gas target over the per-transfer gas limit.
func P1PreludeTxsPerBlock(gasLimit, gasPerTx uint64) int {
	return int(gasLimit / params.ElasticityMultiplier / gasPerTx)
}

// BuildP1PreludeTxs signs the prelude transfers from key: nonce i for
// transfers[i], chain id chainID.
func BuildP1PreludeTxs(chainID *big.Int, key *ecdsa.PrivateKey, transfers []P1PreludeTransfer, gasPerTx uint64, maxFee *uint256.Int) ([]*transaction.Transaction, error) {
	funder := crypto.PubkeyToAddress(key.PublicKey)
	signer := transaction.LatestSignerForChainID(chainID)
	cid, overflow := uint256.FromBig(chainID)
	if overflow {
		return nil, errors.New("prelude: chain id overflow")
	}
	txs := make([]*transaction.Transaction, 0, len(transfers))
	for i := range transfers {
		to := transfers[i].To
		if to == funder {
			return nil, fmt.Errorf("prelude: the funder %s is a delta address", funder.Hex())
		}
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   cid.Clone(),
			Nonce:     uint64(i),
			GasTipCap: new(uint256.Int),
			GasFeeCap: maxFee.Clone(),
			Gas:       gasPerTx,
			To:        &to,
			Value:     transfers[i].Wei.Clone(),
		}), signer, key)
		if err != nil {
			return nil, fmt.Errorf("prelude: sign transfer %d: %w", i, err)
		}
		txs = append(txs, tx)
	}
	return txs, nil
}

// PackP1Prelude splits txs into blocks of at most perBlock, in order.
func PackP1Prelude(txs []*transaction.Transaction, perBlock int) [][]*transaction.Transaction {
	if perBlock <= 0 {
		perBlock = 1
	}
	var out [][]*transaction.Transaction
	for len(txs) > 0 {
		n := min(perBlock, len(txs))
		out = append(out, txs[:n:n])
		txs = txs[n:]
	}
	return out
}

// P1PreludeDigest binds the funder, the gas parameters and the transfer
// list; a resumed run compares it with the one the target recorded.
func P1PreludeDigest(funder types.Address, gasPerTx uint64, maxFee *uint256.Int, transfers []P1PreludeTransfer) types.Hash {
	buf := make([]byte, 0, 20+8+32+len(transfers)*52)
	buf = append(buf, funder[:]...)
	buf = binary.BigEndian.AppendUint64(buf, gasPerTx)
	mf := maxFee.Bytes32()
	buf = append(buf, mf[:]...)
	for i := range transfers {
		w := transfers[i].Wei.Bytes32()
		buf = append(buf, transfers[i].To[:]...)
		buf = append(buf, w[:]...)
	}
	return crypto.Keccak256Hash(buf)
}

// p1PreludeMarker encodes the target's prelude record.
func p1PreludeMarker(blocks, txs uint64, digest types.Hash) []byte {
	b := make([]byte, 0, 48)
	b = binary.BigEndian.AppendUint64(b, blocks)
	b = binary.BigEndian.AppendUint64(b, txs)
	return append(b, digest[:]...)
}

// p1PreludePlan derives the prelude of this run from the configured deltas
// and funder key; nil when the run has no prelude.
func (e *EngineV2) p1PreludePlan(genesisBaseFee *uint256.Int) (txs []*transaction.Transaction, digest types.Hash, err error) {
	if e.cfg.Profile1PreludeKey == nil {
		return nil, types.Hash{}, nil
	}
	transfers := P1PreludeTransfers(e.cfg.Profile1PreludeDeltas...)
	gas := P1PreludeGasPerTx()
	maxFee := P1PreludeMaxFee(genesisBaseFee)
	funder := crypto.PubkeyToAddress(e.cfg.Profile1PreludeKey.PublicKey)
	digest = P1PreludeDigest(funder, gas, maxFee, transfers)
	txs, err = BuildP1PreludeTxs(e.cfg.ChainConfig.ChainID, e.cfg.Profile1PreludeKey, transfers, gas, maxFee)
	return txs, digest, err
}

// p1PreludeCheckResume refuses to continue a target whose prelude record
// differs from this run's (a target without prelude resumed with one, or
// the other way round, or other deltas or another funder).
func (e *EngineV2) p1PreludeCheckResume(tx kv.Tx, genesisBaseFee *uint256.Int) error {
	if e.p1PreludeChecked {
		return nil
	}
	e.p1PreludeChecked = true
	have, _ := tx.GetOne(modules.QMDBMeta, p1PreludeKey)
	if e.cfg.Profile1PreludeKey == nil {
		if len(have) > 0 {
			return errors.New("replay profile 1: the target was built with prelude blocks; pass the same --p1-prelude and --p1-prelude-funder-key")
		}
		return nil
	}
	if len(have) != 48 {
		return errors.New("replay profile 1: --p1-prelude given but the target has no prelude blocks")
	}
	transfers := P1PreludeTransfers(e.cfg.Profile1PreludeDeltas...)
	funder := crypto.PubkeyToAddress(e.cfg.Profile1PreludeKey.PublicKey)
	digest := P1PreludeDigest(funder, P1PreludeGasPerTx(), P1PreludeMaxFee(genesisBaseFee), transfers)
	if types.BytesToHash(have[16:]) != digest {
		return errors.New("replay profile 1: the target's prelude differs from --p1-prelude / --p1-prelude-funder-key")
	}
	return nil
}

// p1Prelude writes the prelude blocks of a fresh target on the genesis
// (parentHash) and returns the hash of the last one and the next block
// number. Without a prelude it returns (parentHash, 1). The blocks are
// sealed like any replayed block (P1Sealer: header, CommitQC, committee
// evidence, slot-grid time one slot after the parent).
func (e *EngineV2) p1Prelude(dstTx kv.RwTx, histAgg *state.HistoryAggregator, qmdbRC *commitment.QMDBRootComputer,
	genesisBaseFee *uint256.Int, parentHash types.Hash) (types.Hash, uint64, error) {

	e.p1PreludeChecked = true
	txs, digest, err := e.p1PreludePlan(genesisBaseFee)
	if err != nil || txs == nil {
		return parentHash, 1, err
	}
	blocks := PackP1Prelude(txs, P1PreludeTxsPerBlock(e.cfg.ChainConfig.HotStuff.GasLimit, P1PreludeGasPerTx()))
	number := uint64(1)
	defer func() { e.p1PreludeBlock = nil }()
	for _, btxs := range blocks {
		e.p1PreludeBlock = btxs
		blk, err := e.p1Block(dstTx, histAgg, qmdbRC, number, parentHash, nil, 0)
		if err != nil {
			return parentHash, number, err
		}
		parentHash = blk.Hash()
		number++
	}
	if err := dstTx.Put(modules.QMDBMeta, p1PreludeKey, p1PreludeMarker(uint64(len(blocks)), uint64(len(txs)), digest)); err != nil {
		return parentHash, number, fmt.Errorf("write prelude marker: %w", err)
	}
	e.stats.P1Diag.Add("prelude_blocks", uint64(len(blocks)))
	e.stats.P1Diag.Add("prelude_txs", uint64(len(txs)))
	e.log.Info("profile-1 prelude written", "blocks", len(blocks), "transfers", len(txs),
		"funder", crypto.PubkeyToAddress(e.cfg.Profile1PreludeKey.PublicKey).Hex())
	return parentHash, number, nil
}
