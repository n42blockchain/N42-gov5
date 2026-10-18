package sharedexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// BoundedResumePlan is a separate v2 original-source prefix policy. Its digest
// identifies offline content; only independent local source and current-state
// verification can arm an owner. Queue credit release never refunds its caps.
type BoundedResumePlan struct {
	Version               uint16
	Identity              Identity
	ManifestSHA256        types.Hash
	CorpusID              types.Hash
	GenesisFileSHA256     types.Hash
	SourceProofSHA256     types.Hash
	MappingSHA256         types.Hash
	RecoveryOuterSHA256   types.Hash
	RecoverySHA256        types.Hash
	AccountVectorSHA256   types.Hash
	OriginalRestoreSHA256 types.Hash
	Canonical             ResumeCanonicalAnchor
	CanonicalNonceSum     uint64
	Senders               []ResumeSender
	NonceEndExclusive     uint64
	MaxSourceFrames       uint64
	MaxSourceTransactions uint64
	MaxNewTransactions    uint64
	SourceEncodedBytes    uint64
	MaxSourceEncodedBytes uint64
	WhitelistSHA256       types.Hash
}

// BoundedResumeFrame describes one unchanged original GCF frame. FirstNonce,
// sender and fixed skip must agree with FrameRange and the arming snapshot.
type BoundedResumeFrame struct {
	ID               CorpusFrameID
	SenderIndex      uint32
	Address          types.Address
	FirstNonce       uint64
	Transactions     uint16
	PayloadHash      types.Hash
	PayloadBytes     uint32
	CanonicalSkipped uint16
	NewlyAdmitted    uint16
}

func boundedResumeAdd(a, b uint64) (uint64, error) {
	if a > ^uint64(0)-b {
		return 0, errors.New("bounded resume integer addition overflow")
	}
	return a + b, nil
}

func boundedResumeMul(a, b uint64) (uint64, error) {
	if a != 0 && b > ^uint64(0)/a {
		return 0, errors.New("bounded resume integer multiplication overflow")
	}
	return a * b, nil
}

func boundedResumeGeometry(d CorpusDescriptor, end uint64) (frames, transactions, encodedLimit uint64, err error) {
	if d.NonceStart != 0 || d.FrameTransactions != 500 || end == 0 || end%500 != 0 || end > 10000 || end > d.NoncesPerSender {
		return 0, 0, 0, errors.New("bounded resume requires an original prefix ending at a positive 500 multiple at most 10000")
	}
	transactions, err = boundedResumeMul(uint64(d.SenderCount), end)
	if err != nil {
		return 0, 0, 0, err
	}
	frames = transactions / 500
	// Each original legacy recipe transaction must have raw RLP <=102 bytes.
	// The limit includes its four-byte length and every original wire header.
	payload, err := boundedResumeMul(transactions, 106)
	if err != nil {
		return 0, 0, 0, err
	}
	headers, err := boundedResumeMul(frames, CorpusWireHeaderSize)
	if err != nil {
		return 0, 0, 0, err
	}
	encodedLimit, err = boundedResumeAdd(payload, headers)
	return frames, transactions, encodedLimit, err
}

func ValidateBoundedResumePlanStructure(p BoundedResumePlan, manifestRaw []byte, baseline []CanonicalAccount) error {
	m, err := DecodeCorpusManifest(manifestRaw)
	if err != nil {
		return err
	}
	d := m.Descriptor
	if p.Version != 2 || p.Identity != d.Identity || p.ManifestSHA256 != types.Hash(sha256.Sum256(manifestRaw)) || p.CorpusID != m.CorpusID || p.GenesisFileSHA256 != d.GenesisFileHash || p.Canonical.Number == 0 || p.Canonical.Number == ^uint64(0) || p.Canonical.Hash == (types.Hash{}) || p.Canonical.Root == (types.Hash{}) {
		return errors.New("bounded resume manifest/chain/genesis/retained anchor differs")
	}
	for _, h := range []types.Hash{p.ManifestSHA256, p.CorpusID, p.GenesisFileSHA256, p.SourceProofSHA256, p.MappingSHA256, p.RecoveryOuterSHA256, p.RecoverySHA256, p.AccountVectorSHA256, p.OriginalRestoreSHA256, p.WhitelistSHA256} {
		if h == (types.Hash{}) {
			return errors.New("bounded resume proof binding missing")
		}
	}
	frames, total, limit, err := boundedResumeGeometry(d, p.NonceEndExclusive)
	if err != nil {
		return err
	}
	minimum, err := boundedResumeMul(total, 5)
	if err != nil {
		return err
	}
	headers, err := boundedResumeMul(frames, CorpusWireHeaderSize)
	if err != nil {
		return err
	}
	minimum, err = boundedResumeAdd(minimum, headers)
	if err != nil || p.MaxSourceFrames != frames || p.MaxSourceTransactions != total || p.MaxSourceEncodedBytes != limit || p.SourceEncodedBytes < minimum || p.SourceEncodedBytes > limit {
		return errors.New("bounded resume exact source geometry or encoded caps differ")
	}
	if len(p.Senders) != int(d.SenderCount) || len(baseline) != int(d.SenderCount) {
		return errors.New("bounded resume requires the complete sender vector")
	}
	mapping, err := ResumeMappingID(p.Senders)
	if err != nil || mapping != p.MappingSHA256 {
		return errors.New("bounded resume mapping digest differs")
	}
	byAddress := make(map[types.Address]CanonicalAccount, len(baseline))
	var sum uint64
	for i, account := range baseline {
		if i > 0 && bytes.Compare(baseline[i-1].Address[:], account.Address[:]) >= 0 {
			return errors.New("bounded resume account vector must retain strict address order")
		}
		if !account.Exists || account.Address == (types.Address{}) || account.CodeHash != crypto.Keccak256Hash(nil) || account.Nonce > p.NonceEndExclusive || len(account.Balance) == 0 || len(account.Balance) > 78 {
			return errors.New("bounded resume requires existing prefix-bounded supply EOAs")
		}
		balance, ok := new(big.Int).SetString(account.Balance, 10)
		remaining := new(big.Int).Mul(new(big.Int).SetUint64(d.NoncesPerSender-account.Nonce), big.NewInt(21001))
		if !ok || balance.Sign() <= 0 || balance.BitLen() > 256 || balance.String() != account.Balance || balance.Cmp(remaining) < 0 {
			return errors.New("bounded resume balance invalid or cannot fund the original remainder")
		}
		sum, err = boundedResumeAdd(sum, account.Nonce)
		if err != nil {
			return err
		}
		byAddress[account.Address] = account
	}
	if sum != p.CanonicalNonceSum || sum > total || p.MaxNewTransactions != total-sum {
		return errors.New("bounded resume nonce sum or nonrefundable new-transaction cap differs")
	}
	for _, sender := range p.Senders {
		account, ok := byAddress[sender.Address]
		if !ok || account.Nonce != sender.Nonce {
			return errors.New("bounded resume logical mapping must join the vector by address")
		}
	}
	vector, err := json.Marshal(baseline)
	if err != nil || types.Hash(sha256.Sum256(vector)) != p.AccountVectorSHA256 {
		return errors.New("bounded resume canonical account vector digest differs")
	}
	return nil
}

func BoundedResumePlanID(p BoundedResumePlan, manifestRaw []byte, baseline []CanonicalAccount) (types.Hash, error) {
	if err := ValidateBoundedResumePlanStructure(p, manifestRaw, baseline); err != nil {
		return types.Hash{}, err
	}
	var out bytes.Buffer
	out.WriteString("gov5/bounded-resume-plan/v2\x00")
	put := func(v any) { _ = binary.Write(&out, binary.BigEndian, v) }
	text := func(v string) { put(uint16(len(v))); out.WriteString(v) }
	put(p.Version)
	put(p.Identity.Version)
	text(p.Identity.Namespace)
	text(p.Identity.ChainID)
	out.Write(p.Identity.GenesisHash[:])
	for _, h := range []types.Hash{p.ManifestSHA256, p.CorpusID, p.GenesisFileSHA256, p.SourceProofSHA256, p.MappingSHA256, p.RecoveryOuterSHA256, p.RecoverySHA256, p.AccountVectorSHA256, p.OriginalRestoreSHA256} {
		out.Write(h[:])
	}
	put(p.Canonical.Number)
	out.Write(p.Canonical.Hash[:])
	out.Write(p.Canonical.Root[:])
	put(p.CanonicalNonceSum)
	put(uint32(len(p.Senders)))
	for _, sender := range p.Senders {
		put(sender.Index)
		out.Write(sender.Address[:])
		put(sender.Nonce)
	}
	for _, n := range []uint64{p.NonceEndExclusive, p.MaxSourceFrames, p.MaxSourceTransactions, p.MaxNewTransactions, p.SourceEncodedBytes, p.MaxSourceEncodedBytes} {
		put(n)
	}
	out.Write(p.WhitelistSHA256[:])
	return types.Hash(sha256.Sum256(out.Bytes())), nil
}
