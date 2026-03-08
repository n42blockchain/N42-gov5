package api

import (
	"context"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/lib/kv"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/turbo/rpchelper"
)

// n42API provides an API to access metadata related information.
type n42API struct {
	api *API
}

// NewN42API creates a new Meta protocol API.
func NewN42API(api *API) *n42API {
	return &n42API{api}
}

// GasPrice returns a suggestion for a gas price for legacy transactions.
func (s *n42API) GasPrice(ctx context.Context) (*hexutil.Big, error) {
	conf.LightClientGPO.Default = big.NewInt(params.GWei)
	if s.api.gpo == nil {
		return (*hexutil.Big)(big.NewInt(params.GWei)), nil
	}
	tipcap, err := s.api.gpo.SuggestTipCap(ctx, s.api.GetChainConfig())
	if err != nil {
		return nil, err
	}
	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock != nil {
		if head := currentBlock.Header(); head != nil && head.BaseFee64() != nil && !head.BaseFee64().IsZero() {
			tipcap.Add(tipcap, head.BaseFee64().ToBig())
		}
	}
	return (*hexutil.Big)(tipcap), nil
}

// MaxPriorityFeePerGas returns a suggestion for a gas tip cap for dynamic fee transactions.
func (s *n42API) MaxPriorityFeePerGas(ctx context.Context) (*hexutil.Big, error) {
	if s.api.gpo == nil {
		return (*hexutil.Big)(big.NewInt(params.GWei)), nil
	}
	tipcap, err := s.api.gpo.SuggestTipCap(ctx, s.api.GetChainConfig())
	if err != nil {
		return nil, err
	}
	return (*hexutil.Big)(tipcap), nil
}

type feeHistoryResult struct {
	OldestBlock  *hexutil.Big     `json:"oldestBlock"`
	Reward       [][]*hexutil.Big `json:"reward,omitempty"`
	BaseFee      []*hexutil.Big   `json:"baseFeePerGas,omitempty"`
	GasUsedRatio []float64        `json:"gasUsedRatio"`
}

// FeeHistory returns the fee market history.
func (s *n42API) FeeHistory(ctx context.Context, blockCount jsonrpc.DecimalOrHex, lastBlock jsonrpc.BlockNumber, rewardPercentiles []float64) (*feeHistoryResult, error) {
	var (
		resolvedLastBlock *uint256.Int
		err               error
	)

	if dbErr := s.api.db.View(ctx, func(tx kv.Tx) error {
		resolvedLastBlock, _, err = rpchelper.GetBlockNumber(jsonrpc.BlockNumberOrHashWithNumber(lastBlock), tx)
		return err
	}); dbErr != nil {
		return nil, dbErr
	}

	if err != nil {
		return nil, err
	}

	oldest, reward, baseFee, gasUsed, err := s.api.gpo.FeeHistory(ctx, int(blockCount), lastBlock, resolvedLastBlock, rewardPercentiles)
	if err != nil {
		return nil, err
	}
	results := &feeHistoryResult{
		OldestBlock:  (*hexutil.Big)(oldest),
		GasUsedRatio: gasUsed,
	}
	if reward != nil {
		results.Reward = make([][]*hexutil.Big, len(reward))
		for i, w := range reward {
			results.Reward[i] = make([]*hexutil.Big, len(w))
			for j, v := range w {
				results.Reward[i][j] = (*hexutil.Big)(v)
			}
		}
	}
	if baseFee != nil {
		results.BaseFee = make([]*hexutil.Big, len(baseFee))
		for i, v := range baseFee {
			results.BaseFee[i] = (*hexutil.Big)(v)
		}
	}
	return results, nil
}

// TxPoolAPI offers an API for the transaction pool. It only operates on data that is non-confidential.
type TxPoolAPI struct {
	api *API
}

// NewTxPoolAPI creates a new tx pool service that gives information about the transaction pool.
func NewTxPoolAPI(api *API) *TxPoolAPI {
	return &TxPoolAPI{api}
}

// Content returns the transactions contained within the transaction pool.
func (s *TxPoolAPI) Content() map[string]map[string]map[string]*RPCTransaction {
	content := map[string]map[string]map[string]*RPCTransaction{
		"pending": make(map[string]map[string]*RPCTransaction),
		"queued":  make(map[string]map[string]*RPCTransaction),
	}
	return content
}

// ContentFrom returns the transactions contained within the transaction pool.
func (s *TxPoolAPI) ContentFrom(addr types.Address) map[string]map[string]*RPCTransaction {
	content := make(map[string]map[string]*RPCTransaction, 2)

	return content
}

// Status returns the number of pending and queued transaction in the pool.
func (s *TxPoolAPI) Status() map[string]hexutil.Uint {
	_, pending, _, queue := s.api.TxsPool().Stats()
	return map[string]hexutil.Uint{
		"pending": hexutil.Uint(pending),
		"queued":  hexutil.Uint(queue),
	}
}

// Inspect retrieves the content of the transaction pool and flattens it into an
// easily inspectable list.
func (s *TxPoolAPI) Inspect() map[string]map[string]map[string]string {
	content := map[string]map[string]map[string]string{
		"pending": make(map[string]map[string]string),
		"queued":  make(map[string]map[string]string),
	}

	return content
}

// AccountAPI provides an API to access accounts managed by this node.
// It offers only methods that can retrieve accounts.
type AccountAPI struct {
}

// NewAccountAPI creates a new AccountAPI.
func NewAccountAPI() *AccountAPI {
	return &AccountAPI{}
}

// Accounts returns the collection of accounts this node manages.
func (s *AccountAPI) Accounts() []types.Address {
	return nil
}

type Web3API struct {
	stack *API
}

func (s *Web3API) ClientVersion() string {
	return "N42/" + params.VersionWithMeta
}

func (s *Web3API) Sha3(input hexutil.Bytes) hexutil.Bytes {
	return crypto.Keccak256(input)
}

type DebugAPI struct {
	api *API
}

// NewDebugAPI creates a new instance of DebugAPI.
func NewDebugAPI(api *API) *DebugAPI {
	return &DebugAPI{api: api}
}

// SetHead rewinds the head of the blockchain to a previous block.
func (api *DebugAPI) SetHead(number hexutil.Uint64) {
	api.api.BlockChain().SetHead(uint64(number))
}

func (debug *DebugAPI) GetAccount(ctx context.Context, address types.Address) {

}

// NetAPI offers network related RPC methods
type NetAPI struct {
	api            *API
	networkVersion uint64
}

// NewNetAPI creates a new net API instance.
func NewNetAPI(api *API, networkVersion uint64) *NetAPI {
	return &NetAPI{api, networkVersion}
}

// Listening returns an indication if the node is listening for network connections.
func (s *NetAPI) Listening() bool {
	return true // always listening
}

// PeerCount returns the number of connected peers.
func (s *NetAPI) PeerCount() hexutil.Uint {
	if s.api.p2p != nil {
		return hexutil.Uint(len(s.api.p2p.PeerInfos()))
	}
	return 0
}

// Version returns the current ethereum protocol version.
func (s *NetAPI) Version() string {
	return fmt.Sprintf("%d", s.networkVersion)
}

// TxsPoolAPI offers an API for the transaction pool. It only operates on data that is non-confidential.
type TxsPoolAPI struct {
	api *API
}

// NewTxsPoolAPI creates a new tx pool service that gives information about the transaction pool.
func NewTxsPoolAPI(api *API) *TxsPoolAPI {
	return &TxsPoolAPI{api}
}

// Content returns the transactions contained within the transaction pool.
func (s *TxsPoolAPI) Content() map[string]map[string]map[string]*RPCTransaction {
	content := map[string]map[string]map[string]*RPCTransaction{
		"pending": make(map[string]map[string]*RPCTransaction),
		"queued":  make(map[string]map[string]*RPCTransaction),
	}
	pending, queue := s.api.TxsPool().Content()
	var curHeader block.IHeader
	if curBlock := s.api.BlockChain().CurrentBlock(); curBlock != nil {
		curHeader = curBlock.Header()
	}
	// Flatten the pending transactions
	for account, txs := range pending {
		dump := make(map[string]*RPCTransaction)
		for _, tx := range txs {
			dump[fmt.Sprintf("%d", tx.Nonce())] = newRPCPendingTransaction(tx, curHeader)
		}
		content["pending"][avmtypes.FromastAddress(&account).Hex()] = dump
	}
	// Flatten the queued transactions
	for account, txs := range queue {
		dump := make(map[string]*RPCTransaction)
		for _, tx := range txs {
			dump[fmt.Sprintf("%d", tx.Nonce())] = newRPCPendingTransaction(tx, curHeader)
		}
		content["queued"][avmtypes.FromastAddress(&account).Hex()] = dump
	}
	return content
}
