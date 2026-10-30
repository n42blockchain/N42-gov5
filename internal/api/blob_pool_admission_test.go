package api

// blob_pool_admission_test.go covers EIP-7594 blob sidecar admission on the
// RPC ingress (eth_sendRawTransaction, the batch endpoint): from Osaka only
// version-1 sidecars with valid cell proofs, before Osaka only version 0.

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/kzg"
)

// testBlobSidecars holds one blob with its commitment, blob proof and cell
// proofs, computed once: cell proofs cost ~100 ms per blob.
type testBlobSidecars struct {
	blob       transaction.Blob
	commitment transaction.Commitment
	blobProof  transaction.Proof
	cellProofs []transaction.Proof
}

var (
	testBlobSidecarOnce sync.Once
	testBlobSidecarVal  *testBlobSidecars
	testBlobSidecarErr  error
)

func loadTestBlobSidecars(t *testing.T) *testBlobSidecars {
	t.Helper()
	testBlobSidecarOnce.Do(func() {
		s := &testBlobSidecars{}
		// Canonical field elements: the high byte of every element is zero.
		for i := 0; i < kzg.FieldElementsPerBlob; i++ {
			s.blob[i*32+30] = byte(i >> 8)
			s.blob[i*32+31] = byte(i)
		}
		ctx, err := kzg.GetContext()
		if err != nil {
			testBlobSidecarErr = err
			return
		}
		if s.commitment, err = ctx.BlobToCommitment(&s.blob); err != nil {
			testBlobSidecarErr = err
			return
		}
		if s.blobProof, err = ctx.ComputeBlobProof(&s.blob, s.commitment); err != nil {
			testBlobSidecarErr = err
			return
		}
		if s.cellProofs, err = kzg.ComputeCellProofs(&s.blob); err != nil {
			testBlobSidecarErr = err
			return
		}
		testBlobSidecarVal = s
	})
	require.NoError(t, testBlobSidecarErr)
	return testBlobSidecarVal
}

// v0 / v1 return fresh sidecars for the test blob.
func (s *testBlobSidecars) v0() *transaction.BlobTxSidecar {
	return &transaction.BlobTxSidecar{
		Version:     transaction.BlobSidecarVersion0,
		Blobs:       []transaction.Blob{s.blob},
		Commitments: []transaction.Commitment{s.commitment},
		Proofs:      []transaction.Proof{s.blobProof},
	}
}

func (s *testBlobSidecars) v1() *transaction.BlobTxSidecar {
	return &transaction.BlobTxSidecar{
		Version:     transaction.BlobSidecarVersion1,
		Blobs:       []transaction.Blob{s.blob},
		Commitments: []transaction.Commitment{s.commitment},
		Proofs:      append([]transaction.Proof(nil), s.cellProofs...),
	}
}

// signedBlobTx signs a blob transaction for the test blob. The sidecar is not
// part of the signed payload, so it can be attached before signing.
func signedBlobTx(t *testing.T, chainID *big.Int, key *ecdsa.PrivateKey, to types.Address, nonce uint64, s *testBlobSidecars, sidecar *transaction.BlobTxSidecar) *transaction.Transaction {
	t.Helper()
	inner := &transaction.BlobTx{
		ChainID:    uint256.MustFromBig(chainID),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		To:         to,
		Value:      new(uint256.Int),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{kzg.CommitmentToVersionedHash(s.commitment)},
		Sidecar:    sidecar,
	}
	signed, err := transaction.SignTx(transaction.NewTx(inner), transaction.NewLondonSigner(chainID), key)
	require.NoError(t, err)
	return signed
}

// signedBlobTxRaw returns the pooled encoding of signedBlobTx: the network
// form when a sidecar is attached, the canonical form otherwise.
func signedBlobTxRaw(t *testing.T, chainID *big.Int, key *ecdsa.PrivateKey, to types.Address, nonce uint64, s *testBlobSidecars, sidecar *transaction.BlobTxSidecar) hexutil.Bytes {
	t.Helper()
	raw, err := transaction.EncodeEthereumPooledTransaction(signedBlobTx(t, chainID, key, to, nonce, s, sidecar))
	require.NoError(t, err)
	return raw
}

func blobAdmissionAPI(t *testing.T, osaka bool) (*TransactionAPI, *apiXChainFixture) {
	t.Helper()
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	cfg := *fx.Config
	if osaka {
		cfg.OsakaTime = big.NewInt(0)
	} else {
		cfg.OsakaTime = nil
	}
	api.chainConfig = &cfg
	api.txspool = &mockEngineTxPool{}
	return NewTransactionAPI(api, new(AddrLocker)), fx
}

func TestSendRawTransactionBlobSidecarAdmissionOsaka(t *testing.T) {
	s := loadTestBlobSidecars(t)
	txAPI, fx := blobAdmissionAPI(t, true)
	chainID, key, to := fx.Config.ChainID, fx.SenderKeys[0], fx.Senders[1]
	ctx := context.Background()

	_, err := txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 200, s, s.v1()))
	require.NoError(t, err, "valid version-1 sidecar must be accepted at Osaka")

	tampered := s.v1()
	tampered.Proofs[5][7] ^= 0x01
	_, err = txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 201, s, tampered))
	require.Error(t, err, "tampered cell proof must be rejected")

	// A version-0 sidecar is converted to version 1, as geth does, and the
	// pool receives the cell proofs.
	pool := &recordingTxPool{}
	txAPI.api.txspool = pool
	_, err = txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 202, s, s.v0()))
	require.NoError(t, err, "version-0 sidecar must be converted at Osaka")
	require.Len(t, pool.added, 1)
	got := pool.added[0].BlobTxSidecar()
	require.Equal(t, transaction.BlobSidecarVersion1, got.Version)
	require.Equal(t, s.cellProofs, got.Proofs)
	txAPI.api.txspool = &mockEngineTxPool{}

	// The conversion recomputes proofs from the blobs; a commitment that
	// does not match them is still refused.
	wrongCommitment := s.v0()
	wrongCommitment.Commitments[0][9] ^= 0x01
	_, err = txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 206, s, wrongCommitment))
	require.Error(t, err, "converted sidecar with a wrong commitment must be rejected")

	_, err = txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 203, s, nil))
	require.Error(t, err, "blob tx without sidecar must be rejected")

	// The batch endpoint applies the same admission rule per entry.
	hs, err := txAPI.BatchRawTransaction(ctx, []hexutil.Bytes{
		signedBlobTxRaw(t, chainID, key, to, 204, s, s.v1()),
		signedBlobTxRaw(t, chainID, key, to, 205, s, tampered),
	})
	require.Error(t, err, "batch with a tampered cell proof must report it")
	require.Len(t, hs, 2)
	require.NotEqual(t, hs[0], hs[1], "only the valid entry gets a hash")
}

func TestSendRawTransactionBlobSidecarAdmissionPreOsaka(t *testing.T) {
	s := loadTestBlobSidecars(t)
	txAPI, fx := blobAdmissionAPI(t, false)
	chainID, key, to := fx.Config.ChainID, fx.SenderKeys[0], fx.Senders[1]
	ctx := context.Background()

	_, err := txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 300, s, s.v0()))
	require.NoError(t, err, "valid version-0 sidecar must be accepted before Osaka")

	_, err = txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 301, s, s.v1()))
	require.Error(t, err, "version-1 sidecar must be rejected before Osaka")

	bad := s.v0()
	bad.Proofs[0][3] ^= 0x01
	_, err = txAPI.SendRawTransaction(ctx, signedBlobTxRaw(t, chainID, key, to, 302, s, bad))
	require.Error(t, err, "invalid blob proof must be rejected")
}

// recordingTxPool records the transactions submitted to it.
type recordingTxPool struct {
	mockEngineTxPool
	added []*transaction.Transaction
}

func (p *recordingTxPool) AddLocal(tx *transaction.Transaction) error {
	p.added = append(p.added, tx)
	return nil
}
