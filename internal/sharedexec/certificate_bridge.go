package sharedexec

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// CertificateBridge forwards a public CommitQC using locally persisted block
// metadata. It holds no owner capability and no validator signing key.
type CertificateBridge struct {
	client *Client
	db     kv.RwDB
}

func NewCertificateBridge(client *Client, db kv.RwDB) (*CertificateBridge, error) {
	if client == nil || db == nil {
		return nil, errors.New("certificate bridge client and metadata store required")
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		genesis, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		cfg, err := rawdb.ReadChainConfig(tx, genesis)
		if err != nil {
			return err
		}
		if genesis == (types.Hash{}) || genesis != client.identity.GenesisHash || cfg == nil || cfg.ChainID == nil || cfg.ChainID.String() != client.identity.ChainID || cfg.Performance == nil || cfg.Performance.NetworkNamespace != client.identity.Namespace {
			return errors.New("certificate bridge local chain identity mismatch")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &CertificateBridge{client: client, db: db}, nil
}

func (b *CertificateBridge) CommitCertificate(ctx context.Context, qc *hotstuff.QuorumCertificate) error {
	if qc == nil {
		return errors.New("certificate bridge requires certificate")
	}
	request := CommitRequest{Identity: b.client.identity, BlockHash: qc.BlockHash}
	if err := b.db.View(ctx, func(tx kv.Tx) error {
		header, err := rawdb.ReadHeaderByHash(tx, qc.BlockHash)
		if err != nil {
			return err
		}
		if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Hash() != qc.BlockHash {
			return errors.New("certificate bridge local committed header missing")
		}
		request.Number, request.ParentHash = header.Number.Uint64(), header.ParentHash
		return nil
	}); err != nil {
		return err
	}
	_, err := b.client.CommitCertified(ctx, request, qc)
	return err
}
