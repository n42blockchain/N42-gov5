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
// N42 Block type and the auxiliary Verify consensus record. Block
// pairs a Header with a Body and caches hash + rlp size via
// sync/atomic.Value, alongside ReceiveAt/ReceivedFrom metadata used
// by the sync layer. Verify holds a validator's Address + PublicKey
// and its ToProtoMessage / FromProtoMessage convert between the
// in-memory form and proto/types_pb.Verifier for P2P gossip.

package block

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/lib/rlp"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
)

type Block struct {
	header *Header
	body   *Body

	hash atomic.Value
	size atomic.Value

	ReceiveAt    time.Time
	ReceivedFrom interface{}
}

type Verify struct {
	Address   types.Address
	PublicKey types.PublicKey
}

func (v *Verify) ToProtoMessage() proto.Message {
	return &types_pb.Verifier{
		Address:   utils.ConvertAddressToH160(v.Address),
		PublicKey: utils.ConvertPublicKeyToH384(v.PublicKey),
	}
}

func (v *Verify) FromProtoMessage(pbVerifier *types_pb.Verifier) *Verify {
	v.Address = utils.ConvertH160toAddress(pbVerifier.Address)
	v.PublicKey = utils.ConvertH384ToPublicKey(pbVerifier.PublicKey)
	return v
}

type Reward struct {
	Address types.Address
	Amount  *uint256.Int
}

type Rewards []*Reward

func (r Rewards) Len() int {
	return len(r)
}

func (r Rewards) Less(i, j int) bool {
	return strings.Compare(r[i].Address.String(), r[j].Address.String()) > 0
}

func (r Rewards) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}

// EncodeIndex implements DerivableList for computing withdrawalsRoot via DeriveSha.
// Encoding: RLP([address(20B), amount(big.Int)])
func (r Rewards) EncodeIndex(i int, w *bytes.Buffer) {
	rlp.Encode(w, []interface{}{r[i].Address, r[i].Amount.ToBig()})
}

func (r *Reward) ToProtoMessage() proto.Message {
	return &types_pb.Reward{
		Address: utils.ConvertAddressToH160(r.Address),
		Amount:  utils.ConvertUint256IntToH256(r.Amount),
	}
}

func (r *Reward) FromProtoMessage(pbReward *types_pb.Reward) *Reward {
	r.Address = utils.ConvertH160toAddress(pbReward.Address)
	r.Amount = utils.ConvertH256ToUint256Int(pbReward.Amount)
	return r
}

func (b *Block) Transactions() []*transaction.Transaction {
	if b.body != nil {
		return b.body.Transactions()
	}
	return nil
}

func (b *Block) StateRoot() types.Hash {
	return b.header.Root
}

func (b *Block) Hash() types.Hash {
	return b.header.Hash()
}

func (b *Block) Marshal() ([]byte, error) {
	return proto.Marshal(b.ToProtoMessage())
}

func (b *Block) Unmarshal(data []byte) error {
	var pBlock types_pb.Block
	if err := proto.Unmarshal(data, &pBlock); err != nil {
		return err
	}
	return b.FromProtoMessage(&pBlock)
}

// blockRLP is the ETH-standard RLP wire form of a Block: the header, each
// transaction as EIP-2718 raw bytes (legacy RLP or typed envelope), and N42's
// verifiers/rewards/zkproof. This replaces the proto transport encoding. The
// block hash is unaffected — it is keccak(rlp(header)) and the header
// round-trips byte-identically (see header RLP optional tags).
type blockRLP struct {
	Header    *Header
	TxData    [][]byte
	Verifiers []*Verify
	Rewards   []*Reward
	ZkProof   []byte `rlp:"optional"`
}

// EncodeRLP implements rlp.Encoder, emitting the ETH-standard wire form.
func (b *Block) EncodeRLP(w io.Writer) error {
	body := b.body
	if body == nil {
		body = &Body{}
	}
	txData := make([][]byte, len(body.Txs))
	for i, tx := range body.Txs {
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			return err
		}
		txData[i] = enc
	}
	return rlp.Encode(w, &blockRLP{
		Header:    b.header,
		TxData:    txData,
		Verifiers: body.Verifiers,
		Rewards:   body.Rewards,
		ZkProof:   body.ZkProof,
	})
}

// DecodeRLP implements rlp.Decoder.
func (b *Block) DecodeRLP(s *rlp.Stream) error {
	var dec blockRLP
	if err := s.Decode(&dec); err != nil {
		return err
	}
	txs := make([]*transaction.Transaction, len(dec.TxData))
	for i, enc := range dec.TxData {
		tx, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			return err
		}
		txs[i] = tx
	}
	b.header = dec.Header
	b.body = &Body{Txs: txs, Verifiers: dec.Verifiers, Rewards: dec.Rewards, ZkProof: dec.ZkProof}
	b.hash = atomic.Value{}
	b.size = atomic.Value{}
	return nil
}

func NewBlock(h IHeader, txs []*transaction.Transaction) IBlock {
	return &Block{
		header:    CopyHeader(h.(*Header)),
		body:      &Body{Txs: txs},
		ReceiveAt: time.Now(),
	}
}

func NewBlockFromReceipt(h IHeader, txs []*transaction.Transaction, _ []IHeader, receipts []*Receipt, reward []*Reward) IBlock {
	block := &Block{
		header:    CopyHeader(h.(*Header)),
		body:      &Body{Txs: txs, Rewards: CopyReward(reward)},
		ReceiveAt: time.Now(),
	}

	block.header.Bloom = CreateBloom(receipts)
	block.header.TxHash = TxRoot(txs)
	block.header.ReceiptHash = hash.DeriveSha(Receipts(receipts))

	return block
}

// UseEthereumTxRoot, when true, computes the block transactions root with the
// Ethereum-standard MPT trie root over EIP-2718 raw transactions
// (DeriveShaErigon + EthTransactions) instead of N42's native keccak-concat over
// proto bytes (DeriveSha + Transactions). Set once at startup from the chain
// config: mainnet_qmdb uses ETH RLP; legacy native chains (e.g. n42 mainnet)
// keep the proto encoding so their historical block hashes stay continuous.
// Changing it changes TxHash and therefore the block hash, so it must be paired
// with a chain reset.
//
// INVARIANT: process-global, set by plain assignment with no synchronization. It
// is safe only because there is exactly one chain (one setter) per process — the
// node sets it in NewBlockChain, the replay CLI in NewEngineV2. Running two
// chains with different schemes in one process (or a test exercising both) would
// race and corrupt this flag; that setup must instead carry the decision on the
// chain/engine and thread it into TxRoot.
var UseEthereumTxRoot bool

// TxRoot computes a block's transactions root, honoring UseEthereumTxRoot. It is
// the single definition shared by block production, replay and validation so
// they always agree on the canonical TxHash.
func TxRoot(txs []*transaction.Transaction) types.Hash {
	if UseEthereumTxRoot {
		return hash.DeriveShaErigon(transaction.EthTransactions(txs))
	}
	return hash.DeriveSha(transaction.Transactions(txs))
}

func (b *Block) Header() IHeader {
	return CopyHeader(b.header)
}

func (b *Block) Body() IBody {
	return b.body
}

func (b *Block) Number64() *uint256.Int {
	return b.header.Number
}

func (b *Block) BaseFee64() *uint256.Int {
	return b.header.BaseFee
}

func (b *Block) Difficulty() *uint256.Int {
	return b.header.Difficulty
}

func (b *Block) Time() uint64 {
	return b.header.Time
}

func (b *Block) GasLimit() uint64 {
	return b.header.GasLimit
}

func (b *Block) GasUsed() uint64 {
	return b.header.GasUsed
}

func (b *Block) Nonce() uint64 {
	return b.header.Nonce.Uint64()
}

func (b *Block) Coinbase() types.Address {
	return b.header.Coinbase
}

func (b *Block) ParentHash() types.Hash {
	return b.header.ParentHash
}

func (b *Block) TxHash() types.Hash {
	return b.header.TxHash
}

func (b *Block) WithSeal(header IHeader) *Block {
	b.header = CopyHeader(header.(*Header))
	return b
}

func (b *Block) Transaction(hash types.Hash) *transaction.Transaction {
	return nil
}

func (b *Block) ToProtoMessage() proto.Message {
	return &types_pb.Block{
		Header: b.header.ToProtoMessage().(*types_pb.Header),
		Body:   b.body.ToProtoMessage().(*types_pb.Body),
	}
}

func (b *Block) FromProtoMessage(message proto.Message) error {
	pBlock, ok := message.(*types_pb.Block)
	if !ok {
		return fmt.Errorf("type conversion failure")
	}

	var header Header
	if err := header.FromProtoMessage(pBlock.Header); err != nil {
		return err
	}

	var body Body
	if err := body.FromProtoMessage(pBlock.Body); err != nil {
		return err
	}

	b.header = &header
	b.body = &body
	b.ReceiveAt = time.Now()
	return nil
}

func (b *Block) SendersToTxs(senders []types.Address) {
	// Sender assignment is handled during transaction deserialization,
	// this method is kept for interface compatibility.
}

func (b *Block) Uncles() []*Header {
	return nil
}
