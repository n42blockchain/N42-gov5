package hotstuff

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal/consensus"
)

// VerifyPerformanceCommitQC uses the Commit domain exclusively. The legacy
// domain signs phase/view/hash, not chain identity; RPC and stored ancestry
// provide identity binding here. H2-v4 and validator-set changes are separate.
func (h *HotStuff) VerifyPerformanceCommitQC(qc *QuorumCertificate) error {
	if h.chainConfig == nil || h.chainConfig.Performance == nil || h.config == nil || len(h.config.Validators) != 7 || qc == nil || qc.View == 0 || qc.BlockHash == (types.Hash{}) || len(qc.Signers) != 7 || len(qc.AggregateSignature) != 96 {
		return errors.New("performance commit requires fixed seven-validator certificate")
	}
	if h.config.InteropV4 || (h.config.CommitteePool != nil && h.config.CommitteePool.Enabled) || (h.config.EpochLength > 0 && uint64(qc.View) >= h.config.EpochLength) {
		return errors.New("performance commit domain/epoch adapter is not wired")
	}
	ce := h.Engine()
	if ce == nil {
		return errors.New("performance commit verifier is not initialized")
	}
	set := ce.ValidatorSetForView(qc.View)
	if set == nil || set.Len() != 7 {
		return errors.New("performance commit genesis set unavailable")
	}
	for i, validator := range h.config.Validators {
		address, err := set.GetAddress(ValidatorIndex(i))
		if err != nil || address != types.HexToAddress(validator.Address) {
			return errors.New("performance commit validator set changed from genesis")
		}
		key, err := set.GetPublicKey(ValidatorIndex(i))
		if err != nil {
			return err
		}
		expected, err := hex.DecodeString(strings.TrimPrefix(validator.BLSKey, "0x"))
		if err != nil || !bytes.Equal(key.Marshal(), expected) {
			return errors.New("performance commit validator keys changed from genesis")
		}
	}
	return ce.VerifyCommitQCWithResolvedSet(qc)
}

func (h *HotStuff) VerifyPerformanceCommitCertificate(chain consensus.ChainHeaderReader, header *block.Header, qc *QuorumCertificate) error {
	if err := h.VerifyPerformanceCommitQC(qc); err != nil {
		return err
	}
	if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() || header.Hash() != qc.BlockHash {
		return errors.New("certified committed header missing or mismatched")
	}
	view, _, _, err := decodeHeaderExtra(header.Extra)
	if err != nil || view > qc.View {
		return errors.New("certified committed header view exceeds proof")
	}
	return h.VerifyPerformanceSealedHeader(chain, header)
}

// VerifyPerformanceSealedHeader validates an available proposal without
// granting finality. The lightweight validator uses this before remote import.
func (h *HotStuff) VerifyPerformanceSealedHeader(chain consensus.ChainHeaderReader, header *block.Header) error {
	if chain == nil || h.chainConfig == nil || h.chainConfig.Performance == nil || h.config == nil || len(h.config.Validators) != 7 || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() {
		return errors.New("sealed performance header requires fixed seven-validator chain")
	}
	view, embedded, reserved, err := decodeHeaderExtra(header.Extra)
	if err != nil || !reserved || view == 0 || (h.config.EpochLength > 0 && uint64(view) >= h.config.EpochLength) {
		return errors.New("certified committed header view/seal malformed")
	}
	canonical, err := buildHeaderExtra(view, embedded)
	if err != nil || len(canonical) != len(header.Extra) || !bytes.Equal(canonical[:len(canonical)-96], header.Extra[:len(header.Extra)-96]) {
		return errors.New("certified committed header extra malformed")
	}
	ce := h.Engine()
	if ce == nil {
		return errors.New("sealed performance header verifier not initialized")
	}
	vs := ce.ValidatorSetForView(view)
	if vs == nil || vs.Len() != 7 {
		return errors.New("certified committed header set unavailable")
	}
	index := LeaderForView(view, vs)
	address, err := vs.GetAddress(index)
	if err != nil || address != header.Coinbase {
		return errors.New("certified committed header proposer mismatch")
	}
	key, err := vs.GetPublicKey(index)
	if err != nil {
		return err
	}
	signature, err := bls.SignatureFromBytes(header.Extra[len(header.Extra)-96:])
	if err != nil {
		return errors.New("certified committed header seal malformed")
	}
	digest := h.SealHash(header)
	if !signature.Verify(key, digest[:]) {
		return errors.New("certified committed header seal invalid")
	}
	return h.VerifyHeader(chain, header, true)
}
