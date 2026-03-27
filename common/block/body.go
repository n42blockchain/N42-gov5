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

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/utils"
)

type Body struct {
	Txs       []*transaction.Transaction
	Verifiers []*Verify
	Rewards   []*Reward
	// ZkProof holds the optional ZK proof for fast-path verification.
	// Field is named ZkProof (not ZKProof) to avoid conflict with the ZKProof() getter method.
	ZkProof []byte
}

func (b *Body) ToProtoMessage() proto.Message {
	pbTxs := make([]*types_pb.Transaction, 0, len(b.Txs))
	for _, v := range b.Txs {
		pbTxs = append(pbTxs, v.ToProtoMessage().(*types_pb.Transaction))
	}

	pbRewards := make([]*types_pb.Reward, 0, len(b.Rewards))
	for _, reward := range b.Rewards {
		pbRewards = append(pbRewards, reward.ToProtoMessage().(*types_pb.Reward))
	}

	pbVerifiers := make([]*types_pb.Verifier, 0, len(b.Verifiers))
	for _, verifier := range b.Verifiers {
		pbVerifiers = append(pbVerifiers, verifier.ToProtoMessage().(*types_pb.Verifier))
	}

	return &types_pb.Body{
		Txs:       pbTxs,
		Verifiers: pbVerifiers,
		Rewards:   pbRewards,
		ZkProof:   b.ZkProof,
	}
}

func (b *Body) FromProtoMessage(message proto.Message) error {
	pBody, ok := message.(*types_pb.Body)
	if !ok {
		return fmt.Errorf("type conversion failure")
	}

	txs := make([]*transaction.Transaction, 0, len(pBody.Txs))
	for _, v := range pBody.Txs {
		tx, err := transaction.FromProtoMessage(v)
		if err != nil {
			return err
		}
		txs = append(txs, tx)
	}
	b.Txs = txs

	verifiers := make([]*Verify, 0, len(pBody.Verifiers))
	for _, v := range pBody.Verifiers {
		verifiers = append(verifiers, new(Verify).FromProtoMessage(v))
	}
	b.Verifiers = verifiers

	rewards := make([]*Reward, 0, len(pBody.Rewards))
	for _, v := range pBody.Rewards {
		rewards = append(rewards, new(Reward).FromProtoMessage(v))
	}
	b.Rewards = rewards

	if len(pBody.ZkProof) > 0 {
		b.ZkProof = make([]byte, len(pBody.ZkProof))
		copy(b.ZkProof, pBody.ZkProof)
	}

	return nil
}

func (b *Body) Transactions() []*transaction.Transaction {
	return b.Txs
}
func (b *Body) Verifier() []*Verify {
	return b.Verifiers
}

func (b *Body) Reward() []*Reward {
	return b.Rewards
}

func (b *Body) ZKProof() []byte {
	return b.ZkProof
}

func (b *Body) reward() []*types_pb.H256 {
	rewardAmount := make([]*types_pb.H256, 0, len(b.Rewards))
	for _, reward := range b.Rewards {
		rewardAmount = append(rewardAmount, utils.ConvertUint256IntToH256(reward.Amount))
	}
	return rewardAmount
}

func (b *Body) rewardAddress() []types.Address {
	addrs := make([]types.Address, len(b.Rewards))
	for i, reward := range b.Rewards {
		addrs[i] = reward.Address
	}
	return addrs
}

func (b *Body) SendersFromTxs() []types.Address {
	senders := make([]types.Address, len(b.Transactions()))
	for i, tx := range b.Transactions() {
		senders[i] = *tx.From()
	}
	return senders
}

func (b *Body) SendersToTxs(senders []types.Address) {
	if senders == nil {
		return
	}
	// Sender assignment is handled during transaction deserialization,
	// this method is kept for interface compatibility.
}

type BodyForStorage struct {
	BaseTxId uint64
	TxAmount uint32
}

func NewBlockFromStorage(hash types.Hash, header *Header, body *Body) *Block {
	b := &Block{header: header, body: body}
	b.hash.Store(hash)
	return b
}

type RawBody struct {
	Transactions [][]byte
}
