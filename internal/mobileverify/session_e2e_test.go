package mobileverify

import (
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// executablePacket builds the packet of an empty block that re-executes
// under the mainnet rules, with the given receipts root in its header.
func executablePacket(t *testing.T, number uint64, receiptsRoot types.Hash) (types.Hash, []byte) {
	t.Helper()
	hdr := &block.Header{
		ParentHash:  types.Hash{0x01},
		UncleHash:   types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:    types.Address{0xbe, 0xef},
		Root:        types.Hash{0x02},
		TxHash:      types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		ReceiptHash: receiptsRoot,
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(number),
		GasLimit:    30_000_000,
		Time:        1700000000,
		Extra:       []byte{},
		BaseFee:     uint256.NewInt(1_000_000_000),
	}
	hb, err := hdr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := evmsdk.EncodeStreamPacket(&evmsdk.StreamPacket{
		BlockHash:   hdr.Hash(),
		HeaderRLP:   hb,
		ReadLogData: evmsdk.EncodeReadLog(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return hdr.Hash(), encoded
}

// The gomobile session drives the real server through the whole phone
// loop: register (provisional until the epoch commits), find the head at
// /latest, re-execute and submit, and read the certificate back once the
// window closes. A block that does not re-execute to its header's
// receipts root is refused before anything is submitted.
func TestSessionEndToEnd(t *testing.T) {
	const number = 88
	root := internal.ReceiptsRootFor(params.MainnetChainConfig, number, nil)
	hash, encoded := executablePacket(t, number, root)
	badHash, badEncoded := executablePacket(t, number+1, types.Hash{0xba, 0xd0})

	reg := NewRegistry()
	cache := NewPacketCache(8)
	cache.Put(hash, number, encoded)
	cache.Put(badHash, number+1, badEncoded)
	packets := NewPacketService(cache, nil, "/test")
	store := NewCertStore(8)
	windows := NewWindowManager(reg, fixedLookup(map[types.Hash]uint64{hash: number, badHash: number + 1}), 60*time.Millisecond, store)
	defer windows.Stop()
	srv := NewHTTPServer("127.0.0.1:0", reg, packets, windows, store)
	srv.SetHead(func() (uint64, types.Hash, bool) { return number, hash, true })
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	if _, err := evmsdk.NewMobileVerifySession(ts.URL, "00", "no-such-chain"); err == nil {
		t.Fatal("unknown chain accepted")
	}
	k, err := evmsdk.GenerateMobileBLSKey()
	if err != nil {
		t.Fatal(err)
	}
	key := k.PrivHex
	sess, err := evmsdk.NewMobileVerifySession(ts.URL+"/", key, "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	if pub, _ := evmsdk.MobileBLSPublicKey(key); sess.PubkeyHex() != pub {
		t.Fatalf("pubkey %s, want %s", sess.PubkeyHex(), pub)
	}
	decode := func(s string, err error, out any) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(s), out); err != nil {
			t.Fatalf("%v: %s", err, s)
		}
	}

	type registration struct {
		Index     uint32 `json:"index"`
		Committed bool   `json:"committed"`
	}
	var r registration
	if _, err := sess.Register(); err != nil {
		t.Fatal(err)
	}
	s, err := sess.Register()
	decode(s, err, &r)
	if r.Committed {
		t.Fatal("committed before the epoch")
	}
	reg.CommitEpoch()
	s, err = sess.Register()
	decode(s, err, &r)
	pub, err := hex.DecodeString(sess.PubkeyHex())
	if err != nil {
		t.Fatal(err)
	}
	var pubkey [48]byte
	copy(pubkey[:], pub)
	idx, ok := reg.Lookup(pubkey)
	if !ok || !r.Committed || r.Index != idx {
		t.Fatalf("registration %+v, registry index %d (%v)", r, idx, ok)
	}

	var head evmsdk.MobileVerifyHead
	s, err = sess.Latest()
	decode(s, err, &head)
	if head != (evmsdk.MobileVerifyHead{Number: number, Hash: hash.Hex(), Packet: true}) {
		t.Fatalf("latest %+v", head)
	}

	if s, err = sess.Cert(hash.Hex()); err != nil || s != "[]" {
		t.Fatalf("cert before the window: %q %v", s, err)
	}

	if _, err := sess.VerifyBlock(badHash.Hex()); err == nil {
		t.Fatal("a block with the wrong receipts root was verified")
	}
	// Under another chain's rules the same block computes another root.
	other, err := evmsdk.NewMobileVerifySession(ts.URL, key, "eth-sepolia")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.VerifyBlock(hash.Hex()); err == nil {
		t.Fatal("verified under the wrong chain")
	}
	var verified struct {
		BlockNumber  uint64 `json:"blockNumber"`
		BlockHash    string `json:"blockHash"`
		ReceiptsRoot string `json:"receiptsRoot"`
		Index        uint32 `json:"index"`
	}
	s, err = sess.VerifyBlock(head.Hash)
	decode(s, err, &verified)
	if verified.BlockNumber != number || verified.BlockHash != hash.Hex() || verified.ReceiptsRoot != root.Hex() || verified.Index != idx {
		t.Fatalf("verified %+v", verified)
	}

	deadline := time.Now().Add(2 * time.Second)
	for store.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.Len() != 1 {
		t.Fatalf("cert blocks = %d, want only the verified one", store.Len())
	}
	var certs []struct {
		BlockNumber  uint64 `json:"blockNumber"`
		ReceiptsRoot string `json:"receiptsRoot"`
		Signers      int    `json:"signers"`
	}
	s, err = sess.Cert(hash.Hex())
	decode(s, err, &certs)
	if len(certs) != 1 || certs[0].Signers != 1 || certs[0].ReceiptsRoot != root.Hex() {
		t.Fatalf("certs %s", s)
	}

	sess.Close()
	if _, err := sess.Health(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("call after Close: %v", err)
	}
}
