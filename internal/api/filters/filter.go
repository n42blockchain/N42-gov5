package filters

import (
	"context"
	"errors"
	"math/big"

	"github.com/RoaringBitmap/roaring"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

type Api interface {
	TxsPool() common.ITxsPool
	Database() kv.RwDB
	Engine() consensus.Engine
	BlockChain() common.IBlockChain
	GetEvm(ctx context.Context, msg internal.Message, ibs evmtypes.IntraBlockState, header block.IHeader, vmConfig *vm2.Config) (*vm2.EVM, func() error, error)
}

// maxLogResults is the maximum number of log results returned by a filter query
// to prevent memory exhaustion from unbounded responses.
const maxLogResults = 10000

// Filter can be used to retrieve and filter logs.
type Filter struct {
	api Api

	db        kv.RwDB
	addresses []types.Address
	topics    [][]types.Hash

	block      types.Hash // Block hash if filtering a single block
	begin, end int64      // Range interval if filtering multiple blocks
}

// NewRangeFilter creates a new filter which uses a bloom filter on blocks to
// figure out whether a particular block is interesting or not.
func NewRangeFilter(api Api, begin, end int64, addresses []types.Address, topics [][]types.Hash) *Filter {
	filter := newFilter(api, addresses, topics)
	filter.begin = begin
	filter.end = end
	return filter
}

// NewBlockFilter creates a new filter which directly inspects the contents of
// a block to figure out whether it is interesting or not.
func NewBlockFilter(api Api, block types.Hash, addresses []types.Address, topics [][]types.Hash) *Filter {
	// Create a generic filter and convert it into a block filter
	filter := newFilter(api, addresses, topics)
	filter.block = block
	return filter
}

// newFilter creates a generic filter that can either filter based on a block hash,
// or based on range queries. The search criteria needs to be explicitly set.
func newFilter(api Api, addresses []types.Address, topics [][]types.Hash) *Filter {
	return &Filter{
		api:       api,
		addresses: addresses,
		topics:    topics,
		db:        api.Database(),
	}
}

// Logs searches the blockchain for matching log entries, returning all from the
// first block that contains matches, updating the start of the filter accordingly.
func (f *Filter) Logs(ctx context.Context) ([]*block.Log, error) {
	// If we're doing singleton block filtering, execute and return
	if f.block != (types.Hash{}) {
		header, err := f.api.BlockChain().GetHeaderByHash(f.block)
		if err != nil {
			return nil, err
		}
		if header == nil {
			return nil, errors.New("unknown block")
		}
		return f.blockLogs(ctx, header)
	}
	// Short-cut if all we care about is pending logs
	if f.begin == jsonrpc.PendingBlockNumber.Int64() {
		if f.end != jsonrpc.PendingBlockNumber.Int64() {
			return nil, errors.New("invalid block range")
		}
		return f.pendingLogs()
	}
	// Figure out the limits of the filter range
	curBlock := f.api.BlockChain().CurrentBlock()
	if curBlock == nil {
		return nil, nil
	}
	header := curBlock.Header()
	if header == nil {
		return nil, nil
	}
	head, err := requireHeaderNumber(header, "current block number unavailable")
	if err != nil {
		return nil, err
	}
	var (
		end     = uint64(f.end)
		pending = f.end == jsonrpc.PendingBlockNumber.Int64()
	)
	// Resolve every named-block sentinel (latest/pending/safe/finalized, all
	// negative) to a concrete height. Handling only latest/pending left safe
	// (-4) and finalized (-3) as raw negatives, which uint64() turned into
	// enormous numbers so the scan matched nothing and the query silently
	// returned empty. Under HotStuff a committed block IS final, so head is the
	// correct resolution for safe/finalized here; on the CL-driven eth-el
	// profile it is a superset (logs up to head), never empty.
	if f.begin < 0 {
		f.begin = int64(head)
	}
	if f.end < 0 {
		end = head
	}
	// Try indexed log lookup first; fall back to unindexed if the index is
	// not available or an error occurs.
	logs, indexed, err := f.indexedLogs(ctx, end)
	if err != nil {
		return logs, err
	}
	if !indexed {
		// Index unavailable — fall back to full scan.
		logs, err = f.unindexedLogs(ctx, end)
		if err != nil {
			return logs, err
		}
	}

	if pending {
		pendingLogs, err := f.pendingLogs()
		if err != nil {
			return nil, err
		}
		logs = append(logs, pendingLogs...)
	}
	return logs, err
}

// indexedLogs returns the logs matching the filter criteria using the roaring
// bitmap indices stored in LogAddressIndex / LogTopicIndex tables. The third
// return value (indexed) indicates whether the index was actually used. When
// false the caller should fall back to unindexedLogs.
func (f *Filter) indexedLogs(ctx context.Context, end uint64) ([]*block.Log, bool, error) {
	from := uint64(f.begin)

	var candidateBlocks *roaring.Bitmap

	// Open a read-only DB transaction for index lookups.
	dbTx, err := f.db.BeginRo(ctx)
	if err != nil {
		return nil, false, err
	}
	defer dbTx.Rollback()

	// Resolve address candidates.
	if len(f.addresses) > 0 {
		addrBM, err := rawdb.BlocksForAddresses(dbTx, f.addresses, from, end)
		if err != nil {
			// Index not available or table empty — fall back.
			return nil, false, nil
		}
		candidateBlocks = addrBM
	}

	// Resolve topic candidates and intersect with address candidates.
	if len(f.topics) > 0 {
		topicBM, err := rawdb.BlocksForTopics(dbTx, f.topics, from, end)
		if err != nil {
			return nil, false, nil
		}
		if topicBM != nil {
			if candidateBlocks == nil {
				candidateBlocks = topicBM
			} else {
				candidateBlocks.And(topicBM)
			}
		}
		// topicBM == nil means all topic positions were wildcards; no filtering needed.
	}

	// If no filter criteria at all, fall back to unindexed (full scan).
	if len(f.addresses) == 0 && len(f.topics) == 0 {
		return nil, false, nil
	}

	// If candidateBlocks is still nil (all wildcard topics, no addresses), fall back.
	if candidateBlocks == nil {
		return nil, false, nil
	}

	var logs []*block.Log
	it := candidateBlocks.Iterator()
	for it.HasNext() {
		select {
		case <-ctx.Done():
			return logs, true, ctx.Err()
		default:
		}

		number := uint64(it.Next())
		header := f.api.BlockChain().GetHeaderByNumber(uint256.NewInt(number))
		if header == nil {
			continue
		}
		found, err := f.checkMatches(ctx, header)
		if err != nil {
			return logs, true, err
		}
		logs = append(logs, found...)
		if len(logs) >= maxLogResults {
			logs = logs[:maxLogResults]
			return logs, true, nil
		}
	}

	f.begin = int64(end) + 1
	return logs, true, nil
}

// unindexedLogs returns the logs matching the filter criteria based on raw block
// iteration and bloom matching.
func (f *Filter) unindexedLogs(ctx context.Context, end uint64) ([]*block.Log, error) {
	var logs []*block.Log

	for ; f.begin <= int64(end); f.begin++ {
		header := f.api.BlockChain().GetHeaderByNumber(uint256.NewInt(uint64(f.begin)))
		if header == nil {
			return logs, nil
		}
		found, err := f.blockLogs(ctx, header)
		if err != nil {
			return logs, err
		}
		logs = append(logs, found...)
		if len(logs) >= maxLogResults {
			logs = logs[:maxLogResults]
			return logs, nil
		}
	}
	return logs, nil
}

// blockLogs returns the logs matching the filter criteria within a single block.
// It uses the header's on-chain Bloom filter to skip blocks that cannot
// contain matching logs.  If the header cannot be cast to *block.Header the
// bloom check is skipped and checkMatches is called unconditionally.
func (f *Filter) blockLogs(ctx context.Context, header block.IHeader) (logs []*block.Log, err error) {
	if h, ok := header.(*block.Header); ok {
		if !bloomFilter(h.Bloom, f.addresses, f.topics) {
			return logs, nil
		}
	}
	found, err := f.checkMatches(ctx, header)
	if err != nil {
		return logs, err
	}
	logs = append(logs, found...)
	return logs, nil
}

// checkMatches checks if the receipts belonging to the given header contain any log events that
// match the filter criteria. This function is called when the bloom filter signals a potential match.
func (f *Filter) checkMatches(ctx context.Context, header block.IHeader) (logs []*block.Log, err error) {
	// Get the logs of the block
	logsList, err := f.api.BlockChain().GetLogs(header.Hash())
	if err != nil {
		return nil, err
	}
	var unfiltered []*block.Log
	for _, logs := range logsList {
		unfiltered = append(unfiltered, logs...)
	}
	logs = filterLogs(unfiltered, nil, nil, f.addresses, f.topics)
	if len(logs) > 0 {
		// We have matching logs, check if we need to resolve full logs via the light client
		if logs[0].TxHash == (types.Hash{}) {
			receipts, err := f.api.BlockChain().GetReceipts(header.Hash())
			if err != nil {
				return nil, err
			}
			unfiltered = unfiltered[:0]
			for _, receipt := range receipts {
				unfiltered = append(unfiltered, receipt.Logs...)
			}
			logs = filterLogs(unfiltered, nil, nil, f.addresses, f.topics)
		}
		return logs, nil
	}
	return nil, nil
}

// pendingLogs returns the logs matching the filter criteria within the pending block.
// Currently returns nil; pending block log filtering is not implemented.
func (f *Filter) pendingLogs() ([]*block.Log, error) {
	return nil, nil
}

func includes(addresses []types.Address, a types.Address) bool {
	for _, addr := range addresses {
		if addr == a {
			return true
		}
	}

	return false
}

// filterLogs creates a slice of logs matching the given criteria.
func filterLogs(logs []*block.Log, fromBlock, toBlock *big.Int, addresses []types.Address, topics [][]types.Hash) []*block.Log {
	var ret []*block.Log
Logs:
	for _, log := range logs {
		if fromBlock != nil && fromBlock.Int64() >= 0 && fromBlock.Uint64() > log.BlockNumber.Uint64() {
			continue
		}
		if toBlock != nil && toBlock.Int64() >= 0 && toBlock.Uint64() < log.BlockNumber.Uint64() {
			continue
		}

		if len(addresses) > 0 && !includes(addresses, log.Address) {
			continue
		}
		// If the to filtered topics is greater than the amount of topics in logs, skip.
		if len(topics) > len(log.Topics) {
			continue
		}
		for i, sub := range topics {
			match := len(sub) == 0 // empty rule set == wildcard
			for _, topic := range sub {
				if log.Topics[i] == topic {
					match = true
					break
				}
			}
			if !match {
				continue Logs
			}
		}
		ret = append(ret, log)
	}
	return ret
}

// bloomFilter reports whether the 2048-bit Ethereum Bloom filter could contain
// any log matching the given address and topic filter criteria.
// An empty filter set (no addresses, no topics) always returns true.
func bloomFilter(bloom block.Bloom, addresses []types.Address, topics [][]types.Hash) bool {
	if len(addresses) > 0 {
		var included bool
		for _, addr := range addresses {
			if bloom.Test(addr.Bytes()) {
				included = true
				break
			}
		}
		if !included {
			return false
		}
	}

	for _, sub := range topics {
		included := len(sub) == 0 // empty rule set == wildcard
		for _, topic := range sub {
			if bloom.Test(topic.Bytes()) {
				included = true
				break
			}
		}
		if !included {
			return false
		}
	}
	return true
}
