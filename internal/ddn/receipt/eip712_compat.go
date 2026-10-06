package receipt

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"

	"github.com/n42blockchain/N42/accounts/abi"
	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// The exact struct field order/types come from n42-26's relay lib.rs.
type QuoteFields struct {
	ChainID                            uint64
	Hub, Requester, RefundTo, Consumer chain.Address
	TemplateID                         uint64
	InputHash                          chain.Hash
	Deadline, SignerVersion            uint64
	Fee                                *big.Int
	QuoteExpiry                        uint64
}
type ResultFields struct {
	ChainID                             uint64
	Hub                                 chain.Address
	RequestID                           *big.Int
	AnswerHash, EvidenceHash, ModelHash chain.Hash
	SignerVersion                       uint64
}

func word(n uint64) []byte               { b := make([]byte, 32); binary.BigEndian.PutUint64(b[24:], n); return b }
func addressWord(a chain.Address) []byte { b := make([]byte, 32); copy(b[12:], a[:]); return b }
func uintWord(n *big.Int) ([]byte, error) {
	if n == nil || n.Sign() < 0 || n.BitLen() > 256 {
		return nil, errors.New("EIP-712 integer must fit uint256")
	}
	return n.FillBytes(make([]byte, 32)), nil
}
func domain(chainID uint64, hub chain.Address) chain.Hash {
	return crypto.Keccak256Hash(crypto.Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")), crypto.Keccak256([]byte("N42Decision")), crypto.Keccak256([]byte("1")), word(chainID), addressWord(hub))
}
func typedDigest(chainID uint64, hub chain.Address, fields ...[]byte) chain.Hash {
	d := domain(chainID, hub)
	h := crypto.Keccak256Hash(fields...)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, d[:], h[:])
}
func QuoteDigest(q QuoteFields) (chain.Hash, error) {
	fee, err := uintWord(q.Fee)
	if err != nil {
		return chain.Hash{}, err
	}
	return typedDigest(q.ChainID, q.Hub, crypto.Keccak256([]byte("Quote(address requester,address refundTo,address consumer,uint64 templateId,bytes32 inputHash,uint64 deadline,uint64 signerVersion,uint256 fee,uint64 quoteExpiry)")), addressWord(q.Requester), addressWord(q.RefundTo), addressWord(q.Consumer), word(q.TemplateID), q.InputHash[:], word(q.Deadline), word(q.SignerVersion), fee, word(q.QuoteExpiry)), nil
}
func ResultDigest(r ResultFields) (chain.Hash, error) {
	id, err := uintWord(r.RequestID)
	if err != nil {
		return chain.Hash{}, err
	}
	return typedDigest(r.ChainID, r.Hub, crypto.Keccak256([]byte("ResultAttestation(uint256 requestId,bytes32 answerHash,bytes32 evidenceHash,bytes32 modelHash,uint64 signerVersion)")), id, r.AnswerHash[:], r.EvidenceHash[:], r.ModelHash[:], word(r.SignerVersion)), nil
}

// AnswerHash is Keccak256(abi.encode(Answer[])), including the dynamic head.
func AnswerHash(answers []d.QuantizedAnswer) (chain.Hash, error) {
	typ, err := abi.NewType("tuple[]", "", []abi.ArgumentMarshaling{{Name: "kind", Type: "uint8"}, {Name: "selected", Type: "uint8"}, {Name: "valuePpm", Type: "uint32"}, {Name: "confidencePpm", Type: "uint32"}, {Name: "probabilitiesPpm", Type: "uint32[]"}})
	if err != nil {
		return chain.Hash{}, err
	}
	type answer struct {
		Kind             uint8
		Selected         uint8
		ValuePpm         uint32
		ConfidencePpm    uint32
		ProbabilitiesPpm []uint32
	}
	out := make([]answer, len(answers))
	for i, a := range answers {
		out[i] = answer{a.Kind, a.Selected, a.ValuePPM, a.ConfidencePPM, a.ProbabilitiesPPM}
	}
	b, err := (abi.Arguments{{Type: typ}}).Pack(out)
	if err != nil {
		return chain.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}

// SignQuote/SignResult return contract-compatible signatures with v=27/28.
func (s *Signer) signTyped(hash chain.Hash) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == nil {
		return "", errors.New("DDN signer closed")
	}
	sig, err := crypto.Sign(hash[:], s.key)
	if err != nil {
		return "", err
	}
	sig[64] += 27
	return "0x" + hex.EncodeToString(sig), nil
}
func (s *Signer) SignQuote(q QuoteFields) (string, error) {
	h, err := QuoteDigest(q)
	if err != nil {
		return "", err
	}
	return s.signTyped(h)
}
func (s *Signer) SignResult(r ResultFields) (string, error) {
	h, err := ResultDigest(r)
	if err != nil {
		return "", err
	}
	return s.signTyped(h)
}
func RecoverEIP712(hash chain.Hash, signature string) (chain.Address, error) {
	if len(signature) != 132 || !strings.HasPrefix(signature, "0x") {
		return chain.Address{}, errors.New("invalid EIP-712 signature")
	}
	sig, err := hex.DecodeString(signature[2:])
	if err != nil {
		return chain.Address{}, err
	}
	if sig[64] == 27 || sig[64] == 28 {
		sig[64] -= 27
	}
	return Recover(hash, "0x"+hex.EncodeToString(sig))
}
