package transaction

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

func TestDecodeEthereumBlobTransactionNetworkWrapper(t *testing.T) {
	to := types.HexToAddress("0x1111111111111111111111111111111111111111")
	hash := types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001")
	wrapper := blobTxNetworkWrapperRLP{
		Tx: blobTxRLP{
			ChainID:    uint256.NewInt(1),
			GasTipCap:  uint256.NewInt(2),
			GasFeeCap:  uint256.NewInt(3),
			Gas:        21_000,
			To:         to,
			Value:      new(uint256.Int),
			BlobFeeCap: uint256.NewInt(4),
			BlobHashes: []types.Hash{hash},
			V:          new(uint256.Int),
			R:          uint256.NewInt(5),
			S:          uint256.NewInt(6),
		},
		Blobs:       []Blob{{1}},
		Commitments: []Commitment{{2}},
		Proofs:      []Proof{{3}},
	}
	payload, err := rlp.EncodeToBytes(&wrapper)
	if err != nil {
		t.Fatalf("encode wrapper: %v", err)
	}
	tx, err := DecodeEthereumTransaction(append([]byte{BlobTxType}, payload...))
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction() error = %v", err)
	}
	if tx.Type() != BlobTxType || len(tx.BlobHashes()) != 1 {
		t.Fatalf("decoded transaction = type %d, hashes %d", tx.Type(), len(tx.BlobHashes()))
	}
	sidecar := tx.BlobTxSidecar()
	if sidecar == nil || len(sidecar.Blobs) != 1 || sidecar.Blobs[0][0] != 1 {
		t.Fatalf("decoded sidecar = %#v", sidecar)
	}
}

func TestEthereumTransactionRoundTrip(t *testing.T) {
	to := types.HexToAddress("0x1111111111111111111111111111111111111111")
	blobTo := types.HexToAddress("0x2222222222222222222222222222222222222222")
	delegateTo := types.HexToAddress("0x3333333333333333333333333333333333333333")
	storageKey := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	blobHash := types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001")
	maxAuthChainID := new(uint256.Int).SetBytes(bytes.Repeat([]byte{0xff}, 32))

	tests := []struct {
		name string
		tx   *Transaction
	}{
		{
			name: "access-list",
			tx: NewTx(&AccessListTx{
				ChainID:  uint256.NewInt(1),
				Nonce:    1,
				GasPrice: uint256.NewInt(10),
				Gas:      21500,
				To:       &to,
				Value:    uint256.NewInt(19),
				Data:     []byte{0x09, 0x0a},
				AccessList: AccessList{{
					Address:     to,
					StorageKeys: []types.Hash{storageKey},
				}},
				V: uint256.NewInt(0),
				R: uint256.NewInt(2),
				S: uint256.NewInt(3),
			}),
		},
		{
			name: "legacy",
			tx: NewTx(&LegacyTx{
				Nonce:    1,
				GasPrice: uint256.NewInt(10),
				Gas:      21000,
				To:       &to,
				Value:    uint256.NewInt(20),
				Data:     []byte{0x01, 0x02},
				V:        uint256.NewInt(37),
				R:        uint256.NewInt(2),
				S:        uint256.NewInt(3),
			}),
		},
		{
			name: "dynamic-fee",
			tx: NewTx(&DynamicFeeTx{
				ChainID:   uint256.NewInt(1),
				Nonce:     2,
				GasTipCap: uint256.NewInt(3),
				GasFeeCap: uint256.NewInt(30),
				Gas:       22000,
				To:        &to,
				Value:     uint256.NewInt(21),
				Data:      []byte{0x03, 0x04},
				AccessList: AccessList{{
					Address:     to,
					StorageKeys: []types.Hash{storageKey},
				}},
				V: uint256.NewInt(1),
				R: uint256.NewInt(4),
				S: uint256.NewInt(5),
			}),
		},
		{
			name: "blob",
			tx: NewTx(&BlobTx{
				ChainID:    uint256.NewInt(1),
				Nonce:      3,
				GasTipCap:  uint256.NewInt(4),
				GasFeeCap:  uint256.NewInt(40),
				Gas:        23000,
				To:         blobTo,
				Value:      uint256.NewInt(22),
				Data:       []byte{0x05, 0x06},
				BlobFeeCap: uint256.NewInt(7),
				BlobHashes: []types.Hash{blobHash},
				V:          uint256.NewInt(1),
				R:          uint256.NewInt(6),
				S:          uint256.NewInt(7),
			}),
		},
		{
			name: "setcode",
			tx: NewTx(&SetCodeTx{
				ChainID:   uint256.NewInt(1),
				Nonce:     4,
				GasTipCap: uint256.NewInt(5),
				GasFeeCap: uint256.NewInt(50),
				Gas:       24000,
				To:        &to,
				Value:     uint256.NewInt(23),
				Data:      []byte{0x07, 0x08},
				AccessList: AccessList{{
					Address:     to,
					StorageKeys: []types.Hash{storageKey},
				}},
				AuthList: AuthorizationList{{
					ChainID: *maxAuthChainID,
					Address: delegateTo,
					Nonce:   9,
					V:       uint256.NewInt(1),
					R:       uint256.NewInt(8),
					S:       uint256.NewInt(9),
				}},
				V: uint256.NewInt(1),
				R: uint256.NewInt(10),
				S: uint256.NewInt(11),
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := EncodeEthereumTransaction(tt.tx)
			if err != nil {
				t.Fatalf("EncodeEthereumTransaction() error = %v", err)
			}

			decoded, err := DecodeEthereumTransaction(enc)
			if err != nil {
				t.Fatalf("DecodeEthereumTransaction() error = %v", err)
			}

			assertTransactionEquivalent(t, decoded, tt.tx)
		})
	}
}

func TestEncodeEthereumTransactionRejectsUnsupportedType(t *testing.T) {
	tx := NewTx(&PostQuantumTx{
		ChainID: uint256.NewInt(1),
		Nonce:   1,
		Gas:     21000,
		SigAlgo: PQAlgoFalcon512,
	})
	if _, err := EncodeEthereumTransaction(tx); err == nil {
		t.Fatal("EncodeEthereumTransaction() error = nil, want unsupported type error")
	}
}

func TestDecodeEthereumTransactionPreservesMaxAuthorizationChainID(t *testing.T) {
	raw := hexutil.MustDecode("0x04f8e101808007830186a0944c87531476694224c79671e2b844c2ab24c886cf8080c0f87cf87aa0ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff944c87531476694224c79671e2b844c2ab24c885cf8001a08625888bf53285bd4d5ce133807c1fb66379dc1e90e58abe967536b684ebc423a03d78ea4353faec9eea32c2f96e6949d27371692491ad4a6adee93ded801653f680a01b8de583bb5be1de409ac4be6bb24983171a87aa0e1ce53723bceadf323ae755a00a20f06aacc0082260e195978bda197ab84987184bb11e3e7b86b433f414d253")

	tx, err := DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction() error = %v", err)
	}
	if len(tx.AuthList()) != 1 {
		t.Fatalf("AuthList() len = %d, want 1", len(tx.AuthList()))
	}
	want := new(uint256.Int).SetBytes(bytes.Repeat([]byte{0xff}, 32))
	if tx.AuthList()[0].ChainID.Cmp(want) != 0 {
		t.Fatalf("AuthList()[0].ChainID = %s, want %s", &tx.AuthList()[0].ChainID, want)
	}
}

func assertTransactionEquivalent(t *testing.T, got, want *Transaction) {
	t.Helper()

	if got.Type() != want.Type() {
		t.Fatalf("Type() = %d, want %d", got.Type(), want.Type())
	}
	if got.Nonce() != want.Nonce() {
		t.Fatalf("Nonce() = %d, want %d", got.Nonce(), want.Nonce())
	}
	if got.Gas() != want.Gas() {
		t.Fatalf("Gas() = %d, want %d", got.Gas(), want.Gas())
	}
	if got.GasPrice().Cmp(want.GasPrice()) != 0 {
		t.Fatalf("GasPrice() = %s, want %s", got.GasPrice(), want.GasPrice())
	}
	if got.GasTipCap().Cmp(want.GasTipCap()) != 0 {
		t.Fatalf("GasTipCap() = %s, want %s", got.GasTipCap(), want.GasTipCap())
	}
	if got.GasFeeCap().Cmp(want.GasFeeCap()) != 0 {
		t.Fatalf("GasFeeCap() = %s, want %s", got.GasFeeCap(), want.GasFeeCap())
	}
	if got.Value().Cmp(want.Value()) != 0 {
		t.Fatalf("Value() = %s, want %s", got.Value(), want.Value())
	}
	if string(got.Data()) != string(want.Data()) {
		t.Fatalf("Data() = %x, want %x", got.Data(), want.Data())
	}
	if !equalAddressPtr(got.To(), want.To()) {
		t.Fatalf("To() = %v, want %v", got.To(), want.To())
	}
	if len(got.AccessList()) != len(want.AccessList()) {
		t.Fatalf("AccessList() len = %d, want %d", len(got.AccessList()), len(want.AccessList()))
	}
	for i := range got.AccessList() {
		if got.AccessList()[i].Address != want.AccessList()[i].Address {
			t.Fatalf("AccessList()[%d].Address = %s, want %s", i, got.AccessList()[i].Address, want.AccessList()[i].Address)
		}
		if len(got.AccessList()[i].StorageKeys) != len(want.AccessList()[i].StorageKeys) {
			t.Fatalf("AccessList()[%d].StorageKeys len = %d, want %d", i, len(got.AccessList()[i].StorageKeys), len(want.AccessList()[i].StorageKeys))
		}
	}
	if len(got.BlobHashes()) != len(want.BlobHashes()) {
		t.Fatalf("BlobHashes() len = %d, want %d", len(got.BlobHashes()), len(want.BlobHashes()))
	}
	for i := range got.BlobHashes() {
		if got.BlobHashes()[i] != want.BlobHashes()[i] {
			t.Fatalf("BlobHashes()[%d] = %s, want %s", i, got.BlobHashes()[i], want.BlobHashes()[i])
		}
	}
	if !equalUint256Ptr(got.BlobFeeCap(), want.BlobFeeCap()) {
		t.Fatalf("BlobFeeCap() = %s, want %s", got.BlobFeeCap(), want.BlobFeeCap())
	}
	if len(got.AuthList()) != len(want.AuthList()) {
		t.Fatalf("AuthList() len = %d, want %d", len(got.AuthList()), len(want.AuthList()))
	}
	for i := range got.AuthList() {
		gotAuth := got.AuthList()[i]
		wantAuth := want.AuthList()[i]
		if gotAuth.ChainID.Cmp(&wantAuth.ChainID) != 0 || gotAuth.Address != wantAuth.Address || gotAuth.Nonce != wantAuth.Nonce {
			t.Fatalf("AuthList()[%d] = %+v, want %+v", i, gotAuth, wantAuth)
		}
	}
}

func equalAddressPtr(a, b *types.Address) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalUint256Ptr(a, b *uint256.Int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Cmp(b) == 0
}
