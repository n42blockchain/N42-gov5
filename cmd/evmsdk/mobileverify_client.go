// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// MobileVerifyClient is the transport half of phase 4 of
// docs/mobile-attestation-design.md: the three HTTP primitives a mobile
// verifier needs against an IDC node's /mobileverify surface —
// register (once, with proof of possession), fetch packet, submit
// receipt. Everything else already exists in this SDK and is reused
// unchanged: ExecuteAndVerifyV2 re-executes the packet, and
// VerificationReceipt carries the BLS attestation. Both the embedded
// SDK (com.mobileSdk.Api facade) and the standalone app converge on
// this client — one data plane, two packagings.

package evmsdk

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/params"
)

// mobileVerifyPoPTag mirrors internal/mobileverify's PoP domain tag.
// Restated here because importing that package would cycle (it imports
// this one for the wire codec); the mobileverify package pins the two
// byte-exactly with a cross-package test, same discipline as the
// receipt signing message.
var mobileVerifyPoPTag = []byte("n42/mobileverify/pop/v1\x00")

// PoPSigningMessage is the payload a registrant signs to prove
// possession of the secret key behind pubkey.
func PoPSigningMessage(pubkey [48]byte) []byte {
	out := make([]byte, 0, len(mobileVerifyPoPTag)+48)
	out = append(out, mobileVerifyPoPTag...)
	out = append(out, pubkey[:]...)
	return out
}

// MobileVerifyClient talks to one IDC node's /mobileverify HTTP surface.
type MobileVerifyClient struct {
	baseURL string
	http    *http.Client
	sk      common.SecretKey
	pubkey  [48]byte
}

// NewMobileVerifyClient creates a client bound to a BLS identity.
// baseURL is the IDC node's mobile endpoint root, e.g.
// "http://idc.example.com:8555"; privKeyHex is the device's BLS secret
// key (the same hex format the facade's verifier config uses).
func NewMobileVerifyClient(baseURL, privKeyHex string) (*MobileVerifyClient, error) {
	sk, err := decodeSecretKey(privKeyHex)
	if err != nil {
		return nil, fmt.Errorf("mobileverify client: %w", err)
	}
	c := &MobileVerifyClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 20 * time.Second},
		sk:      sk,
	}
	copy(c.pubkey[:], sk.PublicKey().Marshal())
	return c, nil
}

// Pubkey returns the device's BLS public key.
func (c *MobileVerifyClient) Pubkey() [48]byte { return c.pubkey }

// mobileVerifyPoWTag mirrors internal/mobileverify's registration
// proof-of-work domain tag (same restatement discipline as the PoP tag;
// TestPoWMessageMatchesSDK pins the bytes).
var mobileVerifyPoWTag = []byte("n42/mobileverify/pow/v1")

// PoWMessage is the byte string hashed for registration proof-of-work.
func PoWMessage(pubkey [48]byte, nonce uint64) []byte {
	msg := make([]byte, 0, len(mobileVerifyPoWTag)+48+8)
	msg = append(msg, mobileVerifyPoWTag...)
	msg = append(msg, pubkey[:]...)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], nonce)
	return append(msg, nb[:]...)
}

// SolveRegistrationPoW grinds a nonce whose Keccak256(PoWMessage) digest has
// at least `bits` leading zero bits — the server's Sybil gate. Bounded; a
// modern phone solves 20 bits well under a second.
func SolveRegistrationPoW(pubkey [48]byte, bits int) (uint64, error) {
	if bits <= 0 {
		return 0, nil
	}
	if bits > 32 {
		return 0, fmt.Errorf("mobileverify: pow difficulty %d too high", bits)
	}
	budget := uint64(1) << uint(bits+4)
	for nonce := uint64(0); nonce < budget; nonce++ {
		h := sha3.NewLegacyKeccak256()
		h.Write(PoWMessage(pubkey, nonce))
		if leadingZeroBits(h.Sum(nil)) >= bits {
			return nonce, nil
		}
	}
	return 0, errors.New("mobileverify: pow budget exhausted")
}

func leadingZeroBits(b []byte) int {
	n := 0
	for _, c := range b {
		if c == 0 {
			n += 8
			continue
		}
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			if c&mask != 0 {
				return n
			}
			n++
		}
	}
	return n
}

// Register submits the device's pubkey with a fresh proof of
// possession and returns its stable MobileIndex. Idempotent: the server
// returns the same index for a known key, so calling on every app
// launch is safe.
func (c *MobileVerifyClient) Register(ctx context.Context) (uint32, error) {
	pop := c.sk.Sign(PoPSigningMessage(c.pubkey))
	fields := map[string]string{
		"pubkey": hex.EncodeToString(c.pubkey[:]),
		"pop":    hex.EncodeToString(pop.Marshal()),
	}
	var out struct {
		Index uint32 `json:"index"`
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return 0, err
	}
	powBits, err := c.postRegister(ctx, body, &out)
	if err == nil {
		return out.Index, nil
	}
	if powBits <= 0 {
		return 0, err
	}
	// Server demands a registration proof-of-work (HTTP 428 + pow_bits):
	// solve locally and retry once with the nonce attached.
	nonce, serr := SolveRegistrationPoW(c.pubkey, powBits)
	if serr != nil {
		return 0, serr
	}
	fields["pow_nonce"] = strconv.FormatUint(nonce, 10)
	if body, err = json.Marshal(fields); err != nil {
		return 0, err
	}
	if _, err = c.postRegister(ctx, body, &out); err != nil {
		return 0, err
	}
	return out.Index, nil
}

// postRegister posts a registration and, on HTTP 428, surfaces the demanded
// proof-of-work difficulty so Register can solve and retry.
func (c *MobileVerifyClient) postRegister(ctx context.Context, body []byte, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/mobileverify/register", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionRequired {
		var pre struct {
			Error   string `json:"error"`
			PowBits int    `json:"pow_bits"`
		}
		if derr := json.NewDecoder(resp.Body).Decode(&pre); derr == nil && pre.PowBits > 0 {
			return pre.PowBits, errors.New(pre.Error)
		}
		return 0, errors.New("registration precondition failed")
	}
	if resp.StatusCode != http.StatusOK {
		return 0, httpErrorFrom(resp)
	}
	return 0, json.NewDecoder(resp.Body).Decode(out)
}

// FetchPacket downloads and decodes a block's StreamPacket.
func (c *MobileVerifyClient) FetchPacket(ctx context.Context, blockHashHex string) (*StreamPacket, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/mobileverify/packet/"+blockHashHex, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpErrorFrom(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	return DecodeStreamPacket(data)
}

// FetchMagnet returns the swarm (BitTorrent) magnet URI for a block's
// packet, when the IDC node is seeding it (design §5b target form). A
// client that prefers the torrent transport resolves the magnet here,
// then joins the swarm out of band; the decoded packet is verified the
// same way regardless of transport. Returns an error (404-wrapped) when
// the node is not seeding.
func (c *MobileVerifyClient) FetchMagnet(ctx context.Context, blockHashHex string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/mobileverify/magnet/"+blockHashHex, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", httpErrorFrom(resp)
	}
	var out struct {
		Magnet string `json:"magnet"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Magnet, nil
}

// SubmitReceipt posts a signed VerificationReceipt to the collection
// endpoint and returns the device's index as acknowledged by the server.
func (c *MobileVerifyClient) SubmitReceipt(ctx context.Context, r *VerificationReceipt) (uint32, error) {
	if r == nil {
		return 0, errors.New("mobileverify client: nil receipt")
	}
	body, err := json.Marshal(map[string]any{
		"blockHash":    hex.EncodeToString(r.BlockHash[:]),
		"blockNumber":  r.BlockNumber,
		"receiptsRoot": hex.EncodeToString(r.ComputedReceiptsRoot[:]),
		"pubkey":       hex.EncodeToString(r.VerifierPubkey[:]),
		"signature":    hex.EncodeToString(r.Signature[:]),
		"timestampMs":  r.TimestampMs,
	})
	if err != nil {
		return 0, err
	}
	var out struct {
		Index uint32 `json:"index"`
	}
	if err := c.postJSON(ctx, "/mobileverify/receipt", body, &out); err != nil {
		return 0, err
	}
	return out.Index, nil
}

// SignReceipt builds and signs a VerificationReceipt for a verified
// re-execution — exactly the tuple ExecuteAndVerifyV2 returned.
func (c *MobileVerifyClient) SignReceipt(res *V2VerifyResult) *VerificationReceipt {
	r := &VerificationReceipt{
		BlockHash:            res.BlockHash,
		BlockNumber:          res.BlockNumber,
		ComputedReceiptsRoot: res.ComputedReceiptsRoot,
		VerifierPubkey:       c.pubkey,
		TimestampMs:          uint64(time.Now().UnixMilli()),
	}
	copy(r.Signature[:], c.sk.Sign(r.SigningMessage()).Marshal())
	return r
}

// VerifyBlock is the one-call loop: fetch the packet, re-execute it
// (ExecuteAndVerifyV2 — the same path the facade runs), sign the
// attestation and submit it. codes should be a long-lived CodeCache so
// bytecode dedup accumulates across blocks; chainCfg nil selects the
// SDK default, matching the facade.
func (c *MobileVerifyClient) VerifyBlock(ctx context.Context, blockHashHex string, codes *CodeCache, chainCfg *params.ChainConfig) (*V2VerifyResult, error) {
	pkt, err := c.FetchPacket(ctx, blockHashHex)
	if err != nil {
		return nil, err
	}
	res, err := ExecuteAndVerifyV2(pkt, codes, chainCfg)
	if err != nil {
		return nil, err
	}
	if _, err := c.SubmitReceipt(ctx, c.SignReceipt(res)); err != nil {
		return res, fmt.Errorf("mobileverify client: verified but submit failed: %w", err)
	}
	return res, nil
}

func (c *MobileVerifyClient) postJSON(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpErrorFrom(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func httpErrorFrom(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
	if e.Error != "" {
		return fmt.Errorf("mobileverify client: server %d: %s", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("mobileverify client: server status %d", resp.StatusCode)
}
