package internal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func speculativeKeyRegressionBlock() *block.Block {
	withdrawals, blobGas, excessBlobGas := types.Hash{0x17}, uint64(2), uint64(3)
	beacon, requests := types.Hash{0x18}, types.Hash{0x19}
	accessList, mobileRoot := types.Hash{0x1a}, types.Hash{0x1b}
	toA, toB := types.Address{0x21}, types.Address{0x22}
	txs := []*transaction.Transaction{
		transaction.NewTransaction(1, types.Address{0x31}, &toA, uint256.NewInt(5), 21000, uint256.NewInt(7), []byte{1}),
		transaction.NewTransaction(2, types.Address{0x32}, &toB, uint256.NewInt(6), 22000, uint256.NewInt(8), []byte{2}),
	}
	header := &block.Header{
		ParentHash: types.Hash{1}, UncleHash: types.Hash{2}, Coinbase: types.Address{3},
		Root: types.Hash{4}, ReceiptHash: types.Hash{5}, Bloom: block.Bloom{0: 6},
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(2), GasLimit: 30_000_000,
		GasUsed: 1, Time: 12, Extra: speculativeKeyExtra(4, []byte{0xa1}),
		MixDigest: types.Hash{7}, Nonce: block.BlockNonce{8}, BaseFee: uint256.NewInt(9),
		WithdrawalsHash: &withdrawals, BlobGasUsed: &blobGas, ExcessBlobGas: &excessBlobGas,
		ParentBeaconRoot: &beacon, RequestsHash: &requests,
		BlockAccessListHash: &accessList, MobileRegistryRoot: &mobileRoot,
	}
	return block.NewBlock(header, txs).(*block.Block)
}

func cloneSpeculativeKeyRegressionBlock(t *testing.T, source *block.Block) *block.Block {
	t.Helper()
	raw, err := source.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var copy block.Block
	if err := copy.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	return &copy
}

// These cases mutate concrete header fields and ordered body contents. Extra
// is intentionally covered separately because its parsed view is bound by the
// speculative lease while its QC/seal payload may change within that view.
func TestSpeculativeExecutionKeyRejectsEveryNonExtraHeaderAndBodyChange(t *testing.T) {
	template := speculativeKeyRegressionBlock()
	key, err := speculativeExecutionKey(template)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		change func(*block.Header)
	}{
		{"parent hash", func(h *block.Header) { h.ParentHash = types.Hash{0x91} }},
		{"uncle hash", func(h *block.Header) { h.UncleHash = types.Hash{0x92} }},
		{"coinbase", func(h *block.Header) { h.Coinbase = types.Address{0x93} }},
		{"state root", func(h *block.Header) { h.Root = types.Hash{0x94} }},
		{"transaction root", func(h *block.Header) { h.TxHash = types.Hash{0x95} }},
		{"receipt root", func(h *block.Header) { h.ReceiptHash = types.Hash{0x96} }},
		{"bloom", func(h *block.Header) { h.Bloom[0] ^= 1 }},
		{"difficulty", func(h *block.Header) { h.Difficulty = uint256.NewInt(1) }},
		{"number", func(h *block.Header) { h.Number = uint256.NewInt(3) }},
		{"gas limit", func(h *block.Header) { h.GasLimit++ }},
		{"gas used", func(h *block.Header) { h.GasUsed++ }},
		{"time", func(h *block.Header) { h.Time++ }},
		{"mix digest", func(h *block.Header) { h.MixDigest = types.Hash{0x97} }},
		{"nonce", func(h *block.Header) { h.Nonce = block.BlockNonce{0x98} }},
		{"base fee", func(h *block.Header) { h.BaseFee = uint256.NewInt(10) }},
		{"withdrawals hash", func(h *block.Header) { value := types.Hash{0x99}; h.WithdrawalsHash = &value }},
		{"blob gas used", func(h *block.Header) { value := uint64(4); h.BlobGasUsed = &value }},
		{"excess blob gas", func(h *block.Header) { value := uint64(5); h.ExcessBlobGas = &value }},
		{"parent beacon root", func(h *block.Header) { value := types.Hash{0x9a}; h.ParentBeaconRoot = &value }},
		{"requests hash", func(h *block.Header) { value := types.Hash{0x9b}; h.RequestsHash = &value }},
		{"block access list hash", func(h *block.Header) { value := types.Hash{0x9c}; h.BlockAccessListHash = &value }},
		{"mobile registry root", func(h *block.Header) { value := types.Hash{0x9d}; h.MobileRegistryRoot = &value }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			changed := cloneSpeculativeKeyRegressionBlock(t, template)
			before, err := changed.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			header := changed.Header().(*block.Header)
			tc.change(header)
			changed.WithSeal(header)
			after, err := changed.Marshal()
			if err != nil || bytes.Equal(after, before) {
				t.Fatalf("header mutation did not change actual block: %v", err)
			}
			got, err := speculativeExecutionKey(changed)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(got, key) {
				t.Fatal("non-Extra execution input change retained speculative key")
			}
		})
	}
	t.Run("ordered transaction body", func(t *testing.T) {
		changed := cloneSpeculativeKeyRegressionBlock(t, template)
		before, err := changed.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		body := changed.Body().(*block.Body)
		body.Txs[0], body.Txs[1] = body.Txs[1], body.Txs[0]
		after, err := changed.Marshal()
		if err != nil || bytes.Equal(after, before) {
			t.Fatalf("body mutation did not change actual block: %v", err)
		}
		got, err := speculativeExecutionKey(changed)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(got, key) {
			t.Fatal("ordered transaction change retained speculative key")
		}
	})
}

func speculativeKeyExtra(view uint64, qcPayload []byte) []byte {
	extra := make([]byte, 12+len(qcPayload))
	copy(extra[:4], "N42H")
	binary.LittleEndian.PutUint64(extra[4:12], view)
	copy(extra[12:], qcPayload)
	return extra
}

func TestSpeculativeExecutionKeyNormalizesQCOnlyWithinParsedView(t *testing.T) {
	template := speculativeKeyRegressionBlock()
	header := template.Header().(*block.Header)
	header.Extra = speculativeKeyExtra(11, []byte{0xa1, 0xb2})
	template.WithSeal(header)
	baseView, err := hotstuff.ExtractViewFromExtra(header.Extra)
	if err != nil || baseView != 11 {
		t.Fatalf("base parsed view = %d, %v", baseView, err)
	}
	key, err := speculativeExecutionKey(template)
	if err != nil {
		t.Fatal(err)
	}
	qcChanged := cloneSpeculativeKeyRegressionBlock(t, template)
	qcHeader := qcChanged.Header().(*block.Header)
	qcHeader.Extra = speculativeKeyExtra(11, []byte{0xc3, 0xd4})
	qcChanged.WithSeal(qcHeader)
	if bytes.Equal(qcChanged.Header().(*block.Header).Extra, template.Header().(*block.Header).Extra) {
		t.Fatal("QC mutation did not change actual block")
	}
	qcView, err := hotstuff.ExtractViewFromExtra(qcChanged.Header().(*block.Header).Extra)
	if err != nil || qcView != baseView {
		t.Fatalf("QC-only mutation changed parsed view to %d: %v", qcView, err)
	}
	qcKey, err := speculativeExecutionKey(qcChanged)
	if err != nil || !bytes.Equal(qcKey, key) {
		t.Fatalf("same-view QC mutation changed EVM key: %x, %v", qcKey, err)
	}

	otherView := cloneSpeculativeKeyRegressionBlock(t, template)
	otherHeader := otherView.Header().(*block.Header)
	otherHeader.Extra = speculativeKeyExtra(12, []byte{0xa1, 0xb2})
	otherView.WithSeal(otherHeader)
	parsed, err := hotstuff.ExtractViewFromExtra(otherView.Header().(*block.Header).Extra)
	if err != nil || parsed == baseView {
		t.Fatalf("view mutation did not change parsed view: %d, %v", parsed, err)
	}
	// The raw key normalizes consensus Extra. Complete must enforce the view
	// binding before it can reuse that equal EVM key.
	chainConfig := &params.ChainConfig{Performance: &params.PerformanceChainConfig{}}
	bc := &BlockChain{chainConfig: chainConfig, engine: hotstuff.New(nil, chainConfig)}
	binding := types.Hash{0xee}
	child := &SpeculativeAppliedChild{chain: bc, state: state.New(nil), header: header, view: baseView, binding: binding, key: key}
	if _, _, _, err := bc.CompleteSpeculativeAppliedChild(context.Background(), child, binding, otherView, false); !errors.Is(err, ErrSpeculativeChildMismatch) {
		t.Fatalf("different parsed view reused speculative child: %v", err)
	}
}

func TestCompleteSpeculativeAppliedChildStillValidatesActualMalformedQC(t *testing.T) {
	chainConfig := &params.ChainConfig{Performance: &params.PerformanceChainConfig{}}
	bc := &BlockChain{chainConfig: chainConfig, engine: hotstuff.New(nil, chainConfig)}
	advisory := speculativeKeyRegressionBlock()
	advisoryHeader := advisory.Header().(*block.Header)
	advisoryHeader.Root = types.Hash{}
	advisoryHeader.ReceiptHash = types.Hash{}
	advisoryHeader.Bloom = block.Bloom{}
	advisoryHeader.GasUsed = 0
	advisoryHeader.Difficulty = uint256.NewInt(0)
	advisoryHeader.Nonce = block.BlockNonce{}
	advisoryHeader.WithdrawalsHash = nil
	advisoryHeader.BlobGasUsed = nil
	advisoryHeader.ExcessBlobGas = nil
	advisoryHeader.RequestsHash = nil
	advisoryHeader.BlockAccessListHash = nil
	advisoryHeader.Extra = speculativeKeyExtra(13, []byte{0xa1})
	advisory.WithSeal(advisoryHeader)
	actual := cloneSpeculativeKeyRegressionBlock(t, advisory)
	actualHeader := actual.Header().(*block.Header)
	actualHeader.Extra = speculativeKeyExtra(13, []byte{0xff}) // malformed QC payload, same view
	actual.WithSeal(actualHeader)
	if bytes.Equal(actual.Header().(*block.Header).Extra, advisory.Header().(*block.Header).Extra) {
		t.Fatal("malformed QC mutation did not change actual block")
	}
	key, err := speculativeExecutionKey(advisory)
	if err != nil {
		t.Fatal(err)
	}
	actualKey, err := speculativeExecutionKey(actual)
	if err != nil || !bytes.Equal(actualKey, key) {
		t.Fatalf("same-view Extra mutation did not reach public validation: key-equal=%v err=%v", bytes.Equal(actualKey, key), err)
	}
	if err := bc.engine.(*hotstuff.HotStuff).ValidateUnsignedCandidateHeader(bc, actual.Header().(*block.Header)); err == nil {
		t.Fatal("public candidate validator accepted malformed actual QC")
	}
	binding := types.Hash{0xef}
	child := &SpeculativeAppliedChild{chain: bc, state: state.New(nil), header: advisoryHeader, view: 13, binding: binding, key: key}
	_, _, _, err = bc.CompleteSpeculativeAppliedChild(context.Background(), child, binding, actual, false)
	if err == nil || errors.Is(err, ErrSpeculativeChildMismatch) {
		t.Fatalf("malformed actual QC bypassed public header validation after key match: %v", err)
	}
}
