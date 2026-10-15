package hotstuff

import (
	"bytes"
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

// ValidateUnsignedCandidateHeader checks a leader-prepared template using only
// public consensus data. It never prepares, signs or proposes for that leader.
func (h *HotStuff) ValidateUnsignedCandidateHeader(chain consensus.ChainHeaderReader, header *block.Header) error {
	if chain == nil || chain.Config() == nil || chain.Config().Performance == nil || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() || h.config == nil {
		return errors.New("unsigned candidate requires explicit performance header")
	}
	if header.Root != (types.Hash{}) || header.ReceiptHash != (types.Hash{}) || header.Bloom != (block.Bloom{}) || header.GasUsed != 0 {
		return errors.New("unsigned candidate execution fields must be unset")
	}
	if header.WithdrawalsHash != nil || header.BlobGasUsed != nil || header.ExcessBlobGas != nil || header.RequestsHash != nil || header.BlockAccessListHash != nil {
		return errors.New("unsigned candidate result fields must be unset")
	}
	if header.Difficulty == nil || !header.Difficulty.IsZero() || header.Nonce != (block.BlockNonce{}) {
		return errors.New("unsigned candidate HotStuff difficulty/nonce must be zero")
	}
	view, qc, reserved, err := decodeHeaderExtra(header.Extra)
	if err != nil || !reserved || view == 0 {
		return errors.New("unsigned candidate requires prepared view and reserved seal")
	}
	encoded, err := buildHeaderExtra(view, qc)
	if err != nil || !bytes.Equal(encoded, header.Extra) {
		return errors.New("unsigned candidate must use canonical extra with zero BLS seal")
	}
	ce := h.Engine()
	if ce == nil {
		return errors.New("unsigned candidate public verifier is not initialized")
	}
	if h.config.InteropV4 || (h.config.CommitteePool != nil && h.config.CommitteePool.Enabled) || (h.config.EpochLength > 0 && uint64(view) >= h.config.EpochLength) {
		return errors.New("unsigned candidate consensus domain/epoch adapter is not wired")
	}
	vs := ce.ValidatorSetForView(view)
	if vs == nil || vs.IsEmpty() {
		return errors.New("unsigned candidate validator set unavailable")
	}
	proposer, err := vs.GetAddress(LeaderForView(view, vs))
	if err != nil || proposer != header.Coinbase {
		return errors.New("unsigned candidate proposer does not match view leader")
	}
	if err := h.VerifyHeader(chain, header, false); err != nil {
		return err
	}
	parent, err := chain.GetHeaderByHash(header.ParentHash)
	ph, ok := parent.(*block.Header)
	if err != nil || !ok || ph == nil {
		return errors.New("unsigned candidate prepared parent unavailable")
	}
	period := h.config.Period
	if period == 0 {
		period = 3
	}
	if header.Time != ph.Time+period {
		return errors.New("unsigned candidate timestamp must match leader Prepare")
	}
	return nil
}
