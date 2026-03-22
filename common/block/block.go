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

package block

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/utils"
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
	block.header.TxHash = hash.DeriveSha(transaction.Transactions(txs))
	block.header.ReceiptHash = hash.DeriveSha(Receipts(receipts))

	return block
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
