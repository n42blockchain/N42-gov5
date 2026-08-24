package ethel

import (
	"fmt"
	"testing"

	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func makeTestBlocks() []*DecodedBlock {
	to1 := types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	to2 := types.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	authAddr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	return []*DecodedBlock{
		{
			// Block with legacy + DynamicFee txs.
			Txs: []*transaction.Transaction{
				transaction.NewTx(&transaction.LegacyTx{
					Nonce:    5,
					GasPrice: uint256.NewInt(20_000_000_000),
					Gas:      21000,
					To:       &to1,
					Value:    uint256.NewInt(1_000_000_000_000_000_000),
					Data:     []byte{0xa9, 0x05, 0x9c, 0xbb, 0x00, 0x00},
					V:        uint256.NewInt(28), // pre-EIP-155
					R:        uint256.NewInt(0).SetBytes(types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890").Bytes()),
					S:        uint256.NewInt(0).SetBytes(types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef").Bytes()),
				}),
				transaction.NewTx(&transaction.DynamicFeeTx{
					ChainID:   uint256.NewInt(1),
					Nonce:     10,
					GasTipCap: uint256.NewInt(1_500_000_000),
					GasFeeCap: uint256.NewInt(30_000_000_000),
					Gas:       100000,
					To:        &to2,
					Value:     uint256.NewInt(0),
					Data:      []byte{0x09, 0x5e, 0xa7, 0xb3},
					V:         uint256.NewInt(1),
					R:         uint256.NewInt(0).SetBytes(types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111").Bytes()),
					S:         uint256.NewInt(0).SetBytes(types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222").Bytes()),
				}),
			},
		},
		{
			// Block with SetCode tx (EIP-7702).
			Txs: []*transaction.Transaction{
				transaction.NewTx(&transaction.SetCodeTx{
					ChainID:   uint256.NewInt(1),
					Nonce:     42,
					GasTipCap: uint256.NewInt(2_000_000_000),
					GasFeeCap: uint256.NewInt(50_000_000_000),
					Gas:       150000,
					To:        &to1,
					Value:     uint256.NewInt(0),
					Data:      []byte{0xde, 0xad, 0xbe, 0xef},
					AccessList: transaction.AccessList{
						{Address: to2, StorageKeys: []types.Hash{
							types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
						}},
					},
					AuthList: transaction.AuthorizationList{
						{
							ChainID: *uint256.NewInt(1),
							Address: authAddr,
							Nonce:   7,
							V:       uint256.NewInt(1),
							R:       uint256.NewInt(0).SetBytes(types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Bytes()),
							S:       uint256.NewInt(0).SetBytes(types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb").Bytes()),
						},
						{
							ChainID: *uint256.NewInt(1),
							Address: types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"),
							Nonce:   0,
							V:       uint256.NewInt(0),
							R:       uint256.NewInt(0).SetBytes(types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc").Bytes()),
							S:       uint256.NewInt(0).SetBytes(types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd").Bytes()),
						},
					},
					V: uint256.NewInt(0),
					R: uint256.NewInt(0).SetBytes(types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333").Bytes()),
					S: uint256.NewInt(0).SetBytes(types.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444").Bytes()),
				}),
			},
		},
		{
			// Empty block with withdrawals.
			Txs: nil,
			Withdrawals: []*Withdrawal{
				{Index: 100, Validator: 5000, Address: to1, Amount: 32_000_000_000},
				{Index: 101, Validator: 5001, Address: to2, Amount: 16_000_000_000},
			},
		},
		{
			// Block with Blob tx.
			Txs: []*transaction.Transaction{
				transaction.NewTx(&transaction.BlobTx{
					ChainID:    uint256.NewInt(1),
					Nonce:      99,
					GasTipCap:  uint256.NewInt(1_000_000_000),
					GasFeeCap:  uint256.NewInt(25_000_000_000),
					Gas:        21000,
					To:         to1,
					Value:      uint256.NewInt(0),
					Data:       nil,
					BlobFeeCap: uint256.NewInt(100_000_000),
					BlobHashes: []types.Hash{
						types.HexToHash("0x01abcdef1234567890abcdef1234567890abcdef1234567890abcdef12345678"),
					},
					V: uint256.NewInt(1),
					R: uint256.NewInt(0).SetBytes(types.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555").Bytes()),
					S: uint256.NewInt(0).SetBytes(types.HexToHash("0x6666666666666666666666666666666666666666666666666666666666666666").Bytes()),
				}),
			},
		},
	}
}

func TestBodySegmentRoundtrip(t *testing.T) {
	blocks := makeTestBlocks()

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	compressed := encodeBodySegment(blocks, 1, enc)

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}

	decoded, err := decodeBodySegment(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(decoded) != len(blocks) {
		t.Fatalf("block count: got %d, want %d", len(decoded), len(blocks))
	}

	for bi, orig := range blocks {
		got := decoded[bi]

		// Check tx count.
		origTxCount := len(orig.Txs)
		if len(got.Txs) != origTxCount {
			t.Errorf("block %d: tx count %d != %d", bi, len(got.Txs), origTxCount)
			continue
		}

		// Check each tx field.
		for ti := 0; ti < origTxCount; ti++ {
			origTx := orig.Txs[ti]
			gotTx := got.Txs[ti]

			if gotTx == nil {
				t.Errorf("block %d tx %d: nil", bi, ti)
				continue
			}

			// Type.
			if gotTx.Type() != origTx.Type() {
				t.Errorf("block %d tx %d: type %d != %d", bi, ti, gotTx.Type(), origTx.Type())
			}
			// Nonce.
			if gotTx.Nonce() != origTx.Nonce() {
				t.Errorf("block %d tx %d: nonce %d != %d", bi, ti, gotTx.Nonce(), origTx.Nonce())
			}
			// Gas.
			if gotTx.Gas() != origTx.Gas() {
				t.Errorf("block %d tx %d: gas %d != %d", bi, ti, gotTx.Gas(), origTx.Gas())
			}
			// To.
			origTo := origTx.To()
			gotTo := gotTx.To()
			if (origTo == nil) != (gotTo == nil) {
				t.Errorf("block %d tx %d: To nil mismatch", bi, ti)
			} else if origTo != nil && *origTo != *gotTo {
				t.Errorf("block %d tx %d: To %x != %x", bi, ti, gotTo, origTo)
			}
			// Value.
			if gotTx.Value().Cmp(origTx.Value()) != 0 {
				t.Errorf("block %d tx %d: value mismatch", bi, ti)
			}
			// GasFeeCap.
			if gotTx.GasFeeCap().Cmp(origTx.GasFeeCap()) != 0 {
				t.Errorf("block %d tx %d: gasFeeCap mismatch: got %s want %s",
					bi, ti, gotTx.GasFeeCap(), origTx.GasFeeCap())
			}
			// GasTipCap (type >= 2).
			if origTx.Type() >= transaction.DynamicFeeTxType {
				if gotTx.GasTipCap().Cmp(origTx.GasTipCap()) != 0 {
					t.Errorf("block %d tx %d: gasTipCap mismatch", bi, ti)
				}
			}
			// Data.
			origData := origTx.Data()
			gotData := gotTx.Data()
			if len(origData) != len(gotData) {
				t.Errorf("block %d tx %d: data len %d != %d", bi, ti, len(gotData), len(origData))
			}
			// V, R, S.
			origV, origR, origS := origTx.RawSignatureValues()
			gotV, gotR, gotS := gotTx.RawSignatureValues()
			if origR.Cmp(gotR) != 0 {
				t.Errorf("block %d tx %d: R mismatch", bi, ti)
			}
			if origS.Cmp(gotS) != 0 {
				t.Errorf("block %d tx %d: S mismatch", bi, ti)
			}
			if origV.Cmp(gotV) != 0 {
				t.Errorf("block %d tx %d: V mismatch: got %s want %s", bi, ti, gotV, origV)
			}

			// SetCode: check AuthList.
			if origTx.Type() == transaction.SetCodeTxType {
				origAL := origTx.AuthList()
				gotAL := gotTx.AuthList()
				if len(origAL) != len(gotAL) {
					t.Errorf("block %d tx %d: authList len %d != %d", bi, ti, len(gotAL), len(origAL))
					continue
				}
				for ai, origAuth := range origAL {
					gotAuth := gotAL[ai]
					if origAuth.ChainID.Cmp(&gotAuth.ChainID) != 0 {
						t.Errorf("block %d tx %d auth %d: chainID mismatch", bi, ti, ai)
					}
					if origAuth.Address != gotAuth.Address {
						t.Errorf("block %d tx %d auth %d: address mismatch", bi, ti, ai)
					}
					if origAuth.Nonce != gotAuth.Nonce {
						t.Errorf("block %d tx %d auth %d: nonce %d != %d", bi, ti, ai, gotAuth.Nonce, origAuth.Nonce)
					}
					if origAuth.V.Cmp(gotAuth.V) != 0 {
						t.Errorf("block %d tx %d auth %d: V mismatch", bi, ti, ai)
					}
					if origAuth.R.Cmp(gotAuth.R) != 0 {
						t.Errorf("block %d tx %d auth %d: R mismatch", bi, ti, ai)
					}
					if origAuth.S.Cmp(gotAuth.S) != 0 {
						t.Errorf("block %d tx %d auth %d: S mismatch", bi, ti, ai)
					}
				}
			}

			// Blob: check BlobHashes + BlobFeeCap.
			if origTx.Type() == transaction.BlobTxType {
				origBH := origTx.BlobHashes()
				gotBH := gotTx.BlobHashes()
				if len(origBH) != len(gotBH) {
					t.Errorf("block %d tx %d: blobHashes len %d != %d", bi, ti, len(gotBH), len(origBH))
				} else {
					for hi, h := range origBH {
						if h != gotBH[hi] {
							t.Errorf("block %d tx %d: blobHash %d mismatch", bi, ti, hi)
						}
					}
				}
				if origTx.BlobFeeCap().Cmp(gotTx.BlobFeeCap()) != 0 {
					t.Errorf("block %d tx %d: blobFeeCap mismatch", bi, ti)
				}
			}

			// AccessList.
			origAcl := origTx.AccessList()
			gotAcl := gotTx.AccessList()
			if len(origAcl) != len(gotAcl) {
				t.Errorf("block %d tx %d: accessList len %d != %d", bi, ti, len(gotAcl), len(origAcl))
			}
		}

		// Check withdrawals.
		if len(orig.Withdrawals) != len(got.Withdrawals) {
			t.Errorf("block %d: withdrawal count %d != %d", bi, len(got.Withdrawals), len(orig.Withdrawals))
			continue
		}
		for wi, origW := range orig.Withdrawals {
			gotW := got.Withdrawals[wi]
			if origW.Index != gotW.Index || origW.Validator != gotW.Validator ||
				origW.Address != gotW.Address || origW.Amount != gotW.Amount {
				t.Errorf("block %d withdrawal %d: mismatch", bi, wi)
			}
		}
	}

	t.Logf("Roundtrip OK: %d blocks, compressed %d bytes", len(blocks), len(compressed))
}

// TestSetCodeTxRoundtrip does a deep field-by-field verification of SetCode (type 4)
// encoding and decoding, including AuthList signatures.
func TestSetCodeTxRoundtrip(t *testing.T) {
	to := types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	authAddr1 := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	authAddr2 := types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	storageKey := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	origTx := transaction.NewTx(&transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     42,
		GasTipCap: uint256.NewInt(2_000_000_000),
		GasFeeCap: uint256.NewInt(50_000_000_000),
		Gas:       150000,
		To:        &to,
		Value:     uint256.NewInt(12345),
		Data:      []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03},
		AccessList: transaction.AccessList{
			{Address: to, StorageKeys: []types.Hash{storageKey}},
		},
		AuthList: transaction.AuthorizationList{
			{
				ChainID: *uint256.NewInt(1),
				Address: authAddr1,
				Nonce:   7,
				V:       uint256.NewInt(1),
				R:       uint256.NewInt(0).SetBytes(types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Bytes()),
				S:       uint256.NewInt(0).SetBytes(types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb").Bytes()),
			},
			{
				ChainID: *uint256.NewInt(42161), // Arbitrum chainID
				Address: authAddr2,
				Nonce:   0,
				V:       uint256.NewInt(0),
				R:       uint256.NewInt(0).SetBytes(types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc").Bytes()),
				S:       uint256.NewInt(0).SetBytes(types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd").Bytes()),
			},
		},
		V: uint256.NewInt(0),
		R: uint256.NewInt(0).SetBytes(types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333").Bytes()),
		S: uint256.NewInt(0).SetBytes(types.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444").Bytes()),
	})

	blocks := []*DecodedBlock{{Txs: []*transaction.Transaction{origTx}}}

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	compressed := encodeBodySegment(blocks, 1, enc)

	dec, _ := zstd.NewReader(nil)
	defer dec.Close()
	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	decoded, err := decodeBodySegment(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(decoded) != 1 || len(decoded[0].Txs) != 1 {
		t.Fatalf("expected 1 block with 1 tx")
	}
	gotTx := decoded[0].Txs[0]

	// ---- Basic fields ----
	assert := func(name string, got, want interface{}) {
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}

	assert("Type", gotTx.Type(), transaction.SetCodeTxType)
	assert("Nonce", gotTx.Nonce(), uint64(42))
	assert("Gas", gotTx.Gas(), uint64(150000))
	assert("To", gotTx.To(), &to)
	assert("Value", gotTx.Value().Uint64(), uint64(12345))
	assert("GasTipCap", gotTx.GasTipCap().Uint64(), uint64(2_000_000_000))
	assert("GasFeeCap", gotTx.GasFeeCap().Uint64(), uint64(50_000_000_000))
	assert("Data", fmt.Sprintf("%x", gotTx.Data()), fmt.Sprintf("%x", []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03}))

	// ---- Signature ----
	gotV, gotR, gotS := gotTx.RawSignatureValues()
	origV, origR, origS := origTx.RawSignatureValues()
	assert("V", gotV.String(), origV.String())
	assert("R", gotR.Hex(), origR.Hex())
	assert("S", gotS.Hex(), origS.Hex())

	// ---- AccessList ----
	assert("AccessList.len", len(gotTx.AccessList()), 1)
	assert("AccessList[0].Addr", gotTx.AccessList()[0].Address, to)
	assert("AccessList[0].Keys", len(gotTx.AccessList()[0].StorageKeys), 1)
	assert("AccessList[0].Keys[0]", gotTx.AccessList()[0].StorageKeys[0], storageKey)

	// ---- AuthList (the critical part) ----
	gotAL := gotTx.AuthList()
	origAL := origTx.AuthList()
	if len(gotAL) != len(origAL) {
		t.Fatalf("AuthList len: got %d, want %d", len(gotAL), len(origAL))
	}

	for i, origAuth := range origAL {
		gotAuth := gotAL[i]
		prefix := fmt.Sprintf("Auth[%d]", i)

		assert(prefix+".ChainID", gotAuth.ChainID.String(), origAuth.ChainID.String())
		assert(prefix+".Address", gotAuth.Address.Hex(), origAuth.Address.Hex())
		assert(prefix+".Nonce", gotAuth.Nonce, origAuth.Nonce)
		assert(prefix+".V", gotAuth.V.String(), origAuth.V.String())
		assert(prefix+".R", gotAuth.R.Hex(), origAuth.R.Hex())
		assert(prefix+".S", gotAuth.S.Hex(), origAuth.S.Hex())

		t.Logf("%s: ChainID=%s Addr=%s Nonce=%d V=%s R=%s..%s S=%s..%s ✓",
			prefix, gotAuth.ChainID.String(), gotAuth.Address.Hex()[:10],
			gotAuth.Nonce, gotAuth.V.String(),
			gotAuth.R.Hex()[:10], gotAuth.R.Hex()[len(gotAuth.R.Hex())-4:],
			gotAuth.S.Hex()[:10], gotAuth.S.Hex()[len(gotAuth.S.Hex())-4:])
	}

	t.Logf("SetCode tx roundtrip: all %d fields + %d auth entries verified, compressed=%d bytes",
		10, len(origAL), len(compressed))
}

func TestBodySegmentEmptyBlocks(t *testing.T) {
	blocks := []*DecodedBlock{
		{Txs: nil},
		{Txs: []*transaction.Transaction{}},
		{Txs: nil, Withdrawals: []*Withdrawal{}},
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	compressed := encodeBodySegment(blocks, 1, enc)

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}

	decoded, err := decodeBodySegment(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(decoded) != 3 {
		t.Fatalf("block count: got %d, want 3", len(decoded))
	}
	for i, b := range decoded {
		if len(b.Txs) != 0 {
			t.Errorf("block %d: expected 0 txs, got %d", i, len(b.Txs))
		}
	}
}

// TestBodySegmentUnclesRoundtrip locks in the fix for body_compact
// silently dropping every uncle: detectBodyFlags() keys postMerge off
// len(UncleRLP) > 0, so if the encoder feeds in nil UncleRLP for what
// is actually a pre-merge block, the segment is flagged post-merge and
// the decoder skips the uncle section. That kills uncle inclusion
// rewards (1/32 of block reward per uncle) plus uncle-miner rewards
// (5..35/32 ETH each), making every miner balance off across the chain
// and breaking later txs that send accumulated mining rewards.
func TestBodySegmentUnclesRoundtrip(t *testing.T) {
	// Two synthetic uncle RLP payloads. The encoder/decoder treats them
	// as opaque bytes (only the header decoder cares about RLP shape),
	// so any distinguishable byte sequences exercise the path.
	uncle0 := []byte{0xf8, 0x42, 0x01, 0x02, 0x03, 0x04}
	uncle1 := []byte{0xf8, 0x55, 0xaa, 0xbb, 0xcc}
	uncle2 := []byte{0xf8, 0x10, 0xde, 0xad, 0xbe, 0xef}
	to := types.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	blocks := []*DecodedBlock{
		// Block 0: pre-merge, has 2 uncles, 1 legacy tx.
		{
			Txs: []*transaction.Transaction{
				transaction.NewTx(&transaction.LegacyTx{
					Nonce:    1,
					GasPrice: uint256.NewInt(50_000_000_000),
					Gas:      21000,
					To:       &to,
					Value:    uint256.NewInt(1_000_000_000_000_000_000),
					V:        uint256.NewInt(28),
					R:        uint256.NewInt(11),
					S:        uint256.NewInt(22),
				}),
			},
			UncleRLP: [][]byte{uncle0, uncle1},
		},
		// Block 1: pre-merge, no txs but 1 uncle (the early-mainnet
		// shape this bug actually hit — empty bodies that still earn
		// inclusion bonus when they include uncles).
		{
			UncleRLP: [][]byte{uncle2},
		},
		// Block 2: pre-merge, no uncles, no txs (empty frontier block).
		{},
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	compressed := encodeBodySegment(blocks, 1, enc)

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}

	decoded, err := decodeBodySegment(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(decoded) != 3 {
		t.Fatalf("block count: got %d, want 3", len(decoded))
	}

	wantUncles := [][][]byte{
		{uncle0, uncle1},
		{uncle2},
		nil,
	}
	for bi, want := range wantUncles {
		got := decoded[bi].UncleRLP
		if len(got) != len(want) {
			t.Errorf("block %d uncle count: got %d want %d", bi, len(got), len(want))
			continue
		}
		for ui, w := range want {
			if string(got[ui]) != string(w) {
				t.Errorf("block %d uncle %d: got %x want %x", bi, ui, got[ui], w)
			}
		}
	}
}

// TestSetCodeAuthVNonParity pins the authorization V values the original
// encoder could not represent. It wrote a single byte, set to 1 only when
// V == 1, so everything else — including the 27 / 28 that mainnet
// authorizations actually carry — was folded to 0.
//
// Nothing caught it because the loss is invisible at the layer most checks look
// at: the transaction hash is unchanged, so a body diff comparing tx hashes
// reports the bodies as identical. It only surfaces during execution, where a
// wrong V recovers a different authority, leaves the delegated account's nonce
// unchanged, and shifts the block's gas — block 24231367 replayed as failed
// from a columnar source while the same block from geth ancient replayed clean.
//
// TestSetCodeTxRoundtrip covers V = 0 and V = 1 only, i.e. exactly the two
// values for which the broken encoding happened to be correct.
func TestSetCodeAuthVNonParity(t *testing.T) {
	to := types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	authAddr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// 27 and 28 are what mainnet carries; 0 and 1 are the y_parity form the
	// encoder assumed was the only one.
	for _, v := range []uint64{0, 1, 27, 28} {
		t.Run(fmt.Sprintf("V=%d", v), func(t *testing.T) {
			origTx := transaction.NewTx(&transaction.SetCodeTx{
				ChainID:   uint256.NewInt(1),
				Nonce:     42,
				GasTipCap: uint256.NewInt(2_000_000_000),
				GasFeeCap: uint256.NewInt(50_000_000_000),
				Gas:       150000,
				To:        &to,
				Value:     uint256.NewInt(1),
				AuthList: transaction.AuthorizationList{
					{
						ChainID: *uint256.NewInt(1),
						Address: authAddr,
						Nonce:   7,
						V:       uint256.NewInt(v),
						R:       uint256.NewInt(0).SetBytes(types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Bytes()),
						S:       uint256.NewInt(0).SetBytes(types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb").Bytes()),
					},
				},
				V: uint256.NewInt(0),
				R: uint256.NewInt(0).SetBytes(types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333").Bytes()),
				S: uint256.NewInt(0).SetBytes(types.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444").Bytes()),
			})

			enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
			defer enc.Close()
			compressed := encodeBodySegment([]*DecodedBlock{{Txs: []*transaction.Transaction{origTx}}}, 1, enc)

			dec, _ := zstd.NewReader(nil)
			defer dec.Close()
			raw, err := dec.DecodeAll(compressed, nil)
			if err != nil {
				t.Fatalf("decompress: %v", err)
			}
			decoded, err := decodeBodySegment(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(decoded) != 1 || len(decoded[0].Txs) != 1 {
				t.Fatalf("decoded %d blocks", len(decoded))
			}
			al := decoded[0].Txs[0].AuthList()
			if len(al) != 1 {
				t.Fatalf("auth list length %d", len(al))
			}
			if al[0].V == nil {
				t.Fatal("auth V decoded as nil")
			}
			if got := al[0].V.Uint64(); got != v {
				t.Fatalf("auth V round-tripped as %d, want %d — a non-parity V was "+
					"flattened, which changes the recovered authority at execution", got, v)
			}
		})
	}
}
