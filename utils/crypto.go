package utils

import (
	"crypto/ecdsa"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/pkg/errors"

	n42_crypto "github.com/n42blockchain/N42/common/crypto"
)

func ConvertFromInterfacePrivKey(privkey crypto.PrivKey) (*ecdsa.PrivateKey, error) {
	secpKey, ok := privkey.(*crypto.Secp256k1PrivateKey)
	if !ok {
		return nil, errors.New("could not cast to Secp256k1PrivateKey")
	}
	rawKey, err := secpKey.Raw()
	if err != nil {
		return nil, err
	}
	privKey := new(ecdsa.PrivateKey)
	privKey.D = new(big.Int).SetBytes(rawKey)
	privKey.Curve = n42_crypto.S256()
	privKey.X, privKey.Y = privKey.Curve.ScalarBaseMult(rawKey)
	return privKey, nil
}

func ConvertToInterfacePrivkey(privkey *ecdsa.PrivateKey) (crypto.PrivKey, error) {
	privBytes := privkey.D.Bytes()
	// Left-pad to 32 bytes when big.Int encodes fewer bytes.
	if len(privBytes) < 32 {
		privBytes = append(make([]byte, 32-len(privBytes)), privBytes...)
	}
	return crypto.UnmarshalSecp256k1PrivateKey(privBytes)
}

func ConvertToInterfacePubkey(pubkey *ecdsa.PublicKey) (crypto.PubKey, error) {
	xVal, yVal := new(btcec.FieldVal), new(btcec.FieldVal)
	if xVal.SetByteSlice(pubkey.X.Bytes()) {
		return nil, errors.Errorf("X value overflows")
	}
	if yVal.SetByteSlice(pubkey.Y.Bytes()) {
		return nil, errors.Errorf("Y value overflows")
	}
	newKey := crypto.PubKey((*crypto.Secp256k1PublicKey)(btcec.NewPublicKey(xVal, yVal)))
	xVal.Zero()
	yVal.Zero()
	return newKey, nil
}
