package receipt

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/holiman/uint256"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func Recover(hash chain.Hash, signature string) (chain.Address, error) {
	if !strings.HasPrefix(signature, "0x") {
		return chain.Address{}, errors.New("signature must be hex")
	}
	sig, err := hex.DecodeString(signature[2:])
	if err != nil || len(sig) != 65 {
		return chain.Address{}, errors.New("invalid receipt signature length")
	}
	r, s := new(uint256.Int).SetBytes(sig[:32]), new(uint256.Int).SetBytes(sig[32:64])
	if !crypto.ValidateSignatureValues(sig[64], r, s, true) {
		return chain.Address{}, errors.New("invalid or malleable receipt signature")
	}
	pub, err := crypto.SigToPub(hash[:], sig)
	if err != nil {
		return chain.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// Verify is pure; the consumer supplies a trusted address (not from the receipt).
// Replay rejection is handled separately by verify.Verifier.
func Verify(r d.DecisionReceipt, req d.DecisionRequest, expected chain.Address, now uint64) error {
	if expected == (chain.Address{}) {
		return errors.New("trusted provider address required")
	}
	if err := req.Validate(now); err != nil {
		return err
	}
	id, err := req.CanonicalHash()
	if err != nil {
		return err
	}
	if r.Version != d.Version || r.RequestID != id || r.ChainID != req.ChainID || r.Nonce != req.Nonce || r.InputHash != req.InputHash || r.PolicyHash != req.PolicyHash {
		return errors.New("receipt does not bind originating request")
	}
	if r.ModelHash == (chain.Hash{}) || (req.ModelRequirements.ModelHash != (chain.Hash{}) && r.ModelHash != req.ModelRequirements.ModelHash) {
		return errors.New("receipt model mismatch")
	}
	if r.Expiry <= now || r.Expiry > req.Deadline || r.CompletedAt > now || r.StartedAt > r.CompletedAt || r.CompletedAt >= r.Expiry || r.LatencyMs != r.CompletedAt-r.StartedAt || r.LatencyMs > req.MaxLatencyMs {
		return errors.New("invalid or expired receipt timing")
	}
	if err := r.Result.Validate(); err != nil {
		return err
	}
	policyResult := r.Result.EnforcePolicy(req)
	if policyResult.NeedEscalation != r.Result.NeedEscalation || (req.SchemaID == "system1-v1" && len(r.Result.Answers) == 3 && r.Result.Answers[2].ValuePPM != policyResult.Answers[2].ValuePPM) {
		return errors.New("required escalation suppressed or inconsistent")
	}

	hash, err := r.CanonicalHash()
	if err != nil {
		return err
	}
	address, err := Recover(hash, r.ProviderSignature)
	if err != nil {
		return err
	}
	if address != expected || r.ProviderDID != "did:n42:"+strings.ToLower(address.Hex()) {
		return errors.New("receipt provider identity mismatch")
	}
	return nil
}
