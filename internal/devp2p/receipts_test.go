package devp2p

import (
	"bytes"
	"io"
	"testing"

	gethcommon "github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
)

// receiptPairs returns the same receipts as N42 and geth values: a legacy
// pre-Byzantium receipt, a failed legacy one, and a successful typed one
// with logs.
func receiptPairs() ([]*n42block.Receipt, []*gethtypes.Receipt) {
	root := bytes.Repeat([]byte{0x5a}, 32)
	addr := types.Address{0xaa}
	topic := types.Hash{0x0b}
	n42 := []*n42block.Receipt{
		{Type: 0, PostState: root, CumulativeGasUsed: 21000},
		{Type: 0, Status: n42block.ReceiptStatusFailed, CumulativeGasUsed: 42000},
		{Type: 2, Status: n42block.ReceiptStatusSuccessful, CumulativeGasUsed: 90000, Logs: []*n42block.Log{
			{Address: addr, Topics: []types.Hash{topic}, Data: []byte{1, 2, 3}},
			{Address: addr},
		}},
	}
	geth := []*gethtypes.Receipt{
		{Type: 0, PostState: root, CumulativeGasUsed: 21000},
		{Type: 0, Status: gethtypes.ReceiptStatusFailed, CumulativeGasUsed: 42000},
		{Type: 2, Status: gethtypes.ReceiptStatusSuccessful, CumulativeGasUsed: 90000, Logs: []*gethtypes.Log{
			{Address: gethcommon.Address(addr), Topics: []gethcommon.Hash{gethcommon.Hash(topic)}, Data: []byte{1, 2, 3}},
			{Address: gethcommon.Address(addr)},
		}},
	}
	for _, r := range geth {
		r.Bloom = gethtypes.CreateBloom(r)
	}
	return n42, geth
}

// The eth/68 form is the consensus encoding geth produces; the eth/69 form
// is geth's network receipt: [type, post-state-or-status, gas, logs].
func TestReceiptWireEncodingsMatchGeth(t *testing.T) {
	n42, geth := receiptPairs()
	for i := range n42 {
		got68, err := encodeReceipt68(n42[i])
		if err != nil {
			t.Fatalf("encodeReceipt68 %d: %v", i, err)
		}
		consensus, err := geth[i].MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		want68 := rlp.RawValue(consensus)
		if geth[i].Type != 0 {
			if want68, err = rlp.EncodeToBytes(consensus); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(got68, want68) {
			t.Fatalf("receipt %d eth/68:\n got %x\nwant %x", i, got68, want68)
		}

		got69, err := encodeReceipt69(n42[i])
		if err != nil {
			t.Fatalf("encodeReceipt69 %d: %v", i, err)
		}
		pss := geth[i].PostState
		if pss == nil {
			if geth[i].Status == gethtypes.ReceiptStatusSuccessful {
				pss = []byte{1}
			} else {
				pss = []byte{}
			}
		}
		want69, err := rlp.EncodeToBytes(&struct {
			TxType            byte
			PostStateOrStatus []byte
			GasUsed           uint64
			Logs              []*gethtypes.Log
		}{geth[i].Type, pss, geth[i].CumulativeGasUsed, geth[i].Logs})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got69, want69) {
			t.Fatalf("receipt %d eth/69:\n got %x\nwant %x", i, got69, want69)
		}
	}
}

type receiptTestProvider struct {
	BlockProvider
	receipts map[types.Hash]n42block.Receipts
}

func (p *receiptTestProvider) GetReceiptsByHash(hash types.Hash) (n42block.Receipts, error) {
	return p.receipts[hash], nil
}

// GetReceipts is answered in the peer's version, in request order, up to
// the first block whose receipts are not held.
func TestHandleGetReceipts(t *testing.T) {
	n42, _ := receiptPairs()
	known, empty, missing, after := types.Hash{1}, types.Hash{2}, types.Hash{3}, types.Hash{4}
	h := &EthHandler{provider: &receiptTestProvider{receipts: map[types.Hash]n42block.Receipts{
		known: n42,
		empty: {},
		after: n42,
	}}}
	for _, version := range []uint{68, 69} {
		req, err := rlp.EncodeToBytes(&getReceiptsPacket{RequestID: 7, Hashes: []types.Hash{known, empty, missing, after}})
		if err != nil {
			t.Fatal(err)
		}
		rw := newCaptureRW()
		if err := h.handleMessage(dispatchTestPeer(), rw, rawMsg(eth69.GetReceiptsMsg, req), version); err != nil {
			t.Fatalf("eth/%d: %v", version, err)
		}
		code, payload := rw.last(t)
		if code != eth69.ReceiptsMsg {
			t.Fatalf("eth/%d: reply code %#x", version, code)
		}
		var resp blockReceiptsPacket
		if err := rlp.DecodeBytes(payload, &resp); err != nil {
			t.Fatalf("eth/%d: decode reply: %v", version, err)
		}
		if resp.RequestID != 7 || len(resp.Receipts) != 2 || len(resp.Receipts[0]) != len(n42) || len(resp.Receipts[1]) != 0 {
			t.Fatalf("eth/%d: reply id %d, %d blocks", version, resp.RequestID, len(resp.Receipts))
		}
		encode := encodeReceipt68
		if version >= 69 {
			encode = encodeReceipt69
		}
		for i, r := range n42 {
			want, _ := encode(r)
			if !bytes.Equal(resp.Receipts[0][i], want) {
				t.Fatalf("eth/%d receipt %d: got %x want %x", version, i, resp.Receipts[0][i], want)
			}
		}
	}
}

// captureRW records the messages written to it.
type captureRW struct {
	codes    []uint64
	payloads [][]byte
}

func newCaptureRW() *captureRW { return &captureRW{} }

func (c *captureRW) ReadMsg() (gethp2p.Msg, error) { return gethp2p.Msg{}, io.EOF }

func (c *captureRW) WriteMsg(msg gethp2p.Msg) error {
	payload, err := io.ReadAll(msg.Payload)
	if err != nil {
		return err
	}
	c.codes = append(c.codes, msg.Code)
	c.payloads = append(c.payloads, payload)
	return nil
}

func (c *captureRW) last(t *testing.T) (uint64, []byte) {
	t.Helper()
	if len(c.codes) == 0 {
		t.Fatal("no message written")
	}
	return c.codes[len(c.codes)-1], c.payloads[len(c.payloads)-1]
}

func rawMsg(code uint64, payload []byte) gethp2p.Msg {
	return gethp2p.Msg{Code: code, Size: uint32(len(payload)), Payload: bytes.NewReader(payload)}
}
