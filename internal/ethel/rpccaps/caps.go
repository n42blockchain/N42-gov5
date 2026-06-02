// Package rpccaps is the canonical, testable encoding of the minimal/mobile
// client RPC capability matrix and its data-trimming spec
// (docs/ethel/minimal-client-rpc-capability.md). It answers two questions a node
// must answer at startup:
//
//   - Serviceable(mode, method, scope): can this node mode serve this eth_ RPC at
//     this scope (latest vs historical)? — used to gate/advertise the RPC surface
//     so an unsupported call returns a clean "not available in mode X" instead of
//     a wrong/opaque result.
//   - RequiredData(mode): which on-disk data classes the mode must keep. The
//     COMPLEMENT is what a node in that mode may prune — the data-trimming spec.
//
// Principle: an RPC is serviceable iff the data it reads is recorded. The matrix
// here and the doc are kept in lock-step by caps_test.go.
package rpccaps

// Mode is a client/node data-availability profile.
type Mode int

const (
	M0      Mode = iota // witness-direct (mobile minimal): rolling window of changeset+receipts
	M1                  // snapshot + hot-delta: full CURRENT state
	Full                // sync-to-tip full node: latest state + post-merge bodies + recent receipts
	Archive             // full historical state + all receipts/logs/proofs
)

func (m Mode) String() string {
	switch m {
	case M0:
		return "M0-witness-direct"
	case M1:
		return "M1-snapshot+hot"
	case Full:
		return "full"
	case Archive:
		return "archive"
	default:
		return "unknown"
	}
}

// Scope distinguishes a latest/recent query from an arbitrary-historical one.
type Scope int

const (
	Latest     Scope = iota // at/near tip (or within a mode's rolling window)
	Historical              // arbitrary old block
)

// Support is how well a (mode, method, scope) is served.
type Support int

const (
	No     Support = iota // data not present → method must be rejected in this mode
	Window                // serviceable only for keys/blocks in the mode's rolling window
	Slow                  // serviceable but expensive (re-execution); not advertised by default
	Yes                   // fully serviceable
)

func (s Support) String() string {
	switch s {
	case No:
		return "no"
	case Window:
		return "window"
	case Slow:
		return "slow"
	case Yes:
		return "yes"
	default:
		return "?"
	}
}

// DataClass is a prunable on-disk data category. RequiredData(mode) lists the
// classes a mode must keep; everything else is prunable in that mode.
type DataClass int

const (
	Headers          DataClass = iota // all block headers (① chain + consensus); tiny, always kept
	PostMergeBodies                   // bodies for post-merge blocks (txs)
	AllBodies                         // bodies for all blocks incl. pre-merge
	TxHashIndex                       // txHash -> (block,index)
	LatestState                       // current PlainState + commitment (no history)
	HistoricalState                   // state-as-of any block (history domain + per-block roots)
	RecentReceipts                    // receipts + logs for a recent window
	AllReceipts                       // receipts + logs for all heights
	LogIndex                          // address/topic -> blocks (for getLogs)
	RollingChangeset                  // M0: derived changeset (account/storage newValues) for the window
	Consensus                         // beacon/CL state + finality (sync-to-tip, validation)
	P2P                               // devp2p peering (sync, gossip)
	Mempool                           // txpool (sendRawTransaction)
)

var dataClassName = map[DataClass]string{
	Headers: "headers", PostMergeBodies: "post-merge-bodies", AllBodies: "all-bodies",
	TxHashIndex: "tx-hash-index", LatestState: "latest-state", HistoricalState: "historical-state",
	RecentReceipts: "recent-receipts", AllReceipts: "all-receipts", LogIndex: "log-index",
	RollingChangeset: "rolling-changeset", Consensus: "consensus", P2P: "p2p", Mempool: "mempool",
}

func (d DataClass) String() string { return dataClassName[d] }

// RequiredData returns the data classes a mode MUST retain. The complement of
// this set (over all DataClass values) is what the mode may prune.
func RequiredData(m Mode) []DataClass {
	switch m {
	case M0:
		return []DataClass{Headers, RollingChangeset, RecentReceipts, Consensus, P2P}
	case M1:
		// Cold snapshot = LatestState (keccak/plain state as-of H0) + hot delta;
		// plus headers + consensus + p2p to follow the tip.
		return []DataClass{Headers, LatestState, Consensus, P2P}
	case Full:
		// The asked-for "full" profile: sync-to-tip, consensus, P2P, latest state,
		// post-merge bodies+tx index, recent receipts+log index.
		return []DataClass{Headers, PostMergeBodies, TxHashIndex, LatestState,
			RecentReceipts, LogIndex, Consensus, P2P, Mempool}
	case Archive:
		// The everything-node: keeps every data class (PostMergeBodies⊂AllBodies and
		// RecentReceipts⊂AllReceipts are listed explicitly so Prunable(Archive) is
		// empty; RollingChangeset is M0's derived artifact but archive-keeps-all).
		return []DataClass{Headers, PostMergeBodies, AllBodies, TxHashIndex, LatestState,
			HistoricalState, RecentReceipts, AllReceipts, LogIndex, RollingChangeset,
			Consensus, P2P, Mempool}
	default:
		return nil
	}
}

// Prunable returns the data classes a mode may DROP (the complement of RequiredData).
func Prunable(m Mode) []DataClass {
	keep := map[DataClass]bool{}
	for _, d := range RequiredData(m) {
		keep[d] = true
	}
	var out []DataClass
	for d := DataClass(0); d <= Mempool; d++ {
		if !keep[d] {
			out = append(out, d)
		}
	}
	return out
}

// category groups eth_ methods by the data they read.
type category int

const (
	catMeta     category = iota // blockNumber/chainId/syncing/gasPrice/feeHistory
	catHeader                   // getBlockBy* (header fields only)
	catBody                     // block txs / counts / by-index / uncles
	catTxByHash                 // getTransactionByHash (needs txHash index)
	catState                    // getBalance/getCode/getStorageAt/getTransactionCount
	catProof                    // getProof
	catEVM                      // call/estimateGas
	catReceipt                  // getTransactionReceipt/getBlockReceipts
	catLogs                     // getLogs/getFilterLogs/*Filter
	catMempool                  // sendRawTransaction/newPendingTransactionFilter
)

var methodCat = map[string]category{
	"eth_blockNumber": catMeta, "eth_chainId": catMeta, "eth_syncing": catMeta,
	"eth_gasPrice": catMeta, "eth_feeHistory": catMeta, "eth_maxPriorityFeePerGas": catMeta,
	"eth_getBlockByNumber": catHeader, "eth_getBlockByHash": catHeader,
	"eth_getBlockTransactionCountByHash": catBody, "eth_getBlockTransactionCountByNumber": catBody,
	"eth_getTransactionByBlockHashAndIndex": catBody, "eth_getTransactionByBlockNumberAndIndex": catBody,
	"eth_getUncleCountByBlockHash": catBody, "eth_getUncleCountByBlockNumber": catBody,
	"eth_getTransactionByHash": catTxByHash,
	"eth_getBalance":           catState, "eth_getCode": catState, "eth_getStorageAt": catState,
	"eth_getTransactionCount": catState,
	"eth_getProof":            catProof,
	"eth_call":                catEVM, "eth_estimateGas": catEVM,
	"eth_getTransactionReceipt": catReceipt, "eth_getBlockReceipts": catReceipt,
	"eth_getLogs": catLogs, "eth_getFilterLogs": catLogs, "eth_newFilter": catLogs,
	"eth_sendRawTransaction": catMempool, "eth_newPendingTransactionFilter": catMempool,
}

// Serviceable reports how well a mode can serve a method at a scope. Unknown
// methods return No (conservative: gate it). This is the single source of truth
// the RPC layer consults to gate/advertise; it matches the doc's matrix.
func Serviceable(m Mode, method string, scope Scope) Support {
	cat, ok := methodCat[method]
	if !ok {
		return No
	}
	switch cat {
	case catMeta:
		return Yes // all EL modes have head/headers
	case catHeader:
		return Yes // headers retained in every EL mode
	case catBody:
		switch m {
		case M0:
			return Window // recent bodies only
		case M1:
			return No // M1 keeps state, not bodies
		case Full:
			return Yes // post-merge bodies (the Full profile); pre-merge pruned but txs are post-merge
		case Archive:
			return Yes
		}
	case catTxByHash:
		switch m {
		case Full, Archive:
			return Yes // tx-hash index present
		default:
			return No // M0/M1 have no global tx index
		}
	case catState, catProof, catEVM:
		switch m {
		case M0:
			if scope == Latest {
				return Window // only keys changed in the rolling window
			}
			return No
		case M1, Full:
			if scope == Latest {
				return Yes // full current state
			}
			return No // no historical state (latest only)
		case Archive:
			return Yes // latest + historical
		}
	case catReceipt:
		if scope == Historical {
			if m == Archive {
				return Yes
			}
			return No // window/recompute only cover recent; outside the window = No
		}
		switch m {
		case M0:
			return Window // replay window blocks
		case M1:
			return Slow // recompute (no stored receipts)
		case Full, Archive:
			return Yes // recent (Full) / all (Archive) receipts stored
		}
	case catLogs:
		if scope == Historical {
			if m == Archive {
				return Yes
			}
			return No
		}
		switch m {
		case M0:
			return Window // window range if logs indexed
		case M1:
			return No
		case Full, Archive:
			return Yes
		}
	case catMempool:
		switch m {
		case Full, Archive:
			return Yes
		default:
			return No // M0/M1 forward to a producer
		}
	}
	return No
}

// Methods returns the eth_ methods this matrix knows about (sorted-by-iteration
// is not guaranteed; callers sort if needed). Used by tests + RPC advertising.
func Methods() []string {
	out := make([]string, 0, len(methodCat))
	for k := range methodCat {
		out = append(out, k)
	}
	return out
}
