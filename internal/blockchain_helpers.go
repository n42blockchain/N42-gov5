package internal

import (
	"errors"
	"math/big"
	"os"
	"strconv"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

func requireConcreteBlock(blk block.IBlock, msg string) (*block.Block, error) {
	concreteBlock, ok := blk.(*block.Block)
	if !ok || concreteBlock == nil {
		return nil, errors.New(msg)
	}
	return concreteBlock, nil
}

func requireConcreteHeader(header block.IHeader, msg string) (*block.Header, error) {
	concreteHeader, ok := header.(*block.Header)
	if !ok || concreteHeader == nil {
		return nil, errors.New(msg)
	}
	return concreteHeader, nil
}

func requireBlockNumber(blk block.IBlock, msg string) (*uint256.Int, error) {
	if blk == nil {
		return nil, errors.New("block is nil")
	}
	number := blk.Number64()
	if number == nil {
		return nil, errors.New(msg)
	}
	return number, nil
}

func requireHeaderNumber(header block.IHeader, msg string) (*uint256.Int, error) {
	if header == nil {
		return nil, errors.New("header is nil")
	}
	number := header.Number64()
	if number == nil {
		return nil, errors.New(msg)
	}
	return number, nil
}

// txRootBlake3Time is the chain's txRootBlake3Time, or -- on a bench chain
// whose built-in chainspec has none -- N42_TXROOT_BLAKE3_TIME (a Unix
// timestamp), so a fleet can run the BLAKE3 binary transactions root from
// a chosen block time before the chainspec carries the fork. Logged loudly:
// every node of the chain must agree on it.
func txRootBlake3Time(config *params.ChainConfig) uint64 {
	if config != nil && config.TxRootBlake3Time != nil {
		return gateFrom(config.TxRootBlake3Time)
	}
	if v := os.Getenv("N42_TXROOT_BLAKE3_TIME"); v != "" {
		if !nativeQMDBChain(config) {
			log.Warn("N42_TXROOT_BLAKE3_TIME ignored: not the native QMDB chain")
			return 0
		}
		if t, err := strconv.ParseUint(v, 10, 64); err == nil && t > 0 {
			if config != nil {
				config.TxRootBlake3Time = new(big.Int).SetUint64(t)
			}
			log.Warn("transactions root: BLAKE3 binary root from N42_TXROOT_BLAKE3_TIME (bench override; every node must set the same value)", "time", t)
			return t
		}
		log.Warn("N42_TXROOT_BLAKE3_TIME ignored (not a positive integer)", "value", v)
	}
	return 0
}

// deferredExecutionTime is the chain's deferredExecutionTime, or -- on a
// bench chain whose built-in chainspec has none -- N42_DEFERRED_EXECUTION_TIME
// (a Unix timestamp), applied to the chain config too so every fork check
// in the node sees it. Logged loudly: every node of the chain must agree.
func deferredExecutionTime(config *params.ChainConfig) uint64 {
	if config != nil && config.DeferredExecutionTime != nil {
		return gateFrom(config.DeferredExecutionTime)
	}
	if v := os.Getenv("N42_DEFERRED_EXECUTION_TIME"); v != "" {
		if !nativeQMDBChain(config) {
			log.Warn("N42_DEFERRED_EXECUTION_TIME ignored: not the native QMDB chain")
			return 0
		}
		if t, err := strconv.ParseUint(v, 10, 64); err == nil && t > 0 {
			if config != nil {
				config.DeferredExecutionTime = new(big.Int).SetUint64(t)
			}
			log.Warn("deferred execution from N42_DEFERRED_EXECUTION_TIME (bench override; every node must set the same value)", "time", t)
			return t
		}
		log.Warn("N42_DEFERRED_EXECUTION_TIME ignored (not a positive integer)", "value", v)
	}
	return 0
}

// nativeQMDBChain: the bench-only fork overrides apply to the native chain
// (QMDB state scheme) only, never to an Ethereum-EL chain.
func nativeQMDBChain(config *params.ChainConfig) bool {
	return config != nil && config.StateScheme == string(params.StateCommitmentPresetQMDB) && !config.EthereumReceiptEncoding()
}

// gateFrom turns a configured fork timestamp into the process-global: a
// present zero means "from genesis" (isForked(0, t) is true for every t),
// which the global expresses as 1, since 0 means "never" there.
func gateFrom(v *big.Int) uint64 {
	if v.Sign() <= 0 {
		return 1
	}
	return v.Uint64()
}

// executedRootIn is ExecutedResultOfHeader's root read inside tx: the
// header's Root before the deferred-execution fork (and for genesis), the
// stored result after it; ok is false when the result is not stored.
func (bc *BlockChain) executedRootIn(tx kv.Getter, h *block.Header) (types.Hash, bool) {
	r, err := ExecutedResultOfHeader(bc.chainConfig, tx, h)
	if err != nil {
		return types.Hash{}, false
	}
	return r.Root, true
}
