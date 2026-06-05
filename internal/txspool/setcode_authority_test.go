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

package txspool

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// newAuthTestPool builds a minimal pool exercising only the fields validateAuth
// touches: chain config (signer), state reader, the all-lookup, pending/queue
// maps and the pending-nonce cache.
func newAuthTestPool() (*TxsPool, *mockReadState) {
	mock := newMockReadState()
	pool := &TxsPool{
		chainconfig:   &params.ChainConfig{ChainID: big.NewInt(1)},
		currentState:  mock,
		all:           newTxLookup(),
		pending:       make(map[types.Address]*txsList),
		queue:         make(map[types.Address]*txsList),
		pendingNonces: newTxNoncer(mock),
	}
	return pool, mock
}

// signedSetCodeTx returns a SetCode tx signed by senderKey (outer) whose single
// authorization is signed by authKey.
func signedSetCodeTx(t *testing.T, senderKey, authKey *ecdsa.PrivateKey, nonce uint64) *transaction.Transaction {
	t.Helper()
	auth := &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: types.HexToAddress("0x00000000000000000000000000000000000000ff"),
		Nonce:   0,
	}
	sh := auth.SigningHash()
	sig, err := crypto.Sign(sh[:], authKey)
	if err != nil {
		t.Fatalf("sign auth: %v", err)
	}
	auth.R = uint256.NewInt(0).SetBytes(sig[:32])
	auth.S = uint256.NewInt(0).SetBytes(sig[32:64])
	auth.V = uint256.NewInt(uint64(sig[64]))

	to := types.HexToAddress("0x00000000000000000000000000000000000000aa")
	inner := &transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(0),
		AuthList:  transaction.AuthorizationList{auth},
	}
	signed, err := transaction.SignNewTx(senderKey, transaction.LatestSignerForChainID(big.NewInt(1)), inner)
	if err != nil {
		t.Fatalf("sign setcode tx: %v", err)
	}
	return signed
}

// fillPending populates pool.pending[addr] with n signed legacy txs (nonces
// 0..n-1) from key, so Len() reflects them.
func fillPending(t *testing.T, key *ecdsa.PrivateKey, addr types.Address, n int) *txsList {
	t.Helper()
	list := newTxsList(true)
	signer := transaction.LatestSignerForChainID(big.NewInt(1))
	to := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	for i := 0; i < n; i++ {
		tx, err := transaction.SignTx(
			transaction.NewTransaction(uint64(i), addr, &to, uint256.NewInt(0), 21000, uint256.NewInt(1), nil),
			signer, key)
		if err != nil {
			t.Fatalf("sign legacy tx: %v", err)
		}
		list.Add(tx, 10)
	}
	return list
}

// setCodeTxWithKey builds a SetCode (EIP-7702) transaction whose single
// authorization is signed by authKey. Using the same key across calls yields
// transactions that share an authority (different nonces → different hashes).
func setCodeTxWithKey(t *testing.T, authKey *ecdsa.PrivateKey, nonce uint64) *transaction.Transaction {
	t.Helper()
	auth := &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: types.HexToAddress("0x00000000000000000000000000000000000000ff"),
		Nonce:   nonce,
	}
	sh := auth.SigningHash()
	sig, err := crypto.Sign(sh[:], authKey)
	if err != nil {
		t.Fatalf("sign auth: %v", err)
	}
	auth.R = uint256.NewInt(0).SetBytes(sig[:32])
	auth.S = uint256.NewInt(0).SetBytes(sig[32:64])
	auth.V = uint256.NewInt(uint64(sig[64]))

	to := types.HexToAddress("0x00000000000000000000000000000000000000aa")
	inner := &transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(0),
		AuthList:  transaction.AuthorizationList{auth},
	}
	return transaction.NewTx(inner)
}

// TestSetCodeAuthoritiesRecovers checks the authority recovery helper returns
// exactly the signing address, and nil for a non-SetCode tx.
func TestSetCodeAuthoritiesRecovers(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	authority := crypto.PubkeyToAddress(key.PublicKey)

	tx := setCodeTxWithKey(t, key, 0)
	got := setCodeAuthorities(tx)
	if len(got) != 1 || got[0] != authority {
		t.Fatalf("setCodeAuthorities = %v, want [%x]", got, authority)
	}

	legacy := transaction.NewTransaction(0, types.Address{}, nil, uint256.NewInt(0), 21000, uint256.NewInt(1), nil)
	if a := setCodeAuthorities(legacy); a != nil {
		t.Fatalf("legacy tx authorities = %v, want nil", a)
	}
}

// TestTxLookupTracksAuthorities checks the txLookup maintains the authority→tx
// index across Add/Remove and that hasAuth reflects it. Two transactions share
// the same authority (signed by one key), so removing one must keep it live.
func TestTxLookupTracksAuthorities(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	authority := crypto.PubkeyToAddress(key.PublicKey)

	lk := newTxLookup()
	if lk.hasAuth(authority) {
		t.Fatal("authority tracked before any Add")
	}

	tx1 := setCodeTxWithKey(t, key, 0)
	tx2 := setCodeTxWithKey(t, key, 1)
	if tx1.Hash() == tx2.Hash() {
		t.Fatal("expected distinct hashes for distinct nonces")
	}

	lk.Add(tx1, false)
	if !lk.hasAuth(authority) {
		t.Fatal("authority not tracked after Add")
	}
	lk.Add(tx2, false)

	// Removing one of two authorizing txs keeps the authority in-flight.
	lk.Remove(tx1.Hash())
	if !lk.hasAuth(authority) {
		t.Fatal("authority dropped while a second authorizing tx is still in-flight")
	}

	// Removing the last clears it entirely.
	lk.Remove(tx2.Hash())
	if lk.hasAuth(authority) {
		t.Fatal("authority still tracked after all authorizing txs removed")
	}
	if len(lk.auths) != 0 {
		t.Fatalf("auths map not emptied: %d entries", len(lk.auths))
	}
}

// TestTxLookupAddDuplicateAuthority checks adding the same tx twice does not
// double-track its authority (defensive against duplicate Add).
func TestTxLookupAddDuplicateAuthority(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	authority := crypto.PubkeyToAddress(key.PublicKey)
	tx := setCodeTxWithKey(t, key, 0)

	lk := newTxLookup()
	lk.Add(tx, false)
	lk.Add(tx, false) // duplicate
	if n := len(lk.auths[authority]); n != 1 {
		t.Fatalf("duplicate Add tracked authority %d times, want 1", n)
	}
	lk.Remove(tx.Hash())
	if lk.hasAuth(authority) {
		t.Fatal("authority still tracked after removing the only tx")
	}
}

// TestValidateAuthRejectsReservedAuthority: a SetCode tx whose authority already
// has >1 in-flight transactions (pending+queue) is rejected — the core anti-spam
// rule (an authority cannot be reserved by stacking).
func TestValidateAuthRejectsReservedAuthority(t *testing.T) {
	pool, _ := newAuthTestPool()

	senderKey, _ := crypto.GenerateKey()
	authKey, _ := crypto.GenerateKey()
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	authAcctKey, _ := crypto.GenerateKey() // owns the txs occupying the authority slot

	// Authority already has 2 in-flight txs → reserved.
	pool.pending[authority] = fillPending(t, authAcctKey, authority, 2)

	tx := signedSetCodeTx(t, senderKey, authKey, 0)
	if err := pool.validateAuth(tx); err != ErrAuthorityReserved {
		t.Fatalf("validateAuth = %v, want ErrAuthorityReserved", err)
	}

	// With only 1 in-flight tx the authority is still claimable.
	pool.pending[authority] = fillPending(t, authAcctKey, authority, 1)
	if err := pool.validateAuth(tx); err != nil {
		t.Fatalf("validateAuth (1 in-flight) = %v, want nil", err)
	}
}

// TestValidateAuthDelegationLimit: a sender that is an in-flight authority may
// keep at most one executable tx — a stacked second tx is rejected, a
// replacement at the same nonce is allowed.
func TestValidateAuthDelegationLimit(t *testing.T) {
	pool, _ := newAuthTestPool()

	senderKey, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)

	// Mark `sender` as an in-flight authority by tracking a SetCode tx that
	// authorizes it (signed by senderKey so the recovered authority == sender).
	authTx := signedSetCodeTx(t, mustKey(t), senderKey, 0)
	pool.all.Add(authTx, false)
	if !pool.all.hasAuth(sender) {
		t.Fatal("sender should be an in-flight authority")
	}

	// Sender already has one pending tx at nonce 5.
	list := newTxsList(true)
	signer := transaction.LatestSignerForChainID(big.NewInt(1))
	to := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	pendingTx, _ := transaction.SignTx(
		transaction.NewTransaction(5, sender, &to, uint256.NewInt(0), 21000, uint256.NewInt(1), nil),
		signer, senderKey)
	list.Add(pendingTx, 10)
	pool.pending[sender] = list

	// A stacked tx at nonce 6 is rejected.
	stacked, _ := transaction.SignTx(
		transaction.NewTransaction(6, sender, &to, uint256.NewInt(0), 21000, uint256.NewInt(2), nil),
		signer, senderKey)
	if err := pool.checkDelegationLimit(stacked); err != ErrInflightTxLimitReached {
		t.Fatalf("stacked tx = %v, want ErrInflightTxLimitReached", err)
	}

	// A replacement at the same nonce 5 is allowed.
	replacement, _ := transaction.SignTx(
		transaction.NewTransaction(5, sender, &to, uint256.NewInt(0), 21000, uint256.NewInt(3), nil),
		signer, senderKey)
	if err := pool.checkDelegationLimit(replacement); err != nil {
		t.Fatalf("replacement tx = %v, want nil", err)
	}
}

// TestValidateAuthIgnoresNormalAccounts: a plain tx from a non-delegated,
// non-authority sender is unaffected.
func TestValidateAuthIgnoresNormalAccounts(t *testing.T) {
	pool, _ := newAuthTestPool()
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	signer := transaction.LatestSignerForChainID(big.NewInt(1))
	to := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	tx, _ := transaction.SignTx(
		transaction.NewTransaction(0, from, &to, uint256.NewInt(0), 21000, uint256.NewInt(1), nil),
		signer, key)
	if err := pool.validateAuth(tx); err != nil {
		t.Fatalf("normal tx validateAuth = %v, want nil", err)
	}
}

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}
