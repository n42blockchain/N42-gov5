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
// Transaction Message type — a fully derived tx implementing the
// core.Message contract used by EVM state transitions. Captures
// from / to, nonce, amount, gas limit, gas price, fee cap, tip,
// blob fee cap, blob hashes, calldata, access list, authorization
// list, checkNonce flag and isFree marker. NewMessage is the
// constructor called by the signer once V/R/S have been verified.

package transaction

import (
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// Message is a fully derived transaction and implements core.Message
type Message struct {
	to         *types.Address
	from       types.Address
	nonce      uint64
	amount     uint256.Int
	gasLimit   uint64
	gasPrice   uint256.Int
	feeCap     uint256.Int
	tip        uint256.Int
	blobFeeCap *uint256.Int
	blobHashes []types.Hash
	data       []byte
	accessList AccessList
	authList   AuthorizationList
	checkNonce bool
	isFree     bool
}

func NewMessage(from types.Address, to *types.Address, nonce uint64, amount *uint256.Int, gasLimit uint64, gasPrice *uint256.Int, feeCap, tip, blobFeeCap *uint256.Int, blobHashes []types.Hash, data []byte, accessList AccessList, checkNonce bool, isFree bool) Message {
	m := Message{
		from:       from,
		to:         to,
		nonce:      nonce,
		amount:     *amount,
		gasLimit:   gasLimit,
		blobHashes: append([]types.Hash(nil), blobHashes...),
		data:       data,
		accessList: accessList,
		checkNonce: checkNonce,
		isFree:     isFree,
	}
	if gasPrice != nil {
		m.gasPrice.Set(gasPrice)
	}
	if tip != nil {
		m.tip.Set(tip)
	}
	if feeCap != nil {
		m.feeCap.Set(feeCap)
	}
	if blobFeeCap != nil {
		m.blobFeeCap = new(uint256.Int).Set(blobFeeCap)
	}
	return m
}

// AsMessage returns the transaction as a core.Message.
func (tx *Transaction) AsMessage(s Signer, baseFee *uint256.Int) (Message, error) {
	msg := Message{
		nonce:      tx.Nonce(),
		gasLimit:   tx.Gas(),
		gasPrice:   copyUint256OrZero(tx.GasPrice()),
		feeCap:     copyUint256OrZero(tx.GasFeeCap()),
		tip:        copyUint256OrZero(tx.GasTipCap()),
		blobHashes: append([]types.Hash(nil), tx.BlobHashes()...),
		to:         tx.To(),
		amount:     copyUint256OrZero(tx.Value()),
		data:       tx.Data(),
		accessList: tx.AccessList(),
		authList:   tx.AuthList(),
		checkNonce: false,
	}
	if blobFeeCap := tx.BlobFeeCap(); blobFeeCap != nil {
		msg.blobFeeCap = new(uint256.Int).Set(blobFeeCap)
	}

	if baseFee != nil {
		msg.gasPrice.Add(&msg.tip, baseFee)
		if msg.gasPrice.Gt(&msg.feeCap) {
			msg.gasPrice = msg.feeCap
		}
	}
	if from := tx.From(); from != nil {
		msg.from = *from
		return msg, nil
	}
	if s == nil {
		return Message{}, fmt.Errorf("missing signer for sender derivation")
	}
	from, err := Sender(s, tx)
	if err != nil {
		return Message{}, err
	}
	msg.from = from

	return msg, nil
}

func copyUint256OrZero(v *uint256.Int) uint256.Int {
	var out uint256.Int
	if v != nil {
		out.Set(v)
	}
	return out
}

func (m Message) From() types.Address         { return m.from }
func (m Message) To() *types.Address          { return m.to }
func (m Message) GasPrice() *uint256.Int      { return &m.gasPrice }
func (m Message) FeeCap() *uint256.Int        { return &m.feeCap }
func (m Message) Tip() *uint256.Int           { return &m.tip }
func (m Message) BlobFeeCap() *uint256.Int    { return m.blobFeeCap }
func (m Message) BlobHashes() []types.Hash    { return m.blobHashes }
func (m Message) Value() *uint256.Int         { return &m.amount }
func (m Message) Gas() uint64                 { return m.gasLimit }
func (m Message) Nonce() uint64               { return m.nonce }
func (m Message) Data() []byte                { return m.data }
func (m Message) AccessList() AccessList      { return m.accessList }
func (m Message) AuthList() AuthorizationList { return m.authList }
func (m Message) CheckNonce() bool            { return m.checkNonce }
func (m Message) IsFree() bool                { return m.isFree }

// ExecutionValues exposes stable pointers to the four uint256 values consumed
// throughout one EVM transaction. The ordinary Message interface uses value-
// receiver getters for compatibility; taking a field address from those
// getters makes a temporary Message copy escape on every call. Hot execution
// paths pass *Message and use this optional view to avoid those copies.
// Callers must treat the returned values as read-only.
func (m *Message) ExecutionValues() (gasPrice, feeCap, tip, value *uint256.Int) {
	return &m.gasPrice, &m.feeCap, &m.tip, &m.amount
}

func (m *Message) SetCheckNonce(checkNonce bool) {
	m.checkNonce = checkNonce
}

func (m *Message) SetIsFree(isFree bool) {
	m.isFree = isFree
}
