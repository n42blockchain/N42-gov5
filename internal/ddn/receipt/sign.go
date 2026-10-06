package receipt

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/n42blockchain/N42/accounts/keystore"
	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// Signer owns a separate DDN key. Never pass a wallet or consensus key here.
type Signer struct {
	mu      sync.Mutex
	key     *ecdsa.PrivateKey
	address chain.Address
}

func NewSigner(key *ecdsa.PrivateKey) (*Signer, error) {
	if key == nil {
		return nil, errors.New("DDN signing key required")
	}
	copy, err := crypto.ToECDSA(crypto.FromECDSA(key))
	if err != nil {
		return nil, err
	}
	return &Signer{key: copy, address: crypto.PubkeyToAddress(copy.PublicKey)}, nil
}
func (s *Signer) Address() chain.Address { return s.address }
func (s *Signer) DID() string            { return "did:n42:" + strings.ToLower(s.address.Hex()) }

// LoadSigner reads an encrypted Web3 keystore exclusively from a dedicated
// ddn-keystore directory. No key is generated or wallet unlocked implicitly.
func LoadSigner(path, password string) (*Signer, error) {
	if filepath.Base(filepath.Dir(filepath.Clean(path))) != "ddn-keystore" {
		return nil, errors.New("DDN key must be in a dedicated ddn-keystore directory")
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 1<<20 {
		return nil, errors.New("DDN keystore must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("DDN keystore read failed")
	}
	key, err := keystore.DecryptKey(data, password)
	if err != nil {
		return nil, errors.New("DDN keystore decryption failed")
	}
	defer key.PrivateKey.D.SetInt64(0)
	return NewSigner(key.PrivateKey)
}
func (s *Signer) Sign(r d.DecisionReceipt) (d.DecisionReceipt, error) {
	if r.ProviderDID != s.DID() {
		return d.DecisionReceipt{}, errors.New("signer must match the claimed provider DID")
	}
	if err := r.Result.Validate(); err != nil {
		return d.DecisionReceipt{}, err
	}
	hash, err := r.CanonicalHash()
	if err != nil {
		return d.DecisionReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == nil {
		return d.DecisionReceipt{}, errors.New("DDN signer closed")
	}
	sig, err := crypto.Sign(hash[:], s.key)
	if err != nil {
		return d.DecisionReceipt{}, err
	}
	r.ProviderSignature = "0x" + hex.EncodeToString(sig)
	return r, nil
}
func (s *Signer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		s.key.D.SetInt64(0)
		s.key = nil
	}
}
