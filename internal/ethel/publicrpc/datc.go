// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// DATC proof source: eth_getProof at ANY height below the archive's head,
// served from a DATC archive (the archive-plus tier, internal/datc) instead of
// the node's own state. eth-el keeps no historical state and no trie-backed
// proof provider, so without it eth_getProof has nothing real to return for
// a past block.
//
// Every answer is walked from the block's stateRoot before it leaves the node
// when the node holds that header (--publicrpc.datc.verify=header, default);
// "strict" refuses heights whose header the node lacks, "off" serves the
// archive as is (a client verifies against its own header either way).

package publicrpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/datc"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// DATCVerify selects when a DATC proof is checked against the node's header.
type DATCVerify string

const (
	DATCVerifyHeader DATCVerify = "header" // verify when the node has the header, else serve
	DATCVerifyStrict DATCVerify = "strict" // verify, and refuse heights without a header
	DATCVerifyOff    DATCVerify = "off"    // never verify on the node
)

// ParseDATCVerify maps the flag value; unknown values are rejected.
func ParseDATCVerify(s string) (DATCVerify, error) {
	switch v := DATCVerify(s); v {
	case "", DATCVerifyHeader:
		return DATCVerifyHeader, nil
	case DATCVerifyStrict, DATCVerifyOff:
		return v, nil
	}
	return "", fmt.Errorf("--publicrpc.datc.verify: %q is not header|strict|off", s)
}

// datcSource adapts a DATC archive to api.ProofSource.
type datcSource struct {
	archive *datc.Archive
	verify  DATCVerify
}

func (d *datcSource) ProveAt(ctx context.Context, tx kv.Tx, address types.Address, storageKeys []string, n uint64) (*api.AccountResult, error) {
	var root *types.Hash
	if d.verify != DATCVerifyOff {
		if h := rawdb.ReadHeaderByNumber(tx, n); h != nil {
			r := h.StateRoot()
			root = &r
		} else if d.verify == DATCVerifyStrict {
			return nil, fmt.Errorf("eth_getProof at %d: this node has no header to verify the archive's proof against", n)
		}
	}
	slots := make([]types.Hash, len(storageKeys))
	for i, k := range storageKeys {
		slots[i] = types.HexToHash(k)
	}
	p, err := d.archive.Prove(ctx, address, slots, n, root)
	switch {
	case errors.Is(err, datc.ErrNotCovered):
		return nil, fmt.Errorf("%w: %v", api.ErrProofNotCovered, err)
	case errors.Is(err, datc.ErrProofMismatch):
		log.Error("eth-el: DATC proof does not verify against the header; refusing", "block", n, "address", address, "err", err)
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("eth_getProof at %d from the DATC archive: %w", n, err)
	}
	return toAccountResult(p, storageKeys), nil
}

func hexNodes(nodes [][]byte) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = hexutil.Encode(n)
	}
	return out
}

// toAccountResult shapes a DATC proof as the eth_getProof result. Storage
// keys are echoed as the caller wrote them.
func toAccountResult(p *datc.Proof, storageKeys []string) *api.AccountResult {
	res := &api.AccountResult{
		Address:      p.Address,
		AccountProof: hexNodes(p.AccountProof),
		Balance:      (*hexutil.Big)(p.Balance.ToBig()),
		CodeHash:     p.CodeHash,
		Nonce:        hexutil.Uint64(p.Nonce),
		StorageHash:  p.StorageHash,
		StorageProof: make([]api.StorageResult, len(p.Storage)),
	}
	for i, s := range p.Storage {
		res.StorageProof[i] = api.StorageResult{
			Key:   storageKeys[i],
			Value: (*hexutil.Big)(s.Value.ToBig()),
			Proof: hexNodes(s.Proof),
		}
	}
	return res
}
