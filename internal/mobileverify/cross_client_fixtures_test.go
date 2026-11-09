// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cross-client fixtures for the /mobileverify surface. Every byte contract a
// second implementation of this surface (the Rust node, n42-rs
// crates/n42/mobileverify) has to match is written here from the code that
// serves it, with fixed inputs, so that implementation can pin itself to this
// one byte for byte (compared by SHA-256 of the raw files, see SHA256SUMS).
//
//	N42_WRITE_FIXTURES=1 go test ./internal/mobileverify/ -run TestWriteCrossClientFixtures -count=1
//
// rewrites testdata/cross_client_v1; without the variable the test rebuilds
// every fixture in memory and fails if any file differs from what the code
// produces now, so a change to one of these contracts is a test failure here
// before it is a field failure on a phone.

package mobileverify

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"lukechampine.com/blake3"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/streamverify"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/bmt"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/proto/types_pb"
)

const (
	fxDir        = "testdata/cross_client_v1"
	fxChainName  = "mainnet_qmdb_staggered"
	fxBLSDST     = "BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_"
	fxIKMPrefix  = "n42/mobileverify/cross-client-fixture/device/"
	fxCommitMs0  = uint64(1_784_372_000_000) // lastSeenMs written for the first commit
	fxCommitStep = uint64(45_000)            // + this per later commit (one epoch)
	fxClosedAtMs = uint64(1_784_372_090_000) // windowClosedAt of every fixture cert
	fxReceiptTs  = uint64(1_784_372_012_345) // timestampMs of the fixture receipt body
)

// --- ordered JSON -----------------------------------------------------------

type fxKV struct {
	k string
	v any
}

// fxObj is a JSON object that keeps its insertion order, so each file reads
// top-down ("_doc" first) and its bytes are stable.
type fxObj []fxKV

func (o fxObj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := fxMarshal(e.k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := fxMarshal(e.v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.k, err)
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func fxMarshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func fxFile(v any) ([]byte, error) {
	raw, err := fxMarshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func hx(b []byte) string { return hex.EncodeToString(b) }

func fxMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// --- devices ----------------------------------------------------------------

type fxDevice struct {
	ikm    [32]byte
	sk     blscommon.SecretKey
	pubkey [48]byte
}

func (d *fxDevice) pop() [96]byte {
	var out [96]byte
	copy(out[:], d.sk.Sign(PoPMessage(d.pubkey)).Marshal())
	return out
}

func (d *fxDevice) sign(msg []byte) [96]byte {
	var out [96]byte
	copy(out[:], d.sk.Sign(msg).Marshal())
	return out
}

func (d *fxDevice) receipt(blockHash types.Hash, number uint64, root types.Hash) *Receipt {
	return &Receipt{
		BlockHash: blockHash, BlockNumber: number, ComputedReceiptsRoot: root,
		VerifierPubkey: d.pubkey, Signature: d.sign(SigningMessage(blockHash, number, root)),
		TimestampMs: fxReceiptTs,
	}
}

// fxDevices derives four device identities from fixed IKMs exactly the way
// the SDK loads a persisted secret (decodeSecretKey -> blst KeyGen(ikm)).
func fxDevices(t *testing.T) []*fxDevice {
	t.Helper()
	out := make([]*fxDevice, 4)
	for i := range out {
		d := &fxDevice{ikm: sha256.Sum256([]byte(fmt.Sprintf("%s%d", fxIKMPrefix, i)))}
		sk, err := bls.SecretKeyFromRandom32Byte(d.ikm)
		fxMust(t, err)
		d.sk = sk
		copy(d.pubkey[:], sk.PublicKey().Marshal())
		out[i] = d
	}
	return out
}

// fxRegistry registers and commits devices in the fixture order: device 0,
// then devices 1 and 2 together, then device 3 (fewer when upTo < 3). Every
// commit's wall-clock lastSeenMs is overwritten with a fixed value.
func fxRegistry(t *testing.T, devs []*fxDevice, upTo int) *Registry {
	t.Helper()
	reg := NewRegistry()
	batches := [][]int{{0}, {1, 2}, {3}}
	for step, batch := range batches {
		added := false
		for _, i := range batch {
			if i > upTo {
				continue
			}
			_, _, err := reg.Register(devs[i].pubkey, devs[i].pop())
			fxMust(t, err)
			added = true
		}
		if !added {
			break
		}
		fxCommit(reg, step)
	}
	return reg
}

// fxCommit runs CommitEpoch and replaces the commit-time wall clock it
// stamps on the new entries (the only non-deterministic snapshot bytes).
func fxCommit(reg *Registry, step int) (types.Hash, int) {
	before := reg.IndexBound()
	root, n := reg.CommitEpoch()
	reg.mu.Lock()
	for i := before; i < len(reg.committed); i++ {
		reg.committed[i].lastSeenMs = fxCommitMs0 + uint64(step)*fxCommitStep
	}
	reg.mu.Unlock()
	return root, n
}

// --- HTTP helpers -----------------------------------------------------------

type fxHTTP struct {
	h    http.Handler
	next int
}

type fxResp struct {
	status int
	body   string
}

func (r fxResp) obj() fxObj {
	return fxObj{{"status", r.status}, {"body", r.body}}
}

// do drives the real handler in process. Each call comes from its own
// client address so the per-IP registration limit never interferes.
func (s *fxHTTP) do(method, path, body string) fxResp {
	s.next++
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = fmt.Sprintf("192.0.2.%d:40000", s.next%250+1)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return fxResp{status: rec.Code, body: rec.Body.String()}
}

func fxRegisterBody(pubkey []byte, pop []byte) string {
	b, _ := json.Marshal(map[string]string{"pubkey": hx(pubkey), "pop": hx(pop)})
	return string(b)
}

// fxCapture wraps a handler and records every request body it serves.
type fxCapture struct {
	inner  http.Handler
	bodies []string
	paths  []string
}

func (c *fxCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.bodies = append(c.bodies, string(b))
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)
	r.Body = io.NopCloser(bytes.NewReader(b))
	c.inner.ServeHTTP(w, r)
}

// --- the test ---------------------------------------------------------------

func TestWriteCrossClientFixtures(t *testing.T) {
	files := map[string]any{}
	devs := fxDevices(t)
	hdrFull := fxFullHeader(t, devs)

	files["keys.json"] = fxKeys(t, devs)
	files["pop.json"] = fxPoP(t, devs)
	files["pow.json"] = fxPoW(t, devs)
	files["registry.json"] = fxRegistryFixture(t, devs)
	files["mask.json"] = fxMasks(t)
	files["receipt.json"] = fxReceipts(t, devs, hdrFull)
	files["cert.json"] = fxCert(t, devs, hdrFull)
	files["receipts_root.json"] = fxReceiptsRoot(t)
	files["header.json"] = fxHeaders(t, hdrFull)
	files["transactions.json"] = fxTransactions(t)
	files["read_log.json"] = fxReadLog(t)
	files["stream_packet.json"] = fxStreamPackets(t)
	files["topics.json"] = fxTopics(t, devs)

	names := make([]string, 0, len(files))
	encoded := map[string][]byte{}
	for name, v := range files {
		b, err := fxFile(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		names = append(names, name)
		encoded[name] = b
	}
	sort.Strings(names)
	var sums bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(encoded[name]), name)
	}
	encoded["SHA256SUMS"] = sums.Bytes()
	names = append(names, "SHA256SUMS")

	if os.Getenv("N42_WRITE_FIXTURES") == "1" {
		fxMust(t, os.MkdirAll(fxDir, 0o755))
		for _, name := range names {
			fxMust(t, os.WriteFile(filepath.Join(fxDir, name), encoded[name], 0o644))
		}
		t.Logf("wrote %d files to %s", len(names), fxDir)
		return
	}
	for _, name := range names {
		got, err := os.ReadFile(filepath.Join(fxDir, name))
		if err != nil {
			t.Fatalf("%s: %v (regenerate with N42_WRITE_FIXTURES=1)", name, err)
		}
		if !bytes.Equal(got, encoded[name]) {
			t.Errorf("%s: the code no longer produces the committed fixture (a cross-client byte contract changed; regenerate with N42_WRITE_FIXTURES=1 only if that is intended)", name)
		}
	}
}

// 1. keys -----------------------------------------------------------------

func fxKeys(t *testing.T, devs []*fxDevice) fxObj {
	list := make([]fxObj, 0, len(devs))
	for i, d := range devs {
		ikmHex := hx(d.ikm[:])
		pubHex, err := evmsdk.MobileBLSPublicKey(ikmHex)
		fxMust(t, err)
		addrHex, err := evmsdk.MobileBLSAddress(ikmHex)
		fxMust(t, err)
		if pubHex != hx(d.pubkey[:]) {
			t.Fatalf("device %d: SDK pubkey %s != %x", i, pubHex, d.pubkey)
		}
		list = append(list, fxObj{
			{"device", i},
			{"ikm_seed", fmt.Sprintf("%s%d", fxIKMPrefix, i)},
			{"ikm", ikmHex},
			{"secret_scalar", hx(d.sk.Marshal())},
			{"pubkey", pubHex},
			{"addr", addrHex},
		})
	}
	return fxObj{
		{"_doc", "Device BLS identities (blst min-pk). ikm = sha256(ikm_seed) is the 32-byte secret the SDK persists (PrivHex); " +
			"the key is blst KeyGen(ikm, info=empty) (evmsdk decodeSecretKey -> bls.SecretKeyFromRandom32Byte), " +
			"NOT the ikm read as a scalar. secret_scalar = the resulting 32-byte big-endian scalar (sk.Marshal). " +
			"pubkey = 48-byte compressed G1 (evmsdk.MobileBLSPublicKey). addr = AddrHex = first 20 bytes of pubkey (evmsdk.MobileBLSAddress)."},
		{"devices", list},
	}
}

// 2. proof of possession ------------------------------------------------------

func fxPoP(t *testing.T, devs []*fxDevice) fxObj {
	list := make([]fxObj, 0, len(devs))
	for i, d := range devs {
		pop := d.pop()
		sdkPoP, err := evmsdk.MobileBLSProofOfPossession(hx(d.ikm[:]))
		fxMust(t, err)
		if sdkPoP != hx(pop[:]) {
			t.Fatalf("device %d: SDK PoP differs", i)
		}
		if !bytes.Equal(PoPMessage(d.pubkey), evmsdk.PoPSigningMessage(d.pubkey)) {
			t.Fatal("PoP message differs from the SDK")
		}
		list = append(list, fxObj{
			{"device", i},
			{"pubkey", hx(d.pubkey[:])},
			{"pop_message", hx(PoPMessage(d.pubkey))},
			{"pop", hx(pop[:])},
		})
	}

	// The SDK's register request body, captured off the wire.
	reg := NewRegistry()
	srv := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(NewPacketCache(8), nil, "/test"), nil, NewCertStore(8))
	capture := &fxCapture{inner: srv.srv.Handler}
	ts := httptest.NewServer(capture)
	defer ts.Close()
	client, err := evmsdk.NewMobileVerifyClient(ts.URL, hx(devs[0].ikm[:]))
	fxMust(t, err)
	_, _, err = client.RegisterStatus(t.Context())
	fxMust(t, err)
	if len(capture.bodies) != 1 {
		t.Fatalf("captured %d register bodies", len(capture.bodies))
	}

	// Rejections, through the real handler.
	h := &fxHTTP{h: srv.srv.Handler}
	p0, p1 := devs[0].pop(), devs[1].pop()
	var zeroPop [96]byte
	var zeroPub [48]byte
	cases := []fxObj{
		append(fxObj{{"case", "pop of device 0 presented with device 1's pubkey (valid signature, wrong key)"},
			{"pubkey", hx(devs[1].pubkey[:])}, {"pop", hx(p0[:])}},
			h.do("POST", "/mobileverify/register", fxRegisterBody(devs[1].pubkey[:], p0[:])).obj()...),
		append(fxObj{{"case", "pop of 96 zero bytes (not a compressed G2 point)"},
			{"pubkey", hx(devs[1].pubkey[:])}, {"pop", hx(zeroPop[:])}},
			h.do("POST", "/mobileverify/register", fxRegisterBody(devs[1].pubkey[:], zeroPop[:])).obj()...),
		append(fxObj{{"case", "pubkey of 48 zero bytes (not a compressed G1 point)"},
			{"pubkey", hx(zeroPub[:])}, {"pop", hx(p1[:])}},
			h.do("POST", "/mobileverify/register", fxRegisterBody(zeroPub[:], p1[:])).obj()...),
		append(fxObj{{"case", "pop one byte short"}, {"pubkey", hx(devs[1].pubkey[:])}, {"pop", hx(p1[:95])}},
			h.do("POST", "/mobileverify/register", fxRegisterBody(devs[1].pubkey[:], p1[:95])).obj()...),
		append(fxObj{{"case", "body is not JSON"}, {"request_body", "pubkey="}},
			h.do("POST", "/mobileverify/register", "pubkey=").obj()...),
		append(fxObj{{"case", "GET instead of POST"}},
			h.do("GET", "/mobileverify/register", "").obj()...),
	}
	if !strings.Contains(cases[0][len(cases[0])-1].v.(string), ErrBadPoP.Error()) {
		t.Fatalf("wrong PoP not rejected as bad PoP: %v", cases[0])
	}
	return fxObj{
		{"_doc", "Proof of possession. pop_message = \"n42/mobileverify/pop/v1\\x00\" (24 bytes) || pubkey(48) = 72 bytes; " +
			"pop = 96-byte compressed G2 signature: blst Sign(sk, pop_message, dst) with dst = " + fxBLSDST + " (no augmentation). " +
			"sdk_register_request_body = the exact bytes evmsdk MobileVerifyClient.RegisterStatus POSTs to /mobileverify/register " +
			"(json.Marshal of a map: keys sorted, hex without 0x, no pow_nonce when the server asks none). " +
			"rejections = status and exact body the gov5 handler answers (bodies end in \\n: json.Encoder)."},
		{"dst", fxBLSDST},
		{"devices", list},
		{"sdk_register_request_body", capture.bodies[0]},
		{"rejections", cases},
	}
}

// 3. proof of work --------------------------------------------------------------

func fxPoW(t *testing.T, devs []*fxDevice) fxObj {
	pub := devs[0].pubkey
	vectors := []fxObj{}
	for _, bits := range []int{0, 4, 8, 12} {
		msg0 := PoWMessage(pub, 0)
		if !bytes.Equal(msg0, evmsdk.PoWMessage(pub, 0)) {
			t.Fatal("PoW message differs from the SDK")
		}
		nonce, ok := SolvePoW(pub, bits, 0)
		if !ok {
			t.Fatalf("bits %d unsolved", bits)
		}
		sdkNonce, err := evmsdk.SolveRegistrationPoW(pub, bits)
		fxMust(t, err)
		if sdkNonce != nonce {
			t.Fatalf("bits %d: SDK nonce %d, server %d", bits, sdkNonce, nonce)
		}
		d0 := crypto.Keccak256(msg0)
		dn := crypto.Keccak256(PoWMessage(pub, nonce))
		vectors = append(vectors, fxObj{
			{"bits", bits},
			{"message_nonce0", hx(msg0)},
			{"digest_nonce0", hx(d0)},
			{"leading_zero_bits_nonce0", leadingZeroBits(d0)},
			{"solved_nonce", nonce},
			{"solved_message", hx(PoWMessage(pub, nonce))},
			{"solved_digest", hx(dn)},
			{"leading_zero_bits_solved", leadingZeroBits(dn)},
		})
	}

	// The 428 round trip with the real client and a server demanding 8 bits.
	reg := NewRegistry()
	srv := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(NewPacketCache(8), nil, "/test"), nil, NewCertStore(8))
	srv.SetRegisterPoWBits(8)
	h := &fxHTTP{h: srv.srv.Handler}
	p0 := devs[0].pop()
	missing := h.do("POST", "/mobileverify/register", fxRegisterBody(pub[:], p0[:]))
	badNonce, _ := json.Marshal(map[string]string{"pubkey": hx(pub[:]), "pop": hx(p0[:]), "pow_nonce": "0x01"})
	bad := h.do("POST", "/mobileverify/register", string(badNonce))

	capture := &fxCapture{inner: srv.srv.Handler}
	ts := httptest.NewServer(capture)
	defer ts.Close()
	client, err := evmsdk.NewMobileVerifyClient(ts.URL, hx(devs[0].ikm[:]))
	fxMust(t, err)
	_, _, err = client.RegisterStatus(t.Context())
	fxMust(t, err)
	if len(capture.bodies) != 2 {
		t.Fatalf("pow round trip: %d requests", len(capture.bodies))
	}
	return fxObj{
		{"_doc", "Registration proof of work for device 0's pubkey (keys.json). message = \"n42/mobileverify/pow/v1\" (23 bytes, no NUL) || pubkey(48) || nonce u64 BIG-endian = 79 bytes; " +
			"digest = legacy Keccak256(message); a nonce solves `bits` when the digest has >= bits leading zero bits, MSB first. " +
			"solved_nonce = the smallest solving nonce, which is what both the server's SolvePoW and the SDK's SolveRegistrationPoW find (they grind 0,1,2,... up to 1<<(bits+4)); bits 0 always verifies with nonce 0. " +
			"server_428 = the handler's answers with --mobileverify.pow-bits 8: missing nonce, then a non-decimal nonce (pow_nonce is a decimal uint64 STRING). " +
			"sdk_round_trip = the two request bodies the SDK client sends against that server: the plain register, then (after the 428) the retry with pow_nonce."},
		{"pubkey", hx(pub[:])},
		{"vectors", vectors},
		{"server_428", fxObj{
			{"pow_bits", 8},
			{"missing_nonce", missing.obj()},
			{"non_decimal_nonce", append(fxObj{{"request_body", string(badNonce)}}, bad.obj()...)},
		}},
		{"sdk_round_trip", capture.bodies},
	}
}

// 4. registry -----------------------------------------------------------------

// fxSpecRoot recomputes the accumulator root from the specification alone
// (MOBILEVERIFY_V3_PARITY.md 1.3) so the fixture also checks the document.
func fxSpecRoot(leaves map[bmt.Hash]bmt.Hash, keys []bmt.Hash, depth int) bmt.Hash {
	switch len(keys) {
	case 0:
		return bmt.Hash{}
	case 1:
		return leaves[keys[0]]
	}
	var left, right []bmt.Hash
	for _, k := range keys {
		if (k[depth/8]>>(7-uint(depth%8)))&1 == 0 {
			left = append(left, k)
		} else {
			right = append(right, k)
		}
	}
	l := fxSpecRoot(leaves, left, depth+1)
	r := fxSpecRoot(leaves, right, depth+1)
	var buf [64]byte
	copy(buf[:32], l[:])
	copy(buf[32:], r[:])
	return blake3.Sum256(buf[:])
}

func fxLeaf(keyHash bmt.Hash, idx uint32) ([]byte, bmt.Hash) {
	leaf := make([]byte, 0, 41)
	leaf = append(leaf, 0x4C)
	leaf = append(leaf, keyHash[:]...)
	leaf = binary.BigEndian.AppendUint64(leaf, uint64(idx))
	h := blake3.Sum256(append([]byte{0x00}, leaf...))
	if h != bmt.HashLeaf(leaf) {
		panic("leaf hash rule differs from lib/bmt")
	}
	return leaf, h
}

func fxRegistryFixture(t *testing.T, devs []*fxDevice) fxObj {
	reg := NewRegistry()
	coord := NewCohortCoordinator(reg, fixedLookup(nil), NewCertStore(8), types.Address{}, DefaultCohortConfig())
	srv := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(NewPacketCache(8), nil, "/test"), coord, NewCertStore(8))
	h := &fxHTTP{h: srv.srv.Handler}

	keyHashes := []fxObj{}
	for i, d := range devs {
		kh := bmt.HashKey(d.pubkey[:])
		if kh != bmt.Hash(blake3.Sum256(d.pubkey[:])) {
			t.Fatal("key hash is not blake3(pubkey)")
		}
		keyHashes = append(keyHashes, fxObj{{"device", i}, {"key_hash", hx(kh[:])}})
	}

	emptyRoot := reg.Root()
	steps := []fxObj{}
	emptyExport := reg.ExportCommitted()
	health0 := h.do("GET", "/mobileverify/health", "")

	batches := [][]int{{0}, {1, 2}, {3}}
	for step, batch := range batches {
		registered := []fxObj{}
		for _, i := range batch {
			p := devs[i].pop()
			first := h.do("POST", "/mobileverify/register", fxRegisterBody(devs[i].pubkey[:], p[:]))
			again := h.do("POST", "/mobileverify/register", fxRegisterBody(devs[i].pubkey[:], p[:]))
			registered = append(registered, fxObj{{"device", i}, {"register", first.obj()}, {"register_again_while_pending", again.obj()}})
		}
		healthPending := h.do("GET", "/mobileverify/health", "")
		root, n := fxCommit(reg, step)
		if n != len(batch) {
			t.Fatalf("step %d committed %d", step, n)
		}

		indices := []fxObj{}
		leaves := map[bmt.Hash]bmt.Hash{}
		var keys []bmt.Hash
		leafList := []fxObj{}
		for i, d := range devs {
			idx, ok := reg.Lookup(d.pubkey)
			if !ok {
				continue
			}
			kh := bmt.HashKey(d.pubkey[:])
			leaf, lh := fxLeaf(kh, idx)
			leaves[kh] = lh
			keys = append(keys, kh)
			indices = append(indices, fxObj{{"device", i}, {"index", idx}})
			leafList = append(leafList, fxObj{{"device", i}, {"index", idx}, {"leaf_bytes", hx(leaf)}, {"leaf_hash", hx(lh[:])}})
		}
		if spec := fxSpecRoot(leaves, keys, 0); spec != root {
			t.Fatalf("step %d: root %x, the specification's rule gives %x", step, root, spec)
		}
		export := reg.ExportCommitted()
		imported := NewRegistry()
		if r, err := imported.ImportCommitted(export); err != nil || r != root {
			t.Fatalf("step %d: import %v root %x want %x", step, err, r, root)
		}
		reRegister := []fxObj{}
		for _, i := range batch {
			p := devs[i].pop()
			reRegister = append(reRegister, fxObj{{"device", i},
				{"register", h.do("POST", "/mobileverify/register", fxRegisterBody(devs[i].pubkey[:], p[:])).obj()}})
		}
		steps = append(steps, fxObj{
			{"step", step},
			{"registered_devices", batch},
			{"registrations", registered},
			{"health_before_commit", healthPending.obj()},
			{"commit_last_seen_ms", fxCommitMs0 + uint64(step)*fxCommitStep},
			{"committed_now", n},
			{"indices", indices},
			{"leaves", leafList},
			{"root", hx(root[:])},
			{"export_committed", hx(export)},
			{"register_after_commit", reRegister},
			{"health_after_commit", h.do("GET", "/mobileverify/health", "").obj()},
			{"snapshot_route", fxObj{{"status", 200}, {"x_mobileverify_root", reg.Root().Hex()}, {"body_is", "export_committed"}}},
		})
	}
	return fxObj{
		{"_doc", "Registry accumulator over the four devices of keys.json, committed in three epochs: device 0; devices 1 and 2 together; device 3. " +
			"Register parks a PoP-verified key as pending; CommitEpoch sorts the pending keys by key_hash = blake3(pubkey48) ascending (bytewise) and appends them, index = append position. " +
			"BMT (lib/bmt): key = key_hash, value = BE8(index); leaf_bytes = 0x4C || key_hash || BE8(index); leaf_hash = blake3(0x00 || leaf_bytes); " +
			"internal = blake3(left32 || right32); path = key_hash bits MSB first; empty child = 32 zero bytes; a lone leaf in a subtree is that subtree's hash (no extension nodes), " +
			"two leaves sharing a prefix get a chain of internals with zero siblings. Every root here was also recomputed from that rule alone. " +
			"export_committed = ExportCommitted(): magic 0x4d565253 BE4 || version 0x01 || count BE4 || count x (pubkey48 || pop96 || active u8 || lastSeenMs BE8). " +
			"CommitEpoch stamps lastSeenMs with the node's wall clock (so snapshots differ between nodes); the fixture overwrote it with commit_last_seen_ms. " +
			"register/health = exact status and body of the gov5 handlers (pending: {committed:false,index:0}; committed: the index). " +
			"health 'accumulator' is the root with 0x, all zeros when empty."},
		{"key_hashes", keyHashes},
		{"empty", fxObj{{"root", hx(emptyRoot[:])}, {"export_committed", hx(emptyExport)}, {"health", health0.obj()}}},
		{"steps", steps},
	}
}

// 5. signer mask -----------------------------------------------------------------

func fxMasks(t *testing.T) fxObj {
	valid := []struct {
		idx   []MobileIndex
		bound int
	}{
		{[]MobileIndex{}, 1},
		{[]MobileIndex{0}, 1},
		{[]MobileIndex{3}, 4},
		{[]MobileIndex{0, 1, 2}, 3},
		{[]MobileIndex{1, 5, 300}, 301},
		{[]MobileIndex{0, 65535, 70000}, 70001},
	}
	vs := []fxObj{}
	for _, v := range valid {
		m, err := EncodeMask(v.idx)
		fxMust(t, err)
		got, err := DecodeMask(m, v.bound)
		fxMust(t, err)
		if fmt.Sprint(got) != fmt.Sprint(v.idx) {
			t.Fatalf("mask round trip %v -> %v", v.idx, got)
		}
		vs = append(vs, fxObj{{"indices", v.idx}, {"bound", v.bound}, {"mask", hx(m)}})
	}
	invalid := []struct {
		class string
		mask  []byte
		bound int
	}{
		{"count exceeds remaining bytes", []byte{0x05, 0x00}, 100},
		{"count exceeds bound", []byte{0x03, 0x00, 0x00, 0x00}, 2},
		{"index at or beyond bound", []byte{0x01, 0x05}, 5},
		{"trailing bytes", []byte{0x01, 0x00, 0x00}, 10},
		{"truncated entry", []byte{0x02, 0x00, 0x80}, 10},
		{"delta overflow (u64::MAX gap)", append([]byte{0x02, 0x00}, binary.AppendUvarint(nil, ^uint64(0))...), 1 << 30},
		{"empty input (no count)", []byte{}, 10},
	}
	is := []fxObj{}
	for _, v := range invalid {
		_, err := DecodeMask(v.mask, v.bound)
		if err == nil {
			t.Fatalf("invalid mask %x accepted", v.mask)
		}
		is = append(is, fxObj{{"class", v.class}, {"mask", hx(v.mask)}, {"bound", v.bound}, {"error", err.Error()}})
	}
	_, unsorted := EncodeMask([]MobileIndex{2, 1})
	return fxObj{
		{"_doc", "Sparse signer mask (mask.go): uvarint(count) then count uvarints over the strictly ascending indices, the first raw, each later one as (index - previous - 1). " +
			"bound = the registry's IndexBound (one past the highest committed index). DecodeMask rejects: count > remaining bytes or > bound, a truncated uvarint, " +
			"a delta of u64::MAX or an index overflow, an index >= bound, and trailing bytes. 'signers' in the cert JSON is the decoded count (0 when decoding fails). " +
			"EncodeMask refuses a non-ascending input (encode_unsorted_error)."},
		{"valid", vs},
		{"invalid", is},
		{"encode_unsorted_error", unsorted.Error()},
	}
}

// 6. receipts -------------------------------------------------------------------

func fxReceipts(t *testing.T, devs []*fxDevice, hdr *block.Header) fxObj {
	var compatHash, compatRoot types.Hash
	for i := range compatHash {
		compatHash[i] = byte(i)
		compatRoot[i] = byte(0xFF - i)
	}
	const compatNumber = uint64(13_220_145)
	blockHash, number, root := hdr.Hash(), hdr.Number.Uint64(), hdr.ReceiptHash

	vec := func(name string, hash types.Hash, n uint64, r types.Hash) fxObj {
		msg := SigningMessage(hash, n, r)
		if !bytes.Equal(msg, evmsdk.BuildSigningMessage(hash, n, r)) {
			t.Fatal("signing message differs from the SDK")
		}
		sigs := []fxObj{}
		for i, d := range devs {
			s := d.sign(msg)
			sigs = append(sigs, fxObj{{"device", i}, {"signature", hx(s[:])}})
		}
		return fxObj{{"name", name}, {"block_hash", hx(hash[:])}, {"block_number", n}, {"receipts_root", hx(r[:])}, {"message", hx(msg)}, {"signatures", sigs}}
	}

	// The exact body the SDK POSTs, captured off the wire.
	reg := fxRegistry(t, devs, 2) // devices 0,1,2 committed; device 3 never registered
	coord := NewCohortCoordinator(reg, fixedLookup(map[types.Hash]uint64{blockHash: number}), NewCertStore(8), types.Address{}, DefaultCohortConfig())
	srv := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(NewPacketCache(8), nil, "/test"), coord, NewCertStore(8))
	capture := &fxCapture{inner: srv.srv.Handler}
	ts := httptest.NewServer(capture)
	defer ts.Close()
	client, err := evmsdk.NewMobileVerifyClient(ts.URL, hx(devs[0].ikm[:]))
	fxMust(t, err)
	sdkReceipt := &evmsdk.VerificationReceipt{
		BlockHash: blockHash, BlockNumber: number, ComputedReceiptsRoot: root,
		VerifierPubkey: devs[0].pubkey, TimestampMs: fxReceiptTs,
	}
	sdkReceipt.Signature = devs[0].sign(sdkReceipt.SigningMessage())
	idx0, err := client.SubmitReceipt(t.Context(), sdkReceipt)
	fxMust(t, err)
	if len(capture.bodies) != 1 {
		t.Fatalf("captured %d receipt bodies", len(capture.bodies))
	}
	sdkBody := capture.bodies[0]

	// Server-side outcomes on a fresh coordinator, in this order (the first
	// receipt for a block opens its window, which changes later answers).
	coord2 := NewCohortCoordinator(reg, fixedLookup(map[types.Hash]uint64{blockHash: number}), NewCertStore(8), types.Address{}, DefaultCohortConfig())
	srv2 := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(NewPacketCache(8), nil, "/test"), coord2, NewCertStore(8))
	h := &fxHTTP{h: srv2.srv.Handler}
	body := func(r *Receipt) string {
		b, _ := json.Marshal(map[string]any{
			"blockHash": hx(r.BlockHash[:]), "blockNumber": r.BlockNumber, "receiptsRoot": hx(r.ComputedReceiptsRoot[:]),
			"pubkey": hx(r.VerifierPubkey[:]), "signature": hx(r.Signature[:]), "timestampMs": r.TimestampMs,
		})
		return string(b)
	}
	outcome := func(name string, req string) fxObj {
		return append(fxObj{{"case", name}, {"request_body", req}}, h.do("POST", "/mobileverify/receipt", req).obj()...)
	}
	good0 := devs[0].receipt(blockHash, number, root)
	unknown := devs[0].receipt(types.Hash{0xde, 0xad}, number, root)
	wrongNum := devs[0].receipt(blockHash, number+1, root)
	unreg := devs[3].receipt(blockHash, number, root)
	badSig := devs[0].receipt(blockHash, number, root)
	badSig.Signature = devs[1].receipt(blockHash, number, root).Signature
	good1x := devs[1].receipt(blockHash, number, root)
	short := body(good0)
	short = strings.Replace(short, hx(blockHash[:]), hx(blockHash[:31]), 1)
	with0x := strings.Replace(body(devs[2].receipt(blockHash, number, root)), `"blockHash":"`, `"blockHash":"0x`, 1)

	cases := []fxObj{
		outcome("unknown block (no header for the hash): 404", body(unknown)),
		outcome("bad hex field length (block hash 31 bytes): 400", short),
		outcome("wrong number while no window is open for the block", body(wrongNum)),
		outcome("unregistered key (device 3, valid signature)", body(unreg)),
		outcome("bad signature (device 1's signature under device 0's pubkey)", body(badSig)),
		outcome("valid receipt from device 0", body(good0)),
		outcome("wrong number once the block's window is open (Collector.Add's message)", body(wrongNum)),
		outcome("valid receipt from device 2 with a 0x-prefixed blockHash", with0x),
	}
	coord2.OnBlockCommitted(number + 1) // intake closes at N+1
	cases = append(cases, outcome("valid receipt from device 1 after the chain reached N+1", body(good1x)))
	cases = append(cases, append(fxObj{{"case", "GET instead of POST"}}, h.do("GET", "/mobileverify/receipt", "").obj()...))

	return fxObj{
		{"_doc", "Receipt signing message: block_hash(32) || block_number u64 LITTLE-endian || receipts_root(32) = 72 bytes; signature = blst Sign(sk, message, dst " + fxBLSDST + "); timestampMs is not signed. " +
			"sdk_compat = the vector of sdk_compat_test.go (hash[i]=i, root[i]=0xff-i, number 13220145); header_full = header.json's full header (its hash, number, ReceiptHash). " +
			"sdk_request_body = the exact bytes evmsdk MobileVerifyClient.SubmitReceipt POSTs (json.Marshal of a map: keys sorted, hex WITHOUT 0x, numbers as JSON numbers) for device 0 on header_full with timestampMs " + fmt.Sprint(fxReceiptTs) + ". " +
			"server_outcomes: a CohortCoordinator-backed handler with devices 0,1,2 committed (indices as in registry.json after step 1) and only header_full known, the cases run IN ORDER (the first receipt for a block opens its window). " +
			"The wrong-number answer depends on whether the window is open: before, the coordinator's 'receipt number X, block is Y'; after, Collector.Add's 'got N/<hash[:8] hex> want M/<hash[:8] hex>'. " +
			"Intake closes when the coordinator sees height N+1."},
		{"dst", fxBLSDST},
		{"vectors", []fxObj{
			vec("sdk_compat", compatHash, compatNumber, compatRoot),
			vec("header_full", blockHash, number, root),
		}},
		{"sdk_request_body", sdkBody},
		{"sdk_response_index", idx0},
		{"server_outcomes", cases},
	}
}

// 7. certificate --------------------------------------------------------------

func fxCertJSON(t *testing.T, reg *Registry, certs []*MobileAttestationCert, hash types.Hash) fxResp {
	store := NewCertStore(8)
	store.Put(certs)
	srv := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(NewPacketCache(8), nil, "/test"), NewCohortCoordinator(reg, fixedLookup(nil), store, types.Address{}, DefaultCohortConfig()), store)
	return (&fxHTTP{h: srv.srv.Handler}).do("GET", "/mobileverify/cert/"+hash.Hex(), "")
}

func fxCert(t *testing.T, devs []*fxDevice, hdr *block.Header) fxObj {
	reg := fxRegistry(t, devs, 3)
	blockHash, number := hdr.Hash(), hdr.Number.Uint64()
	majority := hdr.ReceiptHash
	minority := types.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000bd")
	rootOf := func(i int) types.Hash {
		if i == 2 {
			return minority
		}
		return majority
	}

	col := NewCollector(reg, blockHash, number)
	for i, d := range devs {
		if _, err := col.Add(d.receipt(blockHash, number, rootOf(i))); err != nil {
			t.Fatalf("device %d: %v", i, err)
		}
	}
	certs, err := col.Close(fxClosedAtMs)
	fxMust(t, err)
	if len(certs) != 2 {
		t.Fatalf("buckets %d", len(certs))
	}
	winner, _, err := SelectMajorityBucket(certs, reg.IndexBound())
	fxMust(t, err)
	indices, err := winner.Verify(reg)
	fxMust(t, err)

	// The production path (single node, no peers) must store the same cert.
	store := NewCertStore(8)
	coord := NewCohortCoordinator(reg, fixedLookup(map[types.Hash]uint64{blockHash: number}), store, types.Address{}, DefaultCohortConfig())
	for i, d := range devs {
		if _, err := coord.Submit(d.receipt(blockHash, number, rootOf(i))); err != nil {
			t.Fatalf("coordinator submit %d: %v", i, err)
		}
	}
	for n := number + 1; n <= number+5; n++ {
		coord.OnBlockCommitted(n)
	}
	stored := store.Get(blockHash)
	if len(stored) != 1 || stored[0].AggregateSig != winner.AggregateSig || !bytes.Equal(stored[0].SignerMask, winner.SignerMask) || stored[0].ReceiptsRoot != winner.ReceiptsRoot {
		t.Fatalf("coordinator stored %d certs, not the collector's majority bucket", len(stored))
	}

	pks := []fxObj{}
	var pkList []blscommon.PublicKey
	sigs := []fxObj{}
	for _, idx := range indices {
		pk, err := reg.PublicKey(idx)
		fxMust(t, err)
		pkList = append(pkList, pk)
		for i, d := range devs {
			if bytes.Equal(d.pubkey[:], pk.Marshal()) {
				s := d.sign(SigningMessage(blockHash, number, majority))
				pks = append(pks, fxObj{{"index", idx}, {"device", i}, {"pubkey", hx(pk.Marshal())}})
				sigs = append(sigs, fxObj{{"index", idx}, {"device", i}, {"signature", hx(s[:])}})
			}
		}
	}
	aggPk := bls.AggregateMultiplePubkeys(pkList)

	cohortBody := fxCertJSON(t, reg, []*MobileAttestationCert{winner}, blockHash)
	windowBody := fxCertJSON(t, reg, certs, blockHash)
	missing := fxCertJSON(t, reg, nil, blockHash)
	return fxObj{
		{"_doc", "Certificate for header.json's full header: all four devices committed (registry.json order); devices 0,1,3 attest the header's ReceiptHash (majority), device 2 attests minority_root. " +
			"Collector.Close aggregates each root bucket: indices sorted, signatures summed in G2 in index order (aggregate_signature, 96-byte compressed), mask per mask.json. " +
			"Verification: decode the mask under the registry bound, sum the listed pubkeys in G1 (aggregate_pubkey), one Verify(aggregate_pubkey, message, dst " + fxBLSDST + "). " +
			"cohort_coordinator_response = GET /mobileverify/cert/<hash> as production serves it (CohortCoordinator stores only the majority bucket; ties -> smaller root hex): a JSON array of one object, " +
			"keys in struct order blockHash, blockNumber, receiptsRoot (0x), aggregateSig, signerMask (hex WITHOUT 0x), signers, windowClosedAt; body ends in \\n. " +
			"windowClosedAt is the node's wall clock at close (" + fmt.Sprint(fxClosedAtMs) + " here; the production coordinator's real run was checked to store the same aggregate and mask). " +
			"window_manager_response = the test-only WindowManager variant, which stores every bucket, majority first. no_cert_response = the 404 before any window closed."},
		{"block_hash", hx(blockHash[:])},
		{"block_number", number},
		{"majority_root", hx(majority[:])},
		{"minority_root", hx(minority[:])},
		{"message", hx(SigningMessage(blockHash, number, majority))},
		{"registry_index_bound", reg.IndexBound()},
		{"signer_indices", indices},
		{"signer_pubkeys", pks},
		{"signer_signatures", sigs},
		{"aggregate_pubkey", hx(aggPk.Marshal())},
		{"aggregate_signature", hx(winner.AggregateSig[:])},
		{"signer_mask", hx(winner.SignerMask)},
		{"minority_bucket", fxObj{{"aggregate_signature", hx(certs[1].AggregateSig[:])}, {"signer_mask", hx(certs[1].SignerMask)}}},
		{"cohort_coordinator_response", cohortBody.obj()},
		{"window_manager_response", windowBody.obj()},
		{"no_cert_response", missing.obj()},
	}
}

// 8. receipts root ---------------------------------------------------------------

func fxReceiptsRoot(t *testing.T) fxObj {
	cfg := params.ChainConfigByChainName(fxChainName)
	if cfg == nil {
		t.Fatal("chain config missing")
	}
	const number = 4_000_000
	success := &block.Receipt{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 21_000}
	failed := &block.Receipt{Status: block.ReceiptStatusFailed, CumulativeGasUsed: 21_000}
	withLogs := &block.Receipt{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 52_345, Logs: []*block.Log{
		{
			Address: types.HexToAddress("0x00000000000000000000000000000000000c0ffe"),
			Topics: []types.Hash{
				types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"),
				types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000beef"),
			},
			Data: bytes.Repeat([]byte{0xab}, 32),
		},
		{
			Address: types.HexToAddress("0x1111111111111111111111111111111111111111"),
			Topics:  []types.Hash{types.HexToHash("0x01"), types.HexToHash("0x02")},
			Data:    []byte{},
		},
	}}
	cases := []struct {
		name     string
		receipts block.Receipts
	}{
		{"no receipts", nil},
		{"one successful receipt, cumulative gas 21000, no logs", block.Receipts{success}},
		{"one failed receipt, cumulative gas 21000", block.Receipts{failed}},
		{"one receipt with two logs (2 topics each; data 32 bytes, then empty)", block.Receipts{withLogs}},
		{"three receipts: success, failed (cumulative 42000), two logs (cumulative 94345)", block.Receipts{
			success,
			{Status: block.ReceiptStatusFailed, CumulativeGasUsed: 42_000},
			{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 94_345, Logs: withLogs.Logs},
		}},
	}
	out := []fxObj{}
	for _, c := range cases {
		var concat []byte
		items := []string{}
		for i := range c.receipts {
			var buf bytes.Buffer
			c.receipts.EncodeIndex(i, &buf)
			items = append(items, hx(buf.Bytes()))
			concat = append(concat, buf.Bytes()...)
		}
		root := internal.ReceiptsRootFor(cfg, number, c.receipts)
		if root != types.BytesToHash(crypto.Keccak256(concat)) {
			t.Fatalf("%s: root is not keccak of the concatenation", c.name)
		}
		out = append(out, fxObj{{"name", c.name}, {"receipt_rlp", items}, {"concatenation", hx(concat)}, {"root", hx(root[:])}})
	}
	return fxObj{
		{"_doc", "Native-chain receipts root, internal.ReceiptsRootFor under " + fxChainName + " (EthereumReceiptEncoding false) at block 4000000: " +
			"root = keccak256(concat_i RLP([status, cumulativeGasUsed, [[address20, [topic32...], data]...]])) (block.Receipts.EncodeIndex: no type byte, no bloom, " +
			"status as an RLP uint: 1 -> 0x01, 0 -> 0x80). No receipts: keccak256 of nothing (0xc5d2...a470), not the empty trie root."},
		{"chain", fxChainName},
		{"cases", out},
	}
}

// 9. headers ---------------------------------------------------------------------

func fxFullHeader(t *testing.T, devs []*fxDevice) *block.Header {
	reg := fxRegistry(t, devs, 3)
	regRoot := reg.Root()
	w := types.HexToHash("0x2121212121212121212121212121212121212121212121212121212121212121")
	pbr := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	rq := types.HexToHash("0x2323232323232323232323232323232323232323232323232323232323232323")
	bal := types.HexToHash("0x2424242424242424242424242424242424242424242424242424242424242424")
	blobUsed, excess, slot := uint64(131_072), uint64(393_216), uint64(0x0102030405)
	// HotStuff extra shape (no QC): "N42H" || view u64 LE || 96-byte BLS seal.
	extra := []byte("N42H")
	extra = binary.LittleEndian.AppendUint64(extra, 0x0000_0001_0203_0405)
	for i := 0; i < 96; i++ {
		extra = append(extra, byte(0xa0+i%16))
	}
	var bloom block.Bloom
	for i := range bloom {
		bloom[i] = byte(i * 7)
	}
	diff := new(uint256.Int)
	diff.SetFromHex("0x0102030405060708111213141516171821222324252627283132333435363738")
	return &block.Header{
		ParentHash:          types.HexToHash("0x0101010101010101010101010101010101010101010101010101010101010101"),
		UncleHash:           types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:            types.HexToAddress("0x0202020202020202020202020202020202020202"),
		Root:                types.HexToHash("0x0303030303030303030303030303030303030303030303030303030303030303"),
		TxHash:              types.HexToHash("0x0404040404040404040404040404040404040404040404040404040404040404"),
		ReceiptHash:         types.HexToHash("0x0505050505050505050505050505050505050505050505050505050505050505"),
		Bloom:               bloom,
		Difficulty:          diff,
		Number:              uint256.NewInt(4_000_123),
		GasLimit:            60_000_000,
		GasUsed:             21_000,
		Time:                1_790_000_000,
		Extra:               extra,
		MixDigest:           types.HexToHash("0x0606060606060606060606060606060606060606060606060606060606060606"),
		Nonce:               block.EncodeNonce(0x0708090a0b0c0d0e),
		BaseFee:             uint256.NewInt(7_000_000_000),
		WithdrawalsHash:     &w,
		BlobGasUsed:         &blobUsed,
		ExcessBlobGas:       &excess,
		ParentBeaconRoot:    &pbr,
		RequestsHash:        &rq,
		BlockAccessListHash: &bal,
		SlotNumber:          &slot,
		MobileRegistryRoot:  &regRoot,
	}
}

func fxProtoFields(m protoreflect.Message) []string {
	var out []string
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		out = append(out, fmt.Sprintf("%d %s", fd.Number(), fd.Name()))
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		var a, b int
		fmt.Sscan(out[i], &a)
		fmt.Sscan(out[j], &b)
		return a < b
	})
	return out
}

func fxHeaderObj(t *testing.T, name string, h *block.Header) fxObj {
	enc, err := h.Marshal()
	fxMust(t, err)
	protoLen := binary.LittleEndian.Uint32(enc[len(enc)-4:])
	pb := enc[:protoLen]
	trailer := enc[protoLen:]
	if string(trailer[:4]) != "N42H" {
		t.Fatalf("%s: no trailer", name)
	}
	pbMsg := new(types_pb.Header)
	fxMust(t, proto.Unmarshal(pb, pbMsg))
	rlpBytes, err := rlp.EncodeToBytes(h)
	fxMust(t, err)
	hash := h.Hash()
	if types.BytesToHash(crypto.Keccak256(rlpBytes)) != hash {
		t.Fatalf("%s: hash is not keccak(rlp)", name)
	}
	var back block.Header
	fxMust(t, back.Unmarshal(enc))
	if back.Hash() != hash {
		t.Fatalf("%s: Marshal/Unmarshal changed the hash", name)
	}
	return fxObj{
		{"name", name},
		{"marshal", hx(enc)},
		{"protobuf", hx(pb)},
		{"protobuf_fields_present", fxProtoFields(pbMsg.ProtoReflect())},
		{"trailer", hx(trailer)},
		{"trailer_flags", fmt.Sprintf("%02x", trailer[4])},
		{"proto_len", protoLen},
		{"rlp", hx(rlpBytes)},
		{"hash", hx(hash[:])},
	}
}

func fxHeaders(t *testing.T, full *block.Header) fxObj {
	minimal := &block.Header{Number: uint256.NewInt(42), Difficulty: uint256.NewInt(0)}
	j, err := json.Marshal(full)
	fxMust(t, err)
	return fxObj{
		{"_doc", "gov5 block.Header.Marshal() = protobuf(types_pb.Header) || trailer. Trailer = \"N42H\" || flags u8 || UncleHash32 || [WithdrawalsHash32 if 0x01] || [ParentBeaconRoot32 if 0x02] || " +
			"[RequestsHash32 if 0x04] || [BlobGasUsed u64 LE if 0x08] || [ExcessBlobGas u64 LE if 0x10] || [BlockAccessListHash32 if 0x20] || [MobileRegistryRoot32 if 0x40] || " +
			"[SlotNumber u64 LE if 0x80] || protoLen u32 LE (= offset of the trailer). Protobuf words: H128{1:hi u64, 2:lo u64}; H256{1:hi H128, 2:lo H128}; H160{1:hi H128, 2:lo u32}; " +
			"H2048 = nested halves down to H128. A 32-byte hash maps to big-endian u64 chunks hash[0:8] -> hi.hi, [8:16] -> hi.lo, [16:24] -> lo.hi, [24:32] -> lo.lo; " +
			"an address: [0:8] -> hi.hi, [8:16] -> hi.lo, [16:20] -> lo (u32 BE); a uint256 (Difficulty, Number, BaseFee) with little-endian u64 limbs w[0..3]: hi.hi = w[3], hi.lo = w[2], lo.hi = w[1], lo.lo = w[0] " +
			"(the same as reading its 32-byte big-endian form like a hash). proto3 drops zero scalars, so a zero word is an EMPTY H128 message, but every hash/bloom/difficulty/number field is " +
			"emitted as a present (possibly empty) message; BaseFee is absent when nil; field 14 (Signature) and 19 (LtHashRoot) are never written; 17/18 BlobGasUsed/ExcessBlobGas carry the values " +
			"and 24/25 (HasBlobGasUsed/HasExcessBlobGas, bool, in types.pb.go but NOT in types.proto) say they are present. hash = keccak256(rlp), rlp = Ethereum header order 1-21 " +
			"then 22 BlockAccessListHash, 23 SlotNumber, 24 MobileRegistryRoot (block.Header field order, rlp optional tags). " +
			"full: every field non-zero, Extra in the HotStuff shape \"N42H\" || view u64 LE || 96-byte seal (no QC), MobileRegistryRoot = registry.json's final root. " +
			"minimal: Number 42, Difficulty 0, everything else zero or nil (BaseFee nil, UncleHash all zeros)."},
		{"full_header_json", string(j)},
		{"headers", []fxObj{fxHeaderObj(t, "full", full), fxHeaderObj(t, "minimal", minimal)}},
	}
}

// 10. transactions -----------------------------------------------------------------

func fxTransactions(t *testing.T) fxObj {
	cfg := params.ChainConfigByChainName(fxChainName)
	key, err := crypto.HexToECDSA("4646464646464646464646464646464646464646464646464646464646464646")
	fxMust(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x3535353535353535353535353535353535353535")
	chainID := cfg.ChainID
	cid := uint256.MustFromBig(chainID)

	type txCase struct {
		name   string
		inner  transaction.TxData
		signer transaction.Signer
	}
	cases := []txCase{
		{"legacy (EIP-155)", &transaction.LegacyTx{
			Nonce: 9, GasPrice: uint256.NewInt(20_000_000_000), Gas: 21_000, To: &to,
			Value: uint256.NewInt(1_000_000_000_000_000_000), Data: nil,
		}, transaction.NewEIP155Signer(chainID)},
		{"eip-2930 access list", &transaction.AccessListTx{
			ChainID: cid, Nonce: 10, GasPrice: uint256.NewInt(20_000_000_000), Gas: 30_000, To: &to,
			Value: uint256.NewInt(1), Data: []byte{0xde, 0xad, 0xbe, 0xef},
			AccessList: transaction.AccessList{{Address: to, StorageKeys: []types.Hash{types.HexToHash("0x01")}}},
		}, transaction.NewEIP2930Signer(chainID)},
		{"eip-1559 dynamic fee", &transaction.DynamicFeeTx{
			ChainID: cid, Nonce: 11, GasTipCap: uint256.NewInt(2_000_000_000), GasFeeCap: uint256.NewInt(30_000_000_000),
			Gas: 21_000, To: &to, Value: uint256.NewInt(12_345),
		}, transaction.NewLondonSigner(chainID)},
	}
	out := []fxObj{}
	for _, c := range cases {
		signed, err := transaction.SignTx(transaction.NewTx(c.inner), c.signer, key)
		fxMust(t, err)
		withoutFrom, err := signed.Marshal()
		fxMust(t, err)
		recovered, err := transaction.Sender(c.signer, signed)
		fxMust(t, err)
		if recovered != sender {
			t.Fatal("sender mismatch")
		}
		signed.SetFrom(sender)
		enc, err := signed.Marshal()
		fxMust(t, err)
		eth, err := transaction.EncodeEthereumTransaction(signed)
		fxMust(t, err)
		pbMsg := new(types_pb.Transaction)
		fxMust(t, proto.Unmarshal(enc, pbMsg))
		var back transaction.Transaction
		fxMust(t, back.Unmarshal(enc))
		if back.Hash() != signed.Hash() || back.From() == nil || *back.From() != sender {
			t.Fatalf("%s: protobuf round trip lost the hash or from", c.name)
		}
		v, r, s := signed.RawSignatureValues()
		out = append(out, fxObj{
			{"name", c.name},
			{"type", signed.Type()},
			{"protobuf", hx(enc)},
			{"protobuf_fields_present", fxProtoFields(pbMsg.ProtoReflect())},
			{"protobuf_without_from", hx(withoutFrom)},
			{"ethereum_encoding", hx(eth)},
			{"hash", fxHashHex(signed.Hash())},
			{"from", hx(sender[:])},
			{"v", v.Hex()}, {"r", r.Hex()}, {"s", s.Hex()},
		})
	}
	return fxObj{
		{"_doc", "gov5 protobuf transactions (transaction.Marshal = types_pb.Transaction with V/R/S), the bytes a StreamPacket carries. Signed with secp256k1 key 0x4646...46 on chain " + fxChainName +
			" (chain id " + chainID.String() + "). protobuf = with field 11 'from' set (SetFrom after signing, as the packet producer's transactions carry it; AsMessage then skips recovery); " +
			"protobuf_without_from = the same transaction straight out of SignTx, which does NOT set from. Field numbers: 1 type, 2 nonce, 3 gasPrice (H256), 4 gas, 5 feePerGas, 6 priorityFeePerGas, " +
			"7 value, 8 data, 9 sign, 10 to (H160), 11 from, 12 chainID (u64; NOT written for legacy, whose chain id lives in v), 16 v, 14 r, 15 s (H256, uint256 word mapping as header.json), 23 accessList. " +
			"Field 13 hash is never written by Marshal. A uint256 field that is non-nil but zero (gasPrice of a 1559 tx is its feeCap) is an H256 of empty H128s. " +
			"ethereum_encoding = EncodeEthereumTransaction (legacy RLP / typed 0x01, 0x02 envelope); hash = keccak256 of it."},
		{"chain_id", chainID.Uint64()},
		{"transactions", out},
	}
}

// 11. read log ----------------------------------------------------------------------

func fxReadLog(t *testing.T) fxObj {
	contract := types.HexToHash("0x9c1b4bfdd5bd0e9d8b1d7a5a9e7c6f3f7b2d5f8a1c3e5d7f9b1a3c5e7d9f1b3a")
	max := new(uint256.Int).SetAllOne()
	two255 := new(uint256.Int).Lsh(uint256.NewInt(1), 255)
	bal := uint256.NewInt(1_000_000_000_000_000_000)
	type entry struct {
		desc string
		e    evmsdk.ReadLogEntry
	}
	entries := []entry{
		{"account not found", evmsdk.ReadLogAccountNotFound{}},
		{"EOA nonce 0, no balance", evmsdk.ReadLogAccount{Nonce: 0, Balance: uint256.NewInt(0), CodeHash: evmsdk.EmptyCodeHash}},
		{"EOA nonce 1, balance 1 ether", evmsdk.ReadLogAccount{Nonce: 1, Balance: bal, CodeHash: evmsdk.EmptyCodeHash}},
		{"EOA nonce 300, balance 1", evmsdk.ReadLogAccount{Nonce: 300, Balance: uint256.NewInt(1), CodeHash: evmsdk.EmptyCodeHash}},
		{"EOA nonce 2^40, no balance", evmsdk.ReadLogAccount{Nonce: 1 << 40, Balance: uint256.NewInt(0), CodeHash: evmsdk.EmptyCodeHash}},
		{"EOA nonce 2^55 (7 bytes), balance max", evmsdk.ReadLogAccount{Nonce: 1 << 55, Balance: max, CodeHash: evmsdk.EmptyCodeHash}},
		{"EOA nonce 2^63 (8 bytes: bit 0x40)", evmsdk.ReadLogAccount{Nonce: 1 << 63, Balance: uint256.NewInt(0), CodeHash: evmsdk.EmptyCodeHash}},
		{"contract nonce 1, no balance", evmsdk.ReadLogAccount{Nonce: 1, Balance: uint256.NewInt(0), CodeHash: contract}},
		{"contract nonce 0, balance 2^255", evmsdk.ReadLogAccount{Nonce: 0, Balance: two255, CodeHash: contract}},
		{"storage 0", evmsdk.ReadLogStorage{Value: uint256.NewInt(0)}},
		{"storage 1", evmsdk.ReadLogStorage{Value: uint256.NewInt(1)}},
		{"storage 0x0100", evmsdk.ReadLogStorage{Value: uint256.NewInt(256)}},
		{"storage 2^255", evmsdk.ReadLogStorage{Value: two255}},
		{"storage max", evmsdk.ReadLogStorage{Value: max}},
		{"block hash", evmsdk.ReadLogBlockHash{Hash: types.HexToHash("0xabababababababababababababababababababababababababababababababab")}},
	}
	log := make([]evmsdk.ReadLogEntry, len(entries))
	for i, e := range entries {
		log[i] = e.e
	}
	enc := evmsdk.EncodeReadLog(log)
	dec, err := evmsdk.DecodeReadLog(enc)
	fxMust(t, err)
	if !bytes.Equal(evmsdk.EncodeReadLog(dec), enc) || len(dec) != len(entries) {
		t.Fatal("read log does not round-trip")
	}
	out := []fxObj{}
	for i, e := range entries {
		one := evmsdk.EncodeReadLog([]evmsdk.ReadLogEntry{e.e})[4:]
		var d fxObj
		switch v := dec[i].(type) {
		case evmsdk.ReadLogAccountNotFound:
			d = fxObj{{"kind", "account_not_found"}}
		case evmsdk.ReadLogAccount:
			d = fxObj{{"kind", "account"}, {"nonce", fmt.Sprint(v.Nonce)}, {"balance", v.Balance.Hex()}, {"code_hash", hx(v.CodeHash[:])}}
		case evmsdk.ReadLogStorage:
			d = fxObj{{"kind", "storage"}, {"value", v.Value.Hex()}}
		case evmsdk.ReadLogBlockHash:
			d = fxObj{{"kind", "block_hash"}, {"hash", hx(v.Hash[:])}}
		}
		out = append(out, fxObj{{"desc", e.desc}, {"entry_bytes", hx(one)}, {"decoded", d}})
	}
	bad := [][]byte{
		{0x01, 0, 0, 0, 0x02},             // account header without the exists bit
		{0x01, 0, 0, 0, 0x81},             // storage of length 1, value missing
		{0x01, 0, 0, 0, 0xC0, 0x00},       // block hash entry truncated
		{0x21, 0xA1, 0x07, 0x00},          // count 500001 > max
		{0x01, 0, 0, 0, 0x11, 0x21, 0x00}, // balance length 33
		{0x01, 0, 0, 0, 0xA1},             // storage length 33
		{0x01, 0, 0, 0, 0xC1},             // unknown header byte
	}
	badOut := []fxObj{}
	for _, b := range bad {
		_, err := evmsdk.DecodeReadLog(b)
		if err == nil {
			t.Fatalf("bad read log %x accepted", b)
		}
		badOut = append(badOut, fxObj{{"bytes", hx(b)}, {"error", err.Error()}})
	}
	lax := [][]byte{
		{0x01, 0, 0, 0, 0x05, 0x00, 0x01},                // nonce 1 written in 2 bytes
		{0x01, 0, 0, 0, 0x41, 0, 0, 0, 0, 0, 0, 0, 0x01}, // nonce 1 written in 8 bytes (0x40)
		{0x01, 0, 0, 0, 0x82, 0x00, 0x01},                // storage 1 written in 2 bytes
		{0x01, 0, 0, 0, 0x11, 0x00},                      // balance bit with a zero-length balance
		{0x01, 0, 0, 0, 0x21, 0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c, 0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0, 0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b, 0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70}, // contract bit with the empty code hash
	}
	laxOut := []fxObj{}
	for _, b := range lax {
		dec, err := evmsdk.DecodeReadLog(b)
		if err != nil {
			laxOut = append(laxOut, fxObj{{"bytes", hx(b)}, {"accepted", false}, {"error", err.Error()}})
			continue
		}
		laxOut = append(laxOut, fxObj{{"bytes", hx(b)}, {"accepted", true}, {"reencoded", hx(evmsdk.EncodeReadLog(dec))}})
	}
	return fxObj{
		{"_doc", "Read log codec (cmd/evmsdk/read_log.go). encoded = count u32 LE || entries. 0x00 = account not found. Account header: 0x01 exists | nonce length (0..7) << 1 | 0x40 when the nonce needs 8 bytes " +
			"| 0x10 has balance (non-zero) | 0x20 contract (codeHash != keccak(empty)); then nonce big-endian with leading zeros stripped; then, with 0x10, len u8 || balance big-endian stripped; then codeHash32 with 0x20. " +
			"Storage: 0x80 + len (0..32) || value big-endian stripped. Block hash: 0xC0 || hash32 (never produced by the Go recorder). Entries are positional, no keys. " +
			"decoded: nonce as a decimal string, balance/value as 0x-hex quantities. invalid = inputs DecodeReadLog rejects. non_canonical_inputs = non-minimal encodings and whether gov5's decoder accepts them (a strict decoder would reject what a phone accepts)."},
		{"encoded", hx(enc)},
		{"entries", out},
		{"invalid", badOut},
		{"non_canonical_inputs", laxOut},
	}
}

// 12. stream packets ------------------------------------------------------------------

// fxStateReader is an in-memory state.StateReader for building a packet.
type fxStateReader struct {
	accounts map[types.Address]*account.StateAccount
	codes    map[types.Hash][]byte
}

func (s *fxStateReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	return s.accounts[addr], nil
}
func (s *fxStateReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (s *fxStateReader) ReadAccountCode(_ types.Address, codeHash types.Hash) ([]byte, error) {
	return s.codes[codeHash], nil
}
func (s *fxStateReader) ReadAccountCodeSize(a types.Address, codeHash types.Hash) (int, error) {
	c, err := s.ReadAccountCode(a, codeHash)
	return len(c), err
}

var _ state.StateReader = (*fxStateReader)(nil)

func fxPacketObj(t *testing.T, name string, encoded []byte, verify []string) fxObj {
	pkt, err := evmsdk.DecodeStreamPacket(encoded)
	fxMust(t, err)
	var hdr block.Header
	fxMust(t, hdr.Unmarshal(pkt.HeaderRLP))
	if hdr.Hash() != pkt.BlockHash {
		t.Fatalf("%s: packet hash is not its header's", name)
	}
	log, err := evmsdk.DecodeReadLog(pkt.ReadLogData)
	fxMust(t, err)
	kinds := []string{}
	for _, e := range log {
		switch v := e.(type) {
		case evmsdk.ReadLogAccountNotFound:
			kinds = append(kinds, "account_not_found")
		case evmsdk.ReadLogAccount:
			kinds = append(kinds, fmt.Sprintf("account nonce=%d balance=%s code_hash=%x", v.Nonce, v.Balance.Hex(), v.CodeHash[:]))
		case evmsdk.ReadLogStorage:
			kinds = append(kinds, "storage "+v.Value.Hex())
		case evmsdk.ReadLogBlockHash:
			kinds = append(kinds, fmt.Sprintf("block_hash %x", v.Hash[:]))
		}
	}
	results := []fxObj{}
	for _, chain := range verify {
		cfg := params.ChainConfigByChainName(chain)
		res, err := evmsdk.ExecuteAndVerifyV2(pkt, evmsdk.NewCodeCache(16), cfg)
		if err != nil {
			t.Fatalf("%s under %s: %v", name, chain, err)
		}
		results = append(results, fxObj{{"chain", chain}, {"verified", true}, {"computed_receipts_root", hx(res.ComputedReceiptsRoot[:])}, {"tx_count", res.TxCount}})
	}
	txs := []string{}
	for _, tx := range pkt.Transactions {
		txs = append(txs, hx(tx))
	}
	codes := []fxObj{}
	for _, c := range pkt.Bytecodes {
		codes = append(codes, fxObj{{"hash", hx(c.Hash[:])}, {"code", hx(c.Code)}})
	}
	return fxObj{
		{"name", name},
		{"packet", hx(encoded)},
		{"decoded", fxObj{
			{"flags", fmt.Sprintf("%02x", encoded[3])},
			{"block_hash", hx(pkt.BlockHash[:])},
			{"block_number", hdr.Number.Uint64()},
			{"header_bytes", hx(pkt.HeaderRLP)},
			{"tx_count", len(pkt.Transactions)},
			{"transactions", txs},
			{"read_log_bytes", hx(pkt.ReadLogData)},
			{"read_log_entry_count", len(log)},
			{"read_log_entries", kinds},
			{"bytecode_count", len(pkt.Bytecodes)},
			{"bytecodes", codes},
		}},
		{"execute_and_verify_v2", results},
	}
}

func fxStreamPackets(t *testing.T) fxObj {
	cfg := params.ChainConfigByChainName(fxChainName)
	// Empty block #88, the executablePacket of session_e2e_test.go.
	root88 := internal.ReceiptsRootFor(cfg, 88, nil)
	if root88 != internal.ReceiptsRootFor(params.MainnetChainConfig, 88, nil) {
		t.Fatal("empty-block receipts root differs between the chains")
	}
	_, empty := executablePacket(t, 88, root88)

	// One value transfer under the qmdb chain, block-start system calls first,
	// recorded through the producer's own recorder (blockstart_roundtrip_test.go).
	key, err := crypto.HexToECDSA("4646464646464646464646464646464646464646464646464646464646464646")
	fxMust(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x00000000000000000000000000000000000c0ffe")
	senderAcc := account.NewAccount()
	senderAcc.Initialised = true
	senderAcc.Nonce = 577
	senderAcc.Balance.SetUint64(1e18)
	sysCode := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
	sysAcc := account.NewAccount()
	sysAcc.Initialised = true
	sysAcc.Nonce = 1
	sysAcc.CodeHash = crypto.Keccak256Hash(sysCode)
	beaconAcc, historyAcc := sysAcc, sysAcc
	reader := &fxStateReader{
		accounts: map[types.Address]*account.StateAccount{
			sender: &senderAcc, params.BeaconRootsAddress: &beaconAcc, vm.HistoryStorageAddress: &historyAcc,
		},
		codes: map[types.Hash][]byte{sysAcc.CodeHash: sysCode},
	}
	signed, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID: uint256.MustFromBig(cfg.ChainID), Nonce: 577, GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(10_000_000_000), Gas: 21_000, To: &to, Value: uint256.NewInt(1),
	}), transaction.NewLondonSigner(cfg.ChainID), key)
	fxMust(t, err)
	signed.SetFrom(sender)
	beaconRoot := types.HexToHash("0xbe00000000000000000000000000000000000000000000000000000000000000")
	hdr := &block.Header{
		ParentHash:       types.HexToHash("0x01"),
		UncleHash:        types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:         types.HexToAddress("0x000000000000000000000000000000000000beef"),
		Root:             types.HexToHash("0x02"),
		TxHash:           types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Difficulty:       uint256.NewInt(0),
		Number:           uint256.NewInt(14_300_000),
		GasLimit:         30_000_000,
		Time:             1_790_000_000,
		Extra:            []byte{},
		BaseFee:          uint256.NewInt(1_000_000_000),
		ParentBeaconRoot: &beaconRoot,
	}
	rec := streamverify.NewReadLogRecorder(reader, nil)
	ibs := state.New(rec)
	engine := apos.NewFaker()
	fxMust(t, internal.ProcessExecutionBlockStart(hdr.ParentBeaconRoot, cfg, ibs, hdr, engine))
	gp := new(common.GasPool).AddGas(hdr.GasLimit)
	var used uint64
	ibs.Prepare(signed.Hash(), types.Hash{}, 0)
	receipt, _, err := internal.ApplyTransaction(cfg, func(uint64) types.Hash { return types.Hash{} }, engine, nil, gp,
		ibs, state.NewNoopWriter(), hdr, signed, &used, vm.Config{})
	fxMust(t, err)
	hdr.GasUsed = used
	hdr.ReceiptHash = internal.ReceiptsRootFor(cfg, hdr.Number.Uint64(), []*block.Receipt{receipt})
	pkt, err := streamverify.BuildStreamPacket(hdr, transaction.Transactions{signed}, rec)
	fxMust(t, err)
	transfer, err := evmsdk.EncodeStreamPacket(pkt)
	fxMust(t, err)

	return fxObj{
		{"_doc", "StreamPacket V2 (cmd/evmsdk/stream_packet.go): \"N2\" (0x4e 0x32) || version 0x01 || flags (0x01 iff bytecodes) || block_hash32 || header_len u32 LE || header (block.Header.Marshal, header.json) || " +
			"tx_count u32 LE || {tx_len u32 LE || tx (protobuf, transactions.json)}* || read_log_len u32 LE || read_log (read_log.json) || bytecode_count u32 LE || {code_hash32 || code_len u32 LE || code}*, " +
			"bytecodes sorted by hash. empty_block_88 = session_e2e_test.go's executablePacket (the header carries no chain, so the bytes are the same for mainnet and " + fxChainName +
			"; it verifies under both). transfer = one EIP-1559 value transfer (from set) under " + fxChainName + " at block 14300000 / time 1790000000 (Cancun+Prague): the read log opens with the " +
			"block-start system calls (EIP-4788 beacon roots, EIP-2935 history storage; both stubbed as nonce-1 contracts with code 0x60006000f3), recorded by the producer's ReadLogRecorder in Go IntraBlockState order. " +
			"execute_and_verify_v2 = evmsdk.ExecuteAndVerifyV2 under each chain config, with the receipts root it computed (equal to the header's)."},
		{"packets", []fxObj{
			fxPacketObj(t, "empty_block_88", empty, []string{"mainnet", fxChainName}),
			fxPacketObj(t, "transfer", transfer, []string{fxChainName}),
		}},
	}
}

// 13. topics -----------------------------------------------------------------------------

func fxTopics(t *testing.T, devs []*fxDevice) fxObj {
	gh := params.GenesisHashByChainName(fxChainName)
	if gh == nil {
		t.Fatal("genesis hash missing")
	}
	digest := [4]byte{}
	copy(digest[:], gh[:4])
	suffix := (&encoder.SszNetworkEncoder{}).ProtocolSuffix()
	topic := func(format string) string { return fmt.Sprintf(format, digest) + suffix }
	p0 := devs[0].pop()
	return fxObj{
		{"_doc", "Gossip topics as internal/node/node.go builds them: fmt.Sprintf(\"/n42/%x/<name>\", genesis4) + encoder suffix \"/ssz_snappy\", genesis4 = the first 4 bytes of the chain's " +
			"canonical genesis hash (params.GenesisHashByChainName; --p2p.genesis-override replaces it). Payloads are raw despite the suffix: a packet is the StreamPacket bytes, " +
			"a registration is pubkey48 || pop96 (registration_payload_device0, 144 bytes)."},
		{"chain", fxChainName},
		{"genesis_hash", hx(gh[:])},
		{"genesis4", hx(digest[:])},
		{"mobileverify_packet", topic(p2p.MobilePacketTopicFormat)},
		{"mobileverify_registration", topic(p2p.MobileRegistrationTopicFormat)},
		{"mobileverify_cohort_index", topic(p2p.MobileCohortIndexTopicFormat)},
		{"mobileverify_cohort_reveal", topic(p2p.MobileCohortRevealTopicFormat)},
		{"mobileverify_cohort_cert", topic(p2p.MobileCohortCertTopicFormat)},
		{"registration_payload_device0", hx(encodeRegistration(devs[0].pubkey, p0))},
	}
}

func fxHashHex(h types.Hash) string { return hx(h[:]) }
