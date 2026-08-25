// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// StateProcessor: canonical block-execution entry point. Iterates a
// block's transactions, applies each one via ApplyTransaction
// against the supplied IntraBlockState, updates the cumulative gas
// used and returns the resulting receipts, logs and bloom filter
// used by the block validator.

package internal

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/metrics"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/ethdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	"github.com/holiman/uint256"
)

// ProcessPhases is a per-block breakdown of StateProcessor.Process, recorded
// for the import-path "blockimport phases" line. OBSERVABILITY ONLY — nothing
// here feeds execution or consensus.
//
// The split that matters is Exec vs Finalize: Finalize is where the engine
// computes state root #3 (engine.Finalize -> ibs.IntermediateRoot -> the live
// commitment tree), which on the block-production side is billed to the miner's
// separate "finalize" counter rather than to fillTx. Without the split, a
// follower's whole Process reads as "EVM execution" and looks arbitrarily
// slower than the leader's fillTx for the very same transactions.
type ProcessPhases struct {
	Recover  time.Duration // parallel sender recovery (see recoverBlockSenders)
	Prep     time.Duration // start-of-block system calls (EIP-4788 / EIP-2935)
	Exec     time.Duration // pure EVM: the per-transaction ApplyTransaction loop
	Finalize time.Duration // engine.Finalize: rewards + state root #3
}

// StateProcessor implements Processor and handles state transitions.
type StateProcessor struct {
	config       *params.ChainConfig
	bc           *BlockChain
	engine       consensus.Engine
	slotRecorder vm2.SlotAccessRecorder

	// lastPhases holds the most recent successful Process breakdown. An atomic
	// pointer (published once per block, never mutated in place) keeps this
	// free of locks and of any effect on execution ordering.
	lastPhases atomic.Pointer[ProcessPhases]
}

// LastPhases returns the phase breakdown of the most recent successful
// Process call, or the zero value if none has completed.
func (p *StateProcessor) LastPhases() ProcessPhases {
	if ph := p.lastPhases.Load(); ph != nil {
		return *ph
	}
	return ProcessPhases{}
}

// SetSlotRecorder enables SLOAD access recording for predictive prefetching.
func (p *StateProcessor) SetSlotRecorder(r vm2.SlotAccessRecorder) {
	p.slotRecorder = r
}

// NewStateProcessor initialises a new StateProcessor.
func NewStateProcessor(config *params.ChainConfig, bc *BlockChain, engine consensus.Engine) *StateProcessor {
	return &StateProcessor{
		config: config,
		bc:     bc,
		engine: engine,
	}
}

// Process processes the state changes according to the Ethereum rules by running
// the transaction messages and applying rewards. Returns receipts, unpaid rewards,
// logs, and total gas used.
// senderSourceTrace (N42_SENDER_TRACE=1) logs, per block, how many senders
// came from pool hints vs the process-wide cache vs a full recovery. Kept
// behind an env gate: it is the only way to tell a cold cache from a cold
// pool when the import recov phase blows up.
var (
	senderSourceTrace = os.Getenv("N42_SENDER_TRACE") == "1"
	lastCacheHits     uint64
	lastCacheMisses   uint64
)

func (p *StateProcessor) Process(b *block.Block, ibs *state.IntraBlockState, stateReader state.StateReader, stateWriter state.WriterWithChangeSets, blockHashFunc func(n uint64) types.Hash) (_ block.Receipts, _ map[types.Address]*uint256.Int, _ []*block.Log, _ uint64, retErr error) {
	processStart := time.Now()
	defer func() {
		// Only record metrics on successful block processing to avoid skewing data.
		if retErr == nil {
			metrics.BlockProcessingSeconds.UpdateDuration(processStart)
			metrics.BlockTxCount.Add(len(b.Transactions()))
		}
	}()
	blockNumber, err := requireBlockNumber(b, "block number unavailable")
	if err != nil {
		return nil, nil, nil, 0, err
	}
	concreteHeader, ok := b.Header().(*block.Header)
	if !ok {
		return nil, nil, nil, 0, fmt.Errorf("Process: invalid header type assertion for block %v", b.Number64())
	}

	usedGas := new(uint64)
	gp := new(common.GasPool)
	gp.AddGas(b.GasLimit())

	var (
		rejectedTxs []*RejectedTx
		includedTxs transaction.Transactions
		receipts    block.Receipts
	)

	chainConfig := p.config
	cfg := vm2.Config{SlotRecorder: p.slotRecorder}

	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(blockNumber.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	// Phase timings (observability only) — see ProcessPhases.
	var phases ProcessPhases
	tPhase := time.Now()

	// Warm every transaction's sender cache on a worker pool before the serial
	// loop below. Transactions decoded from the wire carry no sender, so without
	// this each AsMessage inside ApplyTransaction pays for a secp256k1 recovery
	// on the critical path. Purely a prefetch — see recoverBlockSenders for why
	// it cannot change any execution outcome. The signer is derived exactly as
	// applyTransaction derives it; if the header number is unavailable the whole
	// block is about to fail anyway, so recovery is simply skipped.
	if hdrNum, hdrErr := requireHeaderNumber(concreteHeader, "header number unavailable"); hdrErr == nil {
		signer := transaction.MakeSignerWithTimestamp(chainConfig, hdrNum.ToBig(), concreteHeader.Time)
		// CONSENSUS-SAFETY gate: the native tx codec carries `From` on the
		// wire and TxRoot commits to it, but the tx hash does not bind it to
		// the signature — so an imported block's declared senders must be
		// verified against V/R/S or a byzantine leader could forge them.
		// Reject before any execution touches state.
		if err := verifyBlockSenders(signer, b.Transactions()); err != nil {
			return nil, nil, nil, 0, fmt.Errorf("block %s: %w", concreteHeader.Number.String(), err)
		}
		// First pass: senders the pool already recovered at admission — on a
		// saturated fleet that is nearly every transaction of an imported
		// block, turning a 260 ms parallel recovery into map lookups. Second
		// pass: whatever the pool did not have recovers on the worker pool.
		hintFills := applySenderHints(p.bc.senderHints, signer, b.Transactions())
		recoverBlockSenders(signer, b.Transactions())
		if senderSourceTrace && len(b.Transactions()) > 0 {
			h, m := transaction.SenderCacheStats()
			log.Info("sender source", "txs", len(b.Transactions()), "hintFills", hintFills,
				"cacheHits", h-lastCacheHits, "cacheMisses", m-lastCacheMisses)
			lastCacheHits, lastCacheMisses = h, m
		}
	}
	phases.Recover = time.Since(tPhase)
	tPhase = time.Now()

	if err := ProcessExecutionBlockStart(concreteHeader.ParentBeaconRoot, chainConfig, ibs, concreteHeader, p.engine); err != nil {
		return nil, nil, nil, 0, err
	}
	phases.Prep = time.Since(tPhase)
	tPhase = time.Now()

	noop := state.NewNoopWriter()

	// EIP-7928: when the BAL fork is active, wrap the per-tx finalize writer to
	// harvest each transaction's post-execution access changes, then verify the
	// recomputed block-access-list hash against the header below. The wrapper is a
	// pure observer (delegates to noop), so execution is unchanged; when the fork
	// is off, balCap is nil and there is zero overhead.
	var writer state.StateWriter = noop
	var balCap *BALCapture
	if chainConfig.IsBAL(concreteHeader.Time) {
		balCap = NewBALCapture(noop)
		writer = balCap
	}

	for i, tx := range b.Transactions() {
		ibs.Prepare(tx.Hash(), b.Hash(), i)
		if balCap != nil {
			balCap.BeginTx(tx.Hash())
		}
		receipt, _, err := ApplyTransaction(chainConfig, blockHashFunc, p.engine, nil, gp, ibs, writer, concreteHeader, tx, usedGas, cfg)
		if err != nil {
			if balCap != nil {
				balCap.DiscardTx()
			}
			if !cfg.StatelessExec {
				return nil, nil, nil, 0, fmt.Errorf("could not apply tx %d from block %d [%v]: %w", i, b.Number64(), tx.Hash().String(), err)
			}
			rejectedTxs = append(rejectedTxs, &RejectedTx{i, err.Error()})
		} else {
			if balCap != nil {
				balCap.CommitTx()
			}
			includedTxs = append(includedTxs, tx)
			if !cfg.NoReceipts {
				receipts = append(receipts, receipt)
			}
		}
		// Note: FinalizeTx (called inside applyTransaction) already handles
		// per-tx state finalization including empty account deletion.
		// No need to call SoftFinalise here.
	}

	if !cfg.StatelessExec && *usedGas != concreteHeader.GasUsed {
		return nil, nil, nil, 0, fmt.Errorf("gas used by execution: %d, in header: %d", *usedGas, concreteHeader.GasUsed)
	}

	// EIP-7928 verification gate: the recomputed block-access-list hash must match
	// the one bound into the header by the block producer. Covers the block's
	// transactions (system-call writes are not yet harvested — miner and importer
	// omit them identically, so the binding stays consistent).
	if balCap != nil && !cfg.StatelessExec {
		order := blockTxHashes(b)
		got, err := balCap.HashFor(order)
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("compute block access list hash for block %d: %w", b.Number64(), err)
		}
		want := concreteHeader.BlockAccessListHash
		if want == nil || *want != got {
			return nil, nil, nil, 0, fmt.Errorf("block access list hash mismatch at block %d: header %v, computed %s", b.Number64(), want, got.Hex())
		}
	}
	if _, err := ProcessExecutionBlockEnd(nil, chainConfig, ibs, concreteHeader, p.engine); err != nil {
		return nil, nil, nil, 0, err
	}
	phases.Exec = time.Since(tPhase)
	tPhase = time.Now()

	var nopay map[types.Address]*uint256.Int
	if !cfg.ReadOnly {
		txs := b.Transactions()
		var err error
		_, nopay, err = p.engine.Finalize(p.bc, concreteHeader, ibs, txs, nil)
		if err != nil {
			return nil, nil, nil, 0, err
		}
	}
	phases.Finalize = time.Since(tPhase)
	p.lastPhases.Store(&phases)

	// Suppress unused variable warnings
	_ = rejectedTxs
	_ = includedTxs

	return receipts, nopay, ibs.Logs(), *usedGas, nil
}

// applyTransaction applies a single transaction and returns its receipt.
func applyTransaction(config *params.ChainConfig, engine consensus.Engine, gp *common.GasPool, ibs *state.IntraBlockState, stateWriter state.StateWriter, header *block.Header, tx *transaction.Transaction, usedGas *uint64, evm vm2.VMInterface, cfg vm2.Config) (*block.Receipt, []byte, error) {
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, nil, err
	}
	rules := evm.ChainRules()

	msg, err := tx.AsMessage(transaction.MakeSignerWithTimestamp(config, headerNumber.ToBig(), header.Time), header.BaseFee)
	if err != nil {
		return nil, nil, err
	}
	NormalizeExecutionMessage(&msg, cfg.StatelessExec, engine != nil)

	txContext := NewEVMTxContext(&msg)
	if cfg.TraceJumpDest {
		txContext.TxHash = tx.Hash()
	}

	evm.Reset(txContext, ibs)

	result, err := ApplyMessage(evm, &msg, gp, true, false)
	if err != nil {
		return nil, nil, err
	}
	if err = ibs.FinalizeTx(rules, stateWriter); err != nil {
		return nil, nil, err
	}
	// EIP-7778 (Amsterdam): the block consumes the pre-refund gas; the
	// receipt's per-tx GasUsed below stays the sender-billed amount.
	*usedGas += result.BlockGasUsed

	var receipt *block.Receipt
	if !cfg.NoReceipts {
		receipt = &block.Receipt{Type: tx.Type(), CumulativeGasUsed: *usedGas}
		if result.Failed() {
			receipt.Status = block.ReceiptStatusFailed
		} else {
			receipt.Status = block.ReceiptStatusSuccessful
		}

		receipt.TxHash = tx.Hash()
		receipt.GasUsed = result.UsedGas
		if msg.To() == nil {
			receipt.ContractAddress = crypto.CreateAddress(evm.TxContext().Origin, tx.Nonce())
		}
		receipt.Logs = ibs.GetLogs(tx.Hash())
		receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
		receipt.BlockNumber = header.Number
		receipt.TransactionIndex = uint(ibs.TxIndex())
	}

	return receipt, result.ReturnData, err
}

// ApplyTransaction creates the EVM environment and applies a transaction.
func ApplyTransaction(config *params.ChainConfig, blockHashFunc func(n uint64) types.Hash, engine consensus.Engine, author *types.Address, gp *common.GasPool, ibs *state.IntraBlockState, stateWriter state.StateWriter, header *block.Header, tx *transaction.Transaction, usedGas *uint64, cfg vm2.Config) (*block.Receipt, []byte, error) {
	blockContext := NewEVMBlockContext(header, blockHashFunc, engine, config, author)
	vmenv := vm2.NewEVM(blockContext, evmtypes.TxContext{}, ibs, config, cfg)

	return applyTransaction(config, engine, gp, ibs, stateWriter, header, tx, usedGas, vmenv, cfg)
}

// ApplyTransactionWithEVM applies a transaction reusing a pre-built EVM.
// The EVM's block context is left untouched (callers must build the EVM
// with the correct block context for `header`); only txContext + ibs
// are reset via EVM.Reset before execution. Used by witness-replay's
// hot path to avoid one NewEVM + NewEVMInterpreter alloc per tx.
func ApplyTransactionWithEVM(evm vm2.VMInterface, config *params.ChainConfig, engine consensus.Engine, gp *common.GasPool, ibs *state.IntraBlockState, stateWriter state.StateWriter, header *block.Header, tx *transaction.Transaction, usedGas *uint64, cfg vm2.Config) (*block.Receipt, []byte, error) {
	return applyTransaction(config, engine, gp, ibs, stateWriter, header, tx, usedGas, evm, cfg)
}

// NewStateReaderWriter creates a new state reader and writer pair.
// If cache is non-nil, the reader and writer are wrapped with cache-aware
// decorators to accelerate hot-path reads across blocks.
func NewStateReaderWriter(batch ethdb.Database, tx kv.RwTx, blockNumber uint64, writeChangeSets bool, cache *layered.ShardedCache) (state.StateReader, state.WriterWithChangeSets, error) {
	var stateReader state.StateReader = state.NewPlainStateReader(tx)

	var stateWriter state.WriterWithChangeSets
	if writeChangeSets {
		stateWriter = state.NewPlainStateWriter(batch, tx, blockNumber)
	} else {
		stateWriter = state.NewPlainStateWriterNoHistory(batch)
	}

	// Wrap with cache if available (LayeredDB mode).
	if cache != nil {
		stateReader = state.NewCachedStateReader(stateReader, cache)
		stateWriter = state.NewCachedStateWriter(stateWriter, cache)
	}

	return stateReader, stateWriter, nil
}
