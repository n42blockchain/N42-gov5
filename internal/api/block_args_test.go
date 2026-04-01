package api

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestRPCMarshalHeaderAllowsNilNumberAndDifficulty(t *testing.T) {
	header := &block.Header{}

	fields := RPCMarshalHeader(header, nil)
	if fields == nil {
		t.Fatal("RPCMarshalHeader() returned nil")
	}

	number, ok := fields["number"].(*hexutil.Big)
	if !ok || number == nil || number.ToInt().Sign() != 0 {
		t.Fatalf("RPCMarshalHeader() number = %#v", fields["number"])
	}

	difficulty, ok := fields["difficulty"].(*hexutil.Big)
	if !ok || difficulty == nil || difficulty.ToInt().Sign() != 0 {
		t.Fatalf("RPCMarshalHeader() difficulty = %#v", fields["difficulty"])
	}
}

func TestRPCMarshalHeaderPreservesExplicitValues(t *testing.T) {
	header := &block.Header{
		Number:     uint256.NewInt(11),
		Difficulty: uint256.NewInt(22),
	}

	fields := RPCMarshalHeader(header, nil)
	if got := fields["number"].(*hexutil.Big).ToInt().Uint64(); got != 11 {
		t.Fatalf("RPCMarshalHeader() number = %d, want 11", got)
	}
	if got := fields["difficulty"].(*hexutil.Big).ToInt().Uint64(); got != 22 {
		t.Fatalf("RPCMarshalHeader() difficulty = %d, want 22", got)
	}
}

func TestRPCMarshalHeaderUsesEthereumCompatibleHash(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		ParentHash:  typesHashFromByte(0x01),
		Root:        typesHashFromByte(0x02),
		TxHash:      typesHashFromByte(0x03),
		ReceiptHash: typesHashFromByte(0x04),
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(7),
		GasLimit:    30_000_000,
		GasUsed:     0,
		Time:        9,
		BaseFee:     uint256.NewInt(1),
	}

	fields := RPCMarshalHeader(header, nil)
	got, ok := fields["hash"].(avmutil.Hash)
	if !ok {
		t.Fatalf("RPCMarshalHeader() hash type = %T", fields["hash"])
	}
	want := avmutil.Hash(ethCompatibleHeaderHash(header, nil))
	if got != want {
		t.Fatalf("RPCMarshalHeader() hash = %s, want %s", got.Hex(), want.Hex())
	}
}

func TestRPCMarshalHeaderOmitsPostForkFieldsBeforeShanghai(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Number:        uint256.NewInt(0),
		Time:          9,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   ptrToUint64(11),
		ExcessBlobGas: ptrToUint64(22),
	}
	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(10),
		CancunBlock:   big.NewInt(20),
	}

	fields := RPCMarshalHeader(header, cfg)
	for _, key := range []string{"withdrawalsRoot", "withdrawals", "blobGasUsed", "excessBlobGas", "parentBeaconBlockRoot"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("RPCMarshalHeader() unexpectedly included %q before Shanghai/Cancun", key)
		}
	}
}

func TestRPCMarshalHeaderIncludesWithdrawalsAtShanghai(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Number:  uint256.NewInt(10),
		Time:    9,
		BaseFee: uint256.NewInt(1),
	}
	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(10),
		CancunBlock:   big.NewInt(20),
	}

	fields := RPCMarshalHeader(header, cfg)
	got, ok := fields["withdrawalsRoot"].(avmutil.Hash)
	if !ok {
		t.Fatal("RPCMarshalHeader() missing withdrawalsRoot at Shanghai")
	}
	wantRoot := avmutil.Hash(withdrawalsRoot(nil))
	if got != wantRoot {
		t.Fatalf("RPCMarshalHeader() withdrawalsRoot = %s, want %s", got.Hex(), wantRoot.Hex())
	}
	if _, ok := fields["withdrawals"]; !ok {
		t.Fatal("RPCMarshalHeader() missing withdrawals at Shanghai")
	}
	if _, ok := fields["blobGasUsed"]; ok {
		t.Fatal("RPCMarshalHeader() unexpectedly included blobGasUsed before Cancun")
	}
	if _, ok := fields["excessBlobGas"]; ok {
		t.Fatal("RPCMarshalHeader() unexpectedly included excessBlobGas before Cancun")
	}
}

func TestRPCMarshalHeaderIncludesBlobFieldsAtCancun(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Number:        uint256.NewInt(20),
		Time:          9,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   ptrToUint64(11),
		ExcessBlobGas: ptrToUint64(22),
	}
	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(10),
		CancunBlock:   big.NewInt(20),
	}

	fields := RPCMarshalHeader(header, cfg)
	if got, ok := fields["blobGasUsed"].(hexutil.Uint64); !ok || uint64(got) != 11 {
		t.Fatalf("RPCMarshalHeader() blobGasUsed = %#v, want 11", fields["blobGasUsed"])
	}
	if got, ok := fields["excessBlobGas"].(hexutil.Uint64); !ok || uint64(got) != 22 {
		t.Fatalf("RPCMarshalHeader() excessBlobGas = %#v, want 22", fields["excessBlobGas"])
	}
	gotRoot, ok := fields["withdrawalsRoot"].(avmutil.Hash)
	if !ok {
		t.Fatal("RPCMarshalHeader() missing withdrawalsRoot at Cancun")
	}
	wantRoot := avmutil.Hash(withdrawalsRoot(nil))
	if gotRoot != wantRoot {
		t.Fatalf("RPCMarshalHeader() withdrawalsRoot = %s, want %s", gotRoot.Hex(), wantRoot.Hex())
	}
	if gotBeaconRoot, ok := fields["parentBeaconBlockRoot"].(avmutil.Hash); !ok || gotBeaconRoot != (avmutil.Hash{}) {
		t.Fatalf("RPCMarshalHeader() parentBeaconBlockRoot = %#v, want zero hash", fields["parentBeaconBlockRoot"])
	}
	if _, ok := fields["withdrawals"]; !ok {
		t.Fatal("RPCMarshalHeader() missing withdrawals at Cancun")
	}
}

func TestRPCMarshalHeaderIncludesRequestsHashAtPrague(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Number:        uint256.NewInt(0),
		Time:          0,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
	}
	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
	}

	fields := RPCMarshalHeader(header, cfg)
	got, ok := fields["requestsHash"].(avmutil.Hash)
	if !ok {
		t.Fatal("RPCMarshalHeader() missing requestsHash at Prague")
	}
	want := avmutil.Hash(executionRequestsHash(nil))
	if got != want {
		t.Fatalf("RPCMarshalHeader() requestsHash = %s, want %s", got.Hex(), want.Hex())
	}
}

func TestEthCompatibleHeaderHashMatchesHiveGenesisSample(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Root:          types.HexToHash("0x5990f595adc5c6e399fdc1826c33872f57b1118a4149a22a63d9888ed89a0c83"),
		Difficulty:    uint256.MustFromHex("0x30000"),
		Number:        uint256.NewInt(0),
		GasLimit:      0x2fefd8,
		GasUsed:       0,
		Time:          0x1234,
		Extra:         hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000658bdf435d810c91414ec09147daa6db624063790000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
		BaseFee:       uint256.MustFromHex("0x3b9aca00"),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
		Bloom:         block.BytesToBloom(make([]byte, 256)),
		Nonce:         block.EncodeNonce(0),
	}

	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
	}
	got := ethCompatibleHeaderHash(header, cfg)
	want := types.HexToHash("0x923990abfc042bead569332935e739e18a4d89934252b04d8f38d77559c40adf")
	if got != want {
		t.Fatalf("ethCompatibleHeaderHash() = %s, want %s", got, want)
	}
}

func TestEthCompatibleHeaderHashMatchesCancunConsumeEngineGenesisSample(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Root:          types.HexToHash("0xfd7f6c3454dd98ecd77e592d11f9a92e34d4afeaa3eee9ea19f169add83992c4"),
		TxHash:        types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		ReceiptHash:   types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Difficulty:    uint256.NewInt(0),
		Number:        uint256.NewInt(0),
		GasLimit:      0x07270e00,
		GasUsed:       0,
		Time:          0,
		Extra:         hexutil.MustDecode("0x00"),
		BaseFee:       uint256.NewInt(7),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
		Bloom:         block.BytesToBloom(make([]byte, 256)),
		Nonce:         block.EncodeNonce(0),
	}

	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
	}
	got := ethCompatibleHeaderHash(header, cfg)
	want := types.HexToHash("0x00454c827e60cef51ce803c9eb1c485b2c7c0ba4717d9268719f7e77575b758e")
	if got != want {
		t.Fatalf("ethCompatibleHeaderHash() = %s, want %s", got, want)
	}
}

func TestEthCompatibleHeaderHashMatchesConsumeEngineGenesisSample(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Root:          types.HexToHash("0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1"),
		TxHash:        types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		ReceiptHash:   types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Difficulty:    uint256.NewInt(0),
		Number:        uint256.NewInt(0),
		GasLimit:      0x07270e00,
		GasUsed:       0,
		Time:          0,
		Extra:         hexutil.MustDecode("0x00"),
		BaseFee:       uint256.NewInt(7),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
		Bloom:         block.BytesToBloom(make([]byte, 256)),
		Nonce:         block.EncodeNonce(0),
	}

	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
	}
	got := ethCompatibleHeaderHash(header, cfg)
	want := types.HexToHash("0xfcb3144caa6d4d3bbf62ba826c12006f4d1b1c1bb18f2a4a902f2b91a67575c7")
	if got != want {
		t.Fatalf("ethCompatibleHeaderHash() = %s, want %s", got, want)
	}
}

func TestEthCompatibleHeaderHashIncludesRequestsHashAtPrague(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Root:          types.HexToHash("0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1"),
		TxHash:        types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		ReceiptHash:   types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Difficulty:    uint256.NewInt(0),
		Number:        uint256.NewInt(0),
		GasLimit:      0x07270e00,
		GasUsed:       0,
		Time:          0,
		Extra:         hexutil.MustDecode("0x00"),
		BaseFee:       uint256.NewInt(7),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
		Bloom:         block.BytesToBloom(make([]byte, 256)),
		Nonce:         block.EncodeNonce(0),
	}
	cfg := &params.ChainConfig{
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
	}
	requestsHash := executionRequestsHash(nil)
	want := hash.RlpHash(&ethRPCHeader{
		ParentHash:       header.ParentHash,
		UncleHash:        hash.EmptyUncleHash,
		Coinbase:         header.Coinbase,
		Root:             header.Root,
		TxHash:           header.TxHash,
		ReceiptHash:      header.ReceiptHash,
		Bloom:            block.BytesToBloom(header.Bloom.Bytes()),
		Difficulty:       uint256ToBigOrZero(header.Difficulty),
		Number:           uint256ToBigOrZero(header.Number),
		GasLimit:         header.GasLimit,
		GasUsed:          header.GasUsed,
		Time:             header.Time,
		Extra:            header.Extra,
		MixDigest:        header.MixDigest,
		Nonce:            block.EncodeNonce(header.Nonce.Uint64()),
		BaseFee:          header.BaseFee.ToBig(),
		WithdrawalsHash:  ptrToHash(withdrawalsRoot(nil)),
		BlobGasUsed:      header.BlobGasUsed,
		ExcessBlobGas:    header.ExcessBlobGas,
		ParentBeaconRoot: ptrToHash(types.Hash{}),
		RequestsHash:     &requestsHash,
	})
	got := ethCompatibleHeaderHash(header, cfg)
	if got != want {
		t.Fatalf("ethCompatibleHeaderHash() = %s, want %s", got, want)
	}
}

func typesHashFromByte(b byte) types.Hash {
	var h types.Hash
	h[31] = b
	return h
}

func ptrToHash(h types.Hash) *types.Hash {
	return &h
}

func ptrToUint64(v uint64) *uint64 {
	return &v
}
