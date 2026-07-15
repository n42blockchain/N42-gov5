// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// process.go — shared per-block execution core.
//
// ProcessBlock takes a header, a slice of transactions, uncles, and an
// IntraBlockState, then drives the EVM through every transaction using
// the chain config fork rules and the consensus engine's Finalize hook.
// Pre-computed senders can be injected to skip ecrecover. The returned
// BlockResult carries gas used, receipts and senders. The function is
// shared by the batch Executor and the Engine API adapter so both paths
// produce byte-identical receipts and state transitions.

package ethel

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	iinternal "github.com/n42blockchain/N42/internal"
)

// BlockResult holds per-block execution outputs.
type BlockResult struct {
	GasUsed  uint64
	Receipts block.Receipts
	Senders  []types.Address
	// BALRaw is the canonical RLP of the block's EIP-7928 BAL, set only when the
	// BAL fork is active. The executor stores it (keyed by block hash) so the
	// out-of-band BAL service can serve it. nil when the fork is off.
	BALRaw []byte
}

// Trace-mode env vars resolved once at process startup. Reading
// os.Getenv inside the per-tx loop costs a map lookup per call;
// hoisting saves ~24K syscalls/sec at 200 tx/block × 60 blk/s.
var (
	traceTxHashEnv = strings.ToLower(strings.TrimPrefix(os.Getenv("N42_TRACE_TX"), "0x"))
	traceBlockEnv  = func() uint64 {
		s := os.Getenv("N42_TRACE_BLOCK")
		if s == "" {
			return 0
		}
		var v uint64
		fmt.Sscanf(s, "%d", &v)
		return v
	}()
	traceSkipOthersEnv = os.Getenv("N42_TRACE_SKIP_OTHERS") == "1"
	noopTracerEnv      = os.Getenv("N42_NOOP_TRACER") == "1"
	traceProdPathEnv   = os.Getenv("N42_TRACE_PROD_PATH") == "1"
	traceOutEnv        = os.Getenv("N42_TRACE_OUT")
)

func applyEthelWithdrawals(chainCfg *params.ChainConfig, header *block.Header, ibs *state.IntraBlockState, withdrawals []*Withdrawal) {
	if chainCfg == nil || header == nil || header.Number == nil || ibs == nil || len(withdrawals) == 0 {
		return
	}
	if !chainCfg.IsShanghaiAt(header.Number.Uint64(), header.Time) {
		return
	}
	gwei := uint256.NewInt(params.GWei)
	for _, withdrawal := range withdrawals {
		if withdrawal == nil || withdrawal.Amount == 0 {
			continue
		}
		amount := new(uint256.Int).Mul(uint256.NewInt(withdrawal.Amount), gwei)
		if amount.IsZero() {
			continue
		}
		ibs.AddBalance(withdrawal.Address, amount)
	}
}

// ProcessBlock executes all transactions in a block against the given state.
// This is the shared core between the batch Executor and the Engine API adapter.
//
// Parameters:
//   - chainCfg: chain configuration with fork rules
//   - engine: consensus engine (for Author, Finalize)
//   - header: block header being executed
//   - txs: transactions to execute
//   - uncles: uncle headers (for reward calculation, nil post-merge)
//   - withdrawals: EIP-4895 withdrawals, nil before Shanghai
//   - ibs: in-memory state database
//   - blockHashFunc: BLOCKHASH opcode implementation
//
// ProcessBlock executes all transactions in a block.
// If precomputedSenders is provided (non-nil), senders are injected directly
// into transactions via SetFrom, skipping expensive ecrecover.
func ProcessBlock(
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	header *block.Header,
	txs []*transaction.Transaction,
	uncles []block.IHeader,
	withdrawals []*Withdrawal,
	ibs *state.IntraBlockState,
	blockHashFunc func(uint64) types.Hash,
	precomputedSenders []types.Address,
	stateWriter ...state.StateWriter,
) (*BlockResult, error) {
	usedGas := new(uint64)
	gp := new(common.GasPool)
	gp.AddGas(header.GasLimit)

	cfg := vm2.Config{}

	// DAO fork.
	if chainCfg.DAOForkSupport && chainCfg.DAOForkBlock != nil &&
		chainCfg.DAOForkBlock.Cmp(header.Number.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	// Pre-block system calls (beacon root, Prague system contracts, etc.).
	if err := iinternal.ProcessExecutionBlockStart(header.ParentBeaconRoot, chainCfg, ibs, header, engine); err != nil {
		return nil, err
	}

	blockHash := header.Hash()

	var receipts block.Receipts
	senders := make([]types.Address, 0, len(txs))

	if precomputedSenders != nil {
		for i, txn := range txs {
			if i < len(precomputedSenders) {
				txn.SetFrom(precomputedSenders[i])
			}
		}
	}

	signer := transaction.MakeSigner(chainCfg, header.Number.ToBig())

	noop := state.NewNoopWriter()

	// EIP-7928 (eth-el): when the BAL fork is active, wrap the per-tx finalize
	// writer to harvest each tx's post-value writes, so the block access list hash
	// can be verified against the header and the raw BAL stored for serving. A pure
	// observer (delegates to noop); nil and zero-overhead when the fork is off.
	writer := state.StateWriter(noop)
	var balCap *iinternal.BALCapture
	if chainCfg.IsBAL(header.Time) {
		balCap = iinternal.NewBALCapture(noop)
		writer = balCap
	}

	// One EVM per block, reused across all txs via EVM.Reset(txCtx, ibs).
	// Block context is constant within a block, so we only rebuild txCtx
	// inside applyTransaction. Tracer-attached txs still take a per-tx
	// EVM because vm.Config is captured at construction time. Lazy-init:
	// empty blocks skip the alloc.
	var sharedEVM *vm2.EVM
	if len(txs) > 0 {
		blockCtxShared := iinternal.NewEVMBlockContext(header, blockHashFunc, engine, chainCfg, nil)
		sharedEVM = vm2.NewEVM(blockCtxShared, evmtypes.TxContext{}, ibs, chainCfg, cfg)
	}

	currentBlock := header.Number.Uint64()
	// Skip-non-target only when N42_TRACE_BLOCK is explicitly set AND matches.
	// Default (no traceBlock): run all txs normally so state evolves correctly.
	skipNonTarget := traceTxHashEnv != "" && traceBlockEnv != 0 && traceBlockEnv == currentBlock && traceSkipOthersEnv

	for i, txn := range txs {
		// Trace mode: only skip non-target txs in the TARGET block (or all if
		// N42_TRACE_BLOCK unset). For prior blocks, run all txs normally so
		// state evolves correctly going into the target block.
		if skipNonTarget {
			thisH := strings.ToLower(strings.TrimPrefix(txn.Hash().Hex(), "0x"))
			if thisH != traceTxHashEnv {
				continue
			}
		}
		ibs.Prepare(txn.Hash(), blockHash, i)
		if balCap != nil {
			balCap.BeginTx(txn.Hash())
		}

		txCfg := cfg
		// Probe: attach NoopTracer + Debug=true unconditionally to test
		// whether the "no tracer fast path" has a divergent bug. If forward
		// replay succeeds with this, the fix is just to keep Debug+Tracer
		// always on. If it still fails, the bug is elsewhere.
		if noopTracerEnv {
			txCfg.Tracer = NoopTracer{}
			txCfg.Debug = true
		}
		var traceFile *os.File
		var stepTracer *StepTracer
		if traceTxHashEnv != "" {
			thisHash := strings.ToLower(strings.TrimPrefix(txn.Hash().Hex(), "0x"))
			if thisHash == traceTxHashEnv {
				outPath := traceOutEnv
				if outPath == "" {
					outPath = fmt.Sprintf("n42_trace_%s.json", thisHash[:16])
				}
				f, err := os.Create(outPath)
				if err != nil {
					return nil, fmt.Errorf("create trace file %s: %w", outPath, err)
				}
				log.Info("EthEL tx trace ENABLED", "txHash", txn.Hash().Hex(), "outPath", outPath)
				traceFile = f
				stepTracer = NewStepTracer(f)
				txCfg.Tracer = stepTracer
				txCfg.Debug = true
				// Probe: is the target contract considered "existing"?
				if to := txn.To(); to != nil {
					exists := ibs.Exist(*to)
					log.Info("trace target Exist() probe", "addr", to.Hex(), "exists", exists)
				}
			}
		}

		var receipt *block.Receipt
		var err error
		// Trace mode: only the target tx runs (others skipped above), and we
		// bypass nonce/balance checks since the datadir state may have drifted.
		// EXCEPTION: N42_TRACE_PROD_PATH=1 forces the production apply path
		// (gasBailout=false, normal nonce check) while still attaching the
		// tracer. Used to capture the production-mode trace for diff against
		// trace-mode trace and pinpoint where the two paths diverge.
		bypassChecks := stepTracer != nil && !traceProdPathEnv
		if bypassChecks {
			// Trace path: bypass nonce + insufficient-balance checks so we
			// can produce a trace even when the datadir state has drifted
			// from a prior partial-replay abort. Mirrors the speculative
			// prefetch tx-apply pattern.
			signerNow := transaction.MakeSignerWithTimestamp(chainCfg, header.Number.ToBig(), header.Time)
			msg, asErr := txn.AsMessage(signerNow, header.BaseFee)
			if asErr != nil {
				return nil, fmt.Errorf("trace tx AsMessage: %w", asErr)
			}
			iinternal.NormalizeExecutionMessage(&msg, false, engine != nil)
			msg.SetCheckNonce(false)
			txContext := iinternal.NewEVMTxContext(msg)
			blockContext := iinternal.NewEVMBlockContext(header, blockHashFunc, engine, chainCfg, nil)
			evm := vm2.NewEVM(blockContext, txContext, ibs, chainCfg, txCfg)
			result, applyErr := iinternal.ApplyMessage(evm, msg, gp, true, true)
			if applyErr == nil && result != nil {
				_ = ibs.FinalizeTx(chainCfg.Rules(header.Number.Uint64()), writer)
				*usedGas += result.UsedGas
				receipt = &block.Receipt{
					Type:              txn.Type(),
					CumulativeGasUsed: *usedGas,
					Status:            block.ReceiptStatusSuccessful,
					GasUsed:           result.UsedGas,
				}
				if result.Failed() {
					receipt.Status = block.ReceiptStatusFailed
				}
			}
			err = applyErr
		} else {
			// Hot path: reuse the shared EVM. ApplyTransaction's per-tx
			// NewEVM allocation is the largest preventable per-tx cost
			// outside the interpreter loop itself.
			//
			// Tracer-attached txs (txCfg.Tracer != nil) need a fresh EVM
			// because the tracer is wired through vm.Config at construction
			// time and EVM.Reset doesn't rebuild the interpreter.
			if txCfg.Tracer != nil || txCfg.Debug {
				receipt, _, err = iinternal.ApplyTransaction(
					chainCfg, blockHashFunc, engine, nil, gp,
					ibs, writer, header, txn, usedGas, txCfg,
				)
			} else {
				receipt, _, err = iinternal.ApplyTransactionWithEVM(
					sharedEVM, chainCfg, engine, gp,
					ibs, writer, header, txn, usedGas, txCfg,
				)
			}
		}
		if traceFile != nil {
			failed := err != nil || (receipt != nil && receipt.Status == 0)
			restGas := uint64(0)
			gasUsed := uint64(0)
			if receipt != nil {
				gasUsed = receipt.GasUsed
				restGas = txn.Gas() - receipt.GasUsed
			}
			stepTracer.Close(failed, restGas, nil)
			traceFile.Close()
			log.Info("EthEL tx trace closed", "txHash", txn.Hash().Hex(), "failed", failed, "gasUsed", gasUsed, "err", fmt.Sprintf("%v", err))
		}
		if err != nil {
			if errors.Is(err, common.ErrGasLimitReached) {
				log.Error("Gas pool exhausted",
					"block", header.Number.Uint64(),
					"txIndex", i,
					"txHash", txn.Hash().Hex(),
					"txGasLimit", txn.Gas(),
					"poolRemaining", gp.Gas(),
					"usedGasSoFar", *usedGas,
					"blockGasLimit", header.GasLimit,
					"totalTxs", len(txs),
				)
				// Return partial result so caller can diff against geth receipts.
				return &BlockResult{GasUsed: *usedGas, Receipts: receipts, Senders: senders},
					fmt.Errorf("tx %d: %w", i, err)
			}
			return nil, fmt.Errorf("tx %d: %w", i, err)
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
		}

		// Use pre-computed sender or recover from signature.
		if precomputedSenders != nil && i < len(precomputedSenders) {
			senders = append(senders, precomputedSenders[i])
		} else {
			sender, err := transaction.Sender(signer, txn)
			if err != nil {
				return nil, fmt.Errorf("tx %d sender: %w", i, err)
			}
			senders = append(senders, sender)
		}

	}

	// EIP-4895 validator withdrawals (Shanghai+). Apply before the end-of-block
	// system calls so Prague-era deposit/request processing sees the updated
	// balances if a future fork ever couples the two.
	applyEthelWithdrawals(chainCfg, header, ibs, withdrawals)

	// Post-block system calls depend on the receipts emitted by executed transactions.
	if _, err := iinternal.ProcessExecutionBlockEnd(receipts, chainCfg, ibs, header, engine); err != nil {
		return nil, err
	}

	// Finalize (block rewards + uncle rewards).
	// Skip for genesis block (block 0) — Ethereum genesis has no execution
	// and no coinbase reward. Its state root is purely from the alloc.
	if header.Number.Uint64() > 0 {
		if _, _, err := engine.Finalize(nil, header, ibs, txs, uncles); err != nil {
			return nil, fmt.Errorf("finalize: %w", err)
		}
	}

	var balRaw []byte
	if balCap != nil {
		b := balCap.BALFor(iinternal.TxHashes(txs))
		got, err := b.Hash()
		if err != nil {
			return nil, fmt.Errorf("compute block access list hash: %w", err)
		}
		// Verify against the hash bound in the header (block producer computed the
		// same BAL from the same execution) and keep the raw RLP so the executor can
		// store it for the out-of-band BAL service. System-call writes are not yet
		// harvested; producer and verifier omit them identically.
		if want := header.BlockAccessListHash; want == nil || *want != got {
			return nil, fmt.Errorf("block access list hash mismatch: header %v, computed %s", want, got.Hex())
		}
		if balRaw, err = b.EncodeRLP(); err != nil {
			return nil, fmt.Errorf("encode block access list: %w", err)
		}
	}

	return &BlockResult{GasUsed: *usedGas, Receipts: receipts, Senders: senders, BALRaw: balRaw}, nil
}

