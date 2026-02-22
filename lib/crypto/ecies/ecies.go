// Copyright (c) 2013 Kyle Isom <kyle@tyrfingr.is>
// Copyright (c) 2012 The Go Authors. All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//    * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//    * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//    * Neither the name of Google Inc. nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package ecies

import (
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"math/big"

	libcommon "github.com/n42blockchain/N42/lib/common"
)

var (
	ErrImport                     = fmt.Errorf("ecies: failed to import key")
	ErrInvalidCurve               = fmt.Errorf("ecies: invalid elliptic curve")
	ErrInvalidPublicKey           = fmt.Errorf("ecies: invalid public key")
	ErrSharedKeyIsPointAtInfinity = fmt.Errorf("ecies: shared key is point at infinity")
	ErrSharedKeyTooBig            = fmt.Errorf("ecies: shared key params are too big")
	ErrSharedTooLong              = fmt.Errorf("ecies: shared secret is too long")
	ErrInvalidMessage             = fmt.Errorf("ecies: invalid message")
)

// PublicKey is a representation of an elliptic curve public key.
type PublicKey struct {
	X *big.Int
	Y *big.Int
	elliptic.Curve
	Params *ECIESParams
}

// ExportECDSA exports an ECIES public key as an ECDSA public key.
func (pub *PublicKey) ExportECDSA() *ecdsa.PublicKey {
	return &ecdsa.PublicKey{Curve: pub.Curve, X: pub.X, Y: pub.Y}
}

// ImportECDSAPublic imports an ECDSA public key as an ECIES public key.
func ImportECDSAPublic(pub *ecdsa.PublicKey) *PublicKey {
	return &PublicKey{
		X:      pub.X,
		Y:      pub.Y,
		Curve:  pub.Curve,
		Params: ParamsFromCurve(pub.Curve),
	}
}

// PrivateKey is a representation of an elliptic curve private key.
type PrivateKey struct {
	PublicKey
	D *big.Int
}

// ExportECDSA exports an ECIES private key as an ECDSA private key.
func (prv *PrivateKey) ExportECDSA() *ecdsa.PrivateKey {
	return &ecdsa.PrivateKey{
		PublicKey: *prv.PublicKey.ExportECDSA(),
		D:         prv.D,
	}
}

// ImportECDSA imports an ECDSA private key as an ECIES private key.
func ImportECDSA(prv *ecdsa.PrivateKey) *PrivateKey {
	pub := ImportECDSAPublic(&prv.PublicKey)
	return &PrivateKey{*pub, prv.D}
}

// GenerateKey generates an elliptic curve public / private keypair. If params
// is nil, the recommended default parameters for the key will be chosen.
func GenerateKey(rand io.Reader, curve elliptic.Curve, params *ECIESParams) (*PrivateKey, error) {
	pb, x, y, err := elliptic.GenerateKey(curve, rand)
	if err != nil {
		return nil, err
	}
	if params == nil {
		params = ParamsFromCurve(curve)
	}
	return &PrivateKey{
		PublicKey: PublicKey{X: x, Y: y, Curve: curve, Params: params},
		D:        new(big.Int).SetBytes(pb),
	}, nil
}

// MaxSharedKeyLength returns the maximum length of the shared key the
// public key can produce.
func MaxSharedKeyLength(pub *PublicKey) int {
	return libcommon.BitLenToByteLen(pub.Curve.Params().BitSize)
}

// GenerateShared performs ECDH key agreement to establish secret keys for encryption.
func (prv *PrivateKey) GenerateShared(pub *PublicKey, skLen, macLen int) ([]byte, error) {
	if prv.PublicKey.Curve != pub.Curve {
		return nil, ErrInvalidCurve
	}
	if skLen+macLen > MaxSharedKeyLength(pub) {
		return nil, ErrSharedKeyTooBig
	}

	x, _ := pub.Curve.ScalarMult(pub.X, pub.Y, prv.D.Bytes())
	if x == nil {
		return nil, ErrSharedKeyIsPointAtInfinity
	}

	sk := make([]byte, skLen+macLen)
	xBytes := x.Bytes()
	copy(sk[len(sk)-len(xBytes):], xBytes)
	return sk, nil
}

// NIST SP 800-56 Concatenation Key Derivation Function (see section 5.8.1).
func concatKDF(hash hash.Hash, z, s1 []byte, kdLen int) []byte {
	counterBytes := make([]byte, 4)
	k := make([]byte, 0, roundup(kdLen, hash.Size()))
	for counter := uint32(1); len(k) < kdLen; counter++ {
		binary.BigEndian.PutUint32(counterBytes, counter)
		hash.Reset()
		hash.Write(counterBytes)
		hash.Write(z)
		hash.Write(s1)
		k = hash.Sum(k)
	}
	return k[:kdLen]
}

// roundup rounds size up to the next multiple of blocksize.
func roundup(size, blocksize int) int {
	return size + blocksize - (size % blocksize)
}

// deriveKeys creates the encryption and MAC keys using concatKDF.
func deriveKeys(hash hash.Hash, z, s1 []byte, keyLen int) (Ke, Km []byte) {
	K := concatKDF(hash, z, s1, 2*keyLen)
	Ke = K[:keyLen]
	Km = K[keyLen:]
	hash.Reset()
	hash.Write(Km)
	Km = hash.Sum(Km[:0])
	return Ke, Km
}

// messageTag computes the MAC of a message (called the tag) as per
// SEC 1, 3.5.
func messageTag(hash func() hash.Hash, km, msg, shared []byte) []byte {
	mac := hmac.New(hash, km)
	mac.Write(msg)
	mac.Write(shared)
	return mac.Sum(nil)
}

// generateIV generates an initialisation vector for CTR mode.
func generateIV(params *ECIESParams, rand io.Reader) ([]byte, error) {
	iv := make([]byte, params.BlockSize)
	_, err := io.ReadFull(rand, iv)
	return iv, err
}

// symEncrypt carries out CTR encryption using the block cipher specified in the parameters.
func symEncrypt(rand io.Reader, params *ECIESParams, key, m []byte) ([]byte, error) {
	c, err := params.Cipher(key)
	if err != nil {
		return nil, err
	}
	iv, err := generateIV(params, rand)
	if err != nil {
		return nil, err
	}

	ct := make([]byte, len(m)+params.BlockSize)
	copy(ct, iv)
	cipher.NewCTR(c, iv).XORKeyStream(ct[params.BlockSize:], m)
	return ct, nil
}

// symDecrypt carries out CTR decryption using the block cipher specified in the parameters.
func symDecrypt(params *ECIESParams, key, ct []byte) ([]byte, error) {
	c, err := params.Cipher(key)
	if err != nil {
		return nil, err
	}

	m := make([]byte, len(ct)-params.BlockSize)
	cipher.NewCTR(c, ct[:params.BlockSize]).XORKeyStream(m, ct[params.BlockSize:])
	return m, nil
}

// Encrypt encrypts a message using ECIES as specified in SEC 1, 5.1.
//
// s1 and s2 contain shared information that is not part of the resulting
// ciphertext. s1 is fed into key derivation, s2 is fed into the MAC. If the
// shared information parameters aren't being used, they should be nil.
func Encrypt(rand io.Reader, pub *PublicKey, m, s1, s2 []byte) ([]byte, error) {
	params, err := pubkeyParams(pub)
	if err != nil {
		return nil, err
	}

	R, err := GenerateKey(rand, pub.Curve, params)
	if err != nil {
		return nil, err
	}

	z, err := R.GenerateShared(pub, params.KeyLen, params.KeyLen)
	if err != nil {
		return nil, err
	}

	Ke, Km := deriveKeys(params.Hash(), z, s1, params.KeyLen)

	em, err := symEncrypt(rand, params, Ke, m)
	if err != nil || len(em) <= params.BlockSize {
		return nil, err
	}

	d := messageTag(params.Hash, Km, em, s2)
	Rb := elliptic.Marshal(pub.Curve, R.PublicKey.X, R.PublicKey.Y)

	ct := make([]byte, len(Rb)+len(em)+len(d))
	copy(ct, Rb)
	copy(ct[len(Rb):], em)
	copy(ct[len(Rb)+len(em):], d)
	return ct, nil
}

// Decrypt decrypts an ECIES ciphertext.
func (prv *PrivateKey) Decrypt(c, s1, s2 []byte) ([]byte, error) {
	if len(c) == 0 {
		return nil, ErrInvalidMessage
	}
	params, err := pubkeyParams(&prv.PublicKey)
	if err != nil {
		return nil, err
	}

	hash := params.Hash()
	hLen := hash.Size()

	switch c[0] {
	case 2, 3, 4:
		// valid public key prefix
	default:
		return nil, ErrInvalidPublicKey
	}

	rLen := (prv.PublicKey.Curve.Params().BitSize + 7) / 4
	if len(c) < rLen+hLen+1 {
		return nil, ErrInvalidMessage
	}

	mStart := rLen
	mEnd := len(c) - hLen

	Rx, Ry := elliptic.Unmarshal(prv.PublicKey.Curve, c[:rLen])
	if Rx == nil {
		return nil, ErrInvalidPublicKey
	}
	R := &PublicKey{Curve: prv.PublicKey.Curve, X: Rx, Y: Ry}

	z, err := prv.GenerateShared(R, params.KeyLen, params.KeyLen)
	if err != nil {
		return nil, err
	}
	Ke, Km := deriveKeys(hash, z, s1, params.KeyLen)

	d := messageTag(params.Hash, Km, c[mStart:mEnd], s2)
	if subtle.ConstantTimeCompare(c[mEnd:], d) != 1 {
		return nil, ErrInvalidMessage
	}

	return symDecrypt(params, Ke, c[mStart:mEnd])
}
