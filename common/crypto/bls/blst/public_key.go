//go:build (android || (linux && amd64) || (linux && arm64) || (darwin && amd64) || (darwin && arm64) || (windows && amd64)) && !blst_disabled

package blst

import (
	"fmt"

	lru "github.com/hashicorp/golang-lru"
	"github.com/n42blockchain/N42/common/crypto/bls/common"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/pkg/errors"
)

var maxKeys = 1000000
var pubkeyCache *lru.Cache
var errUninitializedPublicKey = errors.New("uninitialized public key")

func init() {
	var err error
	pubkeyCache, err = lru.New(maxKeys)
	if err != nil {
		// This should never happen with a valid maxKeys value,
		// but if it does, we need to know about it immediately
		panic(fmt.Sprintf("failed to initialize pubkey cache: %v", err))
	}
}

const (
	BLSPubkeyLength = 48
)

// PublicKey used in the BLS signature scheme.
type PublicKey struct {
	p *blstPublicKey
}

func unwrapPublicKey(pubKey common.PublicKey) (*PublicKey, bool) {
	pk, ok := pubKey.(*PublicKey)
	if !ok || pk == nil || pk.p == nil {
		return nil, false
	}
	return pk, true
}

// PublicKeyFromBytes creates a BLS public key from a  BigEndian byte slice.
func PublicKeyFromBytes(pubKey []byte) (common.PublicKey, error) {
	if len(pubKey) != BLSPubkeyLength {
		return nil, fmt.Errorf("public key must be %d bytes", BLSPubkeyLength)
	}
	newKey := (*[BLSPubkeyLength]byte)(pubKey)
	if cv, ok := pubkeyCache.Get(*newKey); ok {
		return cv.(*PublicKey).Copy(), nil
	}
	// Subgroup check NOT done when decompressing pubkey.
	p := new(blstPublicKey).Uncompress(pubKey)
	if p == nil {
		return nil, errors.New("could not unmarshal bytes into public key")
	}
	// Subgroup and infinity check
	if !p.KeyValidate() {
		// NOTE: the error is not quite accurate since it includes group check
		return nil, common.ErrInfinitePubKey
	}
	pubKeyObj := &PublicKey{p: p}
	copiedKey := pubKeyObj.Copy()
	cacheKey := *newKey
	pubkeyCache.Add(cacheKey, copiedKey)
	return pubKeyObj, nil
}

// AggregatePublicKeys aggregates the provided raw public keys into a single key.
func AggregatePublicKeys(pubs [][]byte) (common.PublicKey, error) {
	if len(pubs) == 0 {
		return nil, errors.New("nil or empty public keys")
	}
	agg := new(blstAggregatePublicKey)
	mulP1 := make([]*blstPublicKey, 0, len(pubs))
	for _, pubkey := range pubs {
		pubKeyObj, err := PublicKeyFromBytes(pubkey)
		if err != nil {
			return nil, err
		}
		mulP1 = append(mulP1, pubKeyObj.(*PublicKey).p)
	}
	// No group check needed here since it is done in PublicKeyFromBytes
	// Note the checks could be moved from PublicKeyFromBytes into Aggregate
	// and take advantage of multi-threading.
	agg.Aggregate(mulP1, false)
	return &PublicKey{p: agg.ToAffine()}, nil
}

// Marshal a public key into a LittleEndian byte slice.
func (p *PublicKey) Marshal() []byte {
	if p == nil || p.p == nil {
		return nil
	}
	return p.p.Compress()
}

// Copy the public key to a new pointer reference.
func (p *PublicKey) Copy() common.PublicKey {
	if p == nil || p.p == nil {
		return &PublicKey{}
	}
	np := *p.p
	return &PublicKey{p: &np}
}

// IsInfinite checks if the public key is infinite.
func (p *PublicKey) IsInfinite() bool {
	if p == nil || p.p == nil {
		return false
	}
	zeroKey := new(blstPublicKey)
	return p.p.Equals(zeroKey)
}

// Equals checks if the provided public key is equal to
// the current one.
func (p *PublicKey) Equals(p2 common.PublicKey) bool {
	if p == nil || p.p == nil {
		return false
	}
	pk2, ok := unwrapPublicKey(p2)
	if !ok {
		return false
	}
	return p.p.Equals(pk2.p)
}

// Aggregate two public keys.
func (p *PublicKey) Aggregate(p2 common.PublicKey) common.PublicKey {
	if p == nil || p.p == nil {
		return nil
	}
	pk2, ok := unwrapPublicKey(p2)
	if !ok {
		return nil
	}

	agg := new(blstAggregatePublicKey)
	// No group check here since it is checked at decompression time
	agg.Add(p.p, false)
	agg.Add(pk2.p, false)
	p.p = agg.ToAffine()

	return p
}

func (p *PublicKey) UnmarshalJSON(input []byte) error {
	b := make([]byte, BLSPubkeyLength)
	err := hexutil.UnmarshalFixedText("PublicKey", input, b)
	if err != nil {
		return err
	}
	pubkey, err := PublicKeyFromBytes(b)
	if err != nil {
		return err
	}
	*p = *pubkey.(*PublicKey)
	return nil
}

func (p *PublicKey) UnmarshalText(input []byte) error {
	b := make([]byte, BLSPubkeyLength)
	err := hexutil.UnmarshalFixedText("PublicKey", input, b)
	if err != nil {
		return err
	}
	pubkey, err := PublicKeyFromBytes(b)
	if err != nil {
		return err
	}
	*p = *pubkey.(*PublicKey)
	return nil
}

func (p *PublicKey) MarshalText() ([]byte, error) {
	marshaled := p.Marshal()
	if marshaled == nil {
		return nil, errUninitializedPublicKey
	}
	return hexutil.Bytes(marshaled).MarshalText()
}

// AggregateMultiplePubkeys aggregates the provided decompressed keys into a single key.
// Returns nil if the input slice is empty.
func AggregateMultiplePubkeys(pubkeys []common.PublicKey) common.PublicKey {
	if len(pubkeys) == 0 {
		return nil
	}
	mulP1 := make([]*blstPublicKey, 0, len(pubkeys))
	for _, pubkey := range pubkeys {
		pk, ok := unwrapPublicKey(pubkey)
		if !ok {
			return nil
		}
		mulP1 = append(mulP1, pk.p)
	}
	agg := new(blstAggregatePublicKey)
	// No group check needed here since it is done in PublicKeyFromBytes
	// Note the checks could be moved from PublicKeyFromBytes into Aggregate
	// and take advantage of multi-threading.
	agg.Aggregate(mulP1, false)
	return &PublicKey{p: agg.ToAffine()}
}
