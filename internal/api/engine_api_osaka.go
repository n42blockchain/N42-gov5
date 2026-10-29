package api

// EIP-7594 (PeerDAS) Engine API methods introduced with Osaka:
// engine_getPayloadV5 and engine_getBlobsV2.
// Reference: https://github.com/ethereum/execution-apis/blob/main/src/engine/osaka.md

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// maxGetBlobsRequest is the largest getBlobs request served; larger requests
// are answered with -38004 (Too large request).
const maxGetBlobsRequest = 128

// BlobsBundleV2 is the Osaka blobs bundle: Proofs holds CELLS_PER_EXT_BLOB
// cell proofs per blob, blob after blob, instead of one blob proof per blob.
type BlobsBundleV2 struct {
	Commitments []hexutil.Bytes `json:"commitments"`
	Proofs      []hexutil.Bytes `json:"proofs"`
	Blobs       []hexutil.Bytes `json:"blobs"`
}

// GetPayloadResponseV5 is the response for engine_getPayloadV5.
type GetPayloadResponseV5 struct {
	ExecutionPayload      *ExecutionPayloadV4 `json:"executionPayload"`
	BlockValue            *hexutil.Big        `json:"blockValue"`
	BlobsBundle           *BlobsBundleV2      `json:"blobsBundle"`
	ShouldOverrideBuilder bool                `json:"shouldOverrideBuilder"`
	ExecutionRequests     []hexutil.Bytes     `json:"executionRequests"`
}

// BlobAndProofV2 is a blob with its cell proofs (engine_getBlobsV2).
type BlobAndProofV2 struct {
	Blob       hexutil.Bytes   `json:"blob"`
	CellProofs []hexutil.Bytes `json:"proofs"`
}

// GetPayloadV5 retrieves an Osaka payload; its blobs bundle carries cell
// proofs. engine_getPayloadV5
func (e *EngineAPIv4) GetPayloadV5(ctx context.Context, payloadID PayloadID) (*GetPayloadResponseV5, error) {
	built := e.v1.builtPayload(payloadID)
	if built == nil || built.v4 == nil {
		return nil, errPayloadNotFound
	}
	if !builtPayloadForkIn(e.v1.chainConfig(), built, engineForkOsaka) {
		return nil, &engineUnsupportedForkError{msg: "getPayloadV5 called for a payload outside Osaka"}
	}
	bundle, err := blobsBundleV2(built.blobsBundle)
	if err != nil {
		return nil, err
	}
	return &GetPayloadResponseV5{
		ExecutionPayload:      built.v4,
		BlockValue:            enginePayloadBlockValue(built.blockValue),
		BlobsBundle:           bundle,
		ShouldOverrideBuilder: false,
		ExecutionRequests:     cloneHexutilBytesList(built.executionRequests),
	}, nil
}

// blobsBundleV2 presents a built payload's bundle as BlobsBundleV2. The
// builder only includes version-1 sidecars in an Osaka payload, so the bundle
// must hold exactly CellProofsPerBlob proofs per blob; anything else would
// let a consensus client publish data columns it cannot prove.
func blobsBundleV2(bundle *BlobsBundleV1) (*BlobsBundleV2, error) {
	if bundle == nil {
		bundle = emptyBlobsBundle()
	}
	if len(bundle.Commitments) != len(bundle.Blobs) || len(bundle.Proofs) != len(bundle.Blobs)*transaction.CellProofsPerBlob {
		return nil, fmt.Errorf("built payload blobs bundle is not in cell-proof form: %d blobs, %d commitments, %d proofs",
			len(bundle.Blobs), len(bundle.Commitments), len(bundle.Proofs))
	}
	return &BlobsBundleV2{
		Commitments: cloneHexutilBytesList(bundle.Commitments),
		Proofs:      cloneHexutilBytesList(bundle.Proofs),
		Blobs:       cloneHexutilBytesList(bundle.Blobs),
	}, nil
}

// GetBlobsV2 returns blobs with their cell proofs by versioned hash, from the
// transaction pool (version-1 sidecars) and from built Osaka payloads.
// engine_getBlobsV2
//
// Per spec the response is all or nothing: if any requested blob is missing,
// or only held with a version-0 (blob proof) sidecar, the result is null.
// Before Osaka the method also answers null, as geth does.
func (e *EngineAPIv4) GetBlobsV2(ctx context.Context, versionedHashes []types.Hash) ([]*BlobAndProofV2, error) {
	if len(versionedHashes) > maxGetBlobsRequest {
		return nil, &engineTooLargeRequestError{msg: fmt.Sprintf("requested blob count too large: %d", len(versionedHashes))}
	}
	head := blockHeader(e.v1.currentHead())
	if head == nil || head.Number64() == nil ||
		!engineForkIn(e.v1.chainConfig(), head.Number64().Uint64(), head.Time, engineForkOsaka) {
		return nil, nil
	}
	wanted := make(map[types.Hash]*BlobAndProofV2, len(versionedHashes))
	for _, h := range versionedHashes {
		wanted[h] = nil
	}
	found := 0
	take := func(h types.Hash, blob hexutil.Bytes, proofs []hexutil.Bytes) {
		if entry, ok := wanted[h]; ok && entry == nil {
			wanted[h] = &BlobAndProofV2{Blob: blob, CellProofs: proofs}
			found++
		}
	}

	if e.api != nil && e.api.api != nil && e.api.api.txspool != nil {
		if txs, err := e.api.api.txspool.GetTransaction(); err == nil {
			for _, tx := range txs {
				if found == len(wanted) {
					break
				}
				if tx == nil || tx.Type() != transaction.BlobTxType {
					continue
				}
				sc := tx.BlobTxSidecar()
				hashes := tx.BlobHashes()
				if sc == nil || sc.Version != transaction.BlobSidecarVersion1 || len(sc.Blobs) != len(hashes) {
					continue
				}
				for i, h := range hashes {
					if _, ok := wanted[h]; !ok {
						continue
					}
					cellProofs, err := sc.CellProofsAt(i)
					if err != nil {
						continue
					}
					proofs := make([]hexutil.Bytes, len(cellProofs))
					for j := range cellProofs {
						proofs[j] = append(hexutil.Bytes(nil), cellProofs[j][:]...)
					}
					take(h, append(hexutil.Bytes(nil), sc.Blobs[i][:]...), proofs)
				}
			}
		}
	}

	if overlay := e.v1.overlay(); overlay != nil && found < len(wanted) {
		overlay.mu.RLock()
		for _, built := range overlay.builtPayloads {
			if built == nil || built.blobsBundle == nil {
				continue
			}
			bundle := built.blobsBundle
			if len(bundle.Commitments) != len(bundle.Blobs) || len(bundle.Proofs) != len(bundle.Blobs)*transaction.CellProofsPerBlob {
				continue // not a cell-proof (Osaka) bundle
			}
			for i := range bundle.Blobs {
				if len(bundle.Commitments[i]) != 48 {
					continue
				}
				start := i * transaction.CellProofsPerBlob
				take(computeVersionedHash(bundle.Commitments[i]), bundle.Blobs[i],
					cloneHexutilBytesList(bundle.Proofs[start:start+transaction.CellProofsPerBlob]))
			}
		}
		overlay.mu.RUnlock()
	}

	if found != len(wanted) {
		return nil, nil
	}
	results := make([]*BlobAndProofV2, len(versionedHashes))
	for i, h := range versionedHashes {
		results[i] = wanted[h]
	}
	return results, nil
}

// blobTxIncludableInPayload reports whether the payload builder may include
// tx in a payload of the given fork. A blob transaction needs its sidecar,
// or the consensus client could not publish the blobs, and the sidecar must
// have the payload fork's version (EIP-7594: version 1 from Osaka, 0 before),
// so the payload's blobs bundle is entirely in the form getPayload returns.
func blobTxIncludableInPayload(tx *transaction.Transaction, osaka bool) bool {
	if tx == nil || tx.Type() != transaction.BlobTxType {
		return true
	}
	sc := tx.BlobTxSidecar()
	if sc == nil {
		return false
	}
	if osaka {
		return sc.Version == transaction.BlobSidecarVersion1
	}
	return sc.Version == transaction.BlobSidecarVersion0
}
