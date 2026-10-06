package mobileverify

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newHTTPServerTestStub() *HTTPServer {
	reg := NewRegistry()
	cache := NewPacketCache(8)
	packets := NewPacketService(cache, nil, "/test")
	store := NewCertStore(8)
	windows := NewWindowManager(reg, fixedLookup(nil), 0, store)
	return NewHTTPServer("127.0.0.1:0", reg, packets, windows, store)
}

func TestHandleReceiptRejectsNonPOST(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleReceipt(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/receipt", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleReceiptRejectsBadJSON(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleReceipt(rec, httptest.NewRequest(http.MethodPost, "/mobileverify/receipt", bytes.NewReader([]byte("{"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleReceiptRejectsBadHexFields(t *testing.T) {
	s := newHTTPServerTestStub()
	body, _ := json.Marshal(receiptRequest{BlockHash: "not-hex"})
	rec := httptest.NewRecorder()
	s.handleReceipt(rec, httptest.NewRequest(http.MethodPost, "/mobileverify/receipt", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleReceiptRejectsUnknownBlock(t *testing.T) {
	s := newHTTPServerTestStub()
	d := newDevice(t)
	registerCommitted(t, s.reg, d.pubkey, d.pop())
	rcpt := d.receipt(h(0x01), 1, h(0x02))
	body, _ := json.Marshal(receiptRequest{
		BlockHash:    rcpt.BlockHash.Hex()[2:],
		BlockNumber:  rcpt.BlockNumber,
		ReceiptsRoot: rcpt.ComputedReceiptsRoot.Hex()[2:],
		Pubkey:       bytesToHex(rcpt.VerifierPubkey[:]),
		Signature:    bytesToHex(rcpt.Signature[:]),
		TimestampMs:  rcpt.TimestampMs,
	})
	rec := httptest.NewRecorder()
	s.handleReceipt(rec, httptest.NewRequest(http.MethodPost, "/mobileverify/receipt", bytes.NewReader(body)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s, want %d", rec.Code, rec.Body.String(), http.StatusNotFound)
	}
}

func TestHandleCertRejectsNonGET(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleCert(rec, httptest.NewRequest(http.MethodPost, "/mobileverify/cert/"+h(0x01).Hex()[2:], nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleCertRejectsBadHash(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleCert(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/cert/not-a-hash", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleCertNotFound(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleCert(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/cert/"+h(0x01).Hex()[2:], nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleCertFound(t *testing.T) {
	s := newHTTPServerTestStub()
	blockHash := h(0x05)
	s.certs.Put([]*MobileAttestationCert{{BlockHash: blockHash, BlockNumber: 5}})
	rec := httptest.NewRecorder()
	s.handleCert(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/cert/"+blockHash.Hex()[2:], nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s, want 200", rec.Code, rec.Body.String())
	}
}

func TestHandleMagnetRejectsNonGET(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleMagnet(rec, httptest.NewRequest(http.MethodPost, "/mobileverify/magnet/"+h(0x01).Hex()[2:], nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleMagnetRejectsBadHash(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleMagnet(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/magnet/zz", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleMagnetNotSeeded(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleMagnet(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/magnet/"+h(0x01).Hex()[2:], nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleAlarmsRejectsNonGET(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleAlarms(rec, httptest.NewRequest(http.MethodPost, "/mobileverify/alarms", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAlarmsWithoutBufferReturnsEmptyList(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleAlarms(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/alarms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []DivergenceAlarm
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("alarms = %v, want empty", got)
	}
}

func TestHandleAlarmsWithBuffer(t *testing.T) {
	s := newHTTPServerTestStub()
	alarms := NewAlarmBuffer(8)
	alarms.Record(DivergenceAlarm{BlockNumber: 3})
	s.SetAlarms(alarms)

	rec := httptest.NewRecorder()
	s.handleAlarms(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/alarms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []DivergenceAlarm
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BlockNumber != 3 {
		t.Fatalf("alarms = %+v, want one alarm for block 3", got)
	}
}

func TestHandleSnapshotRejectsNonGET(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleSnapshot(rec, httptest.NewRequest(http.MethodPost, "/mobileverify/snapshot", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleSnapshotServesExport(t *testing.T) {
	s := newHTTPServerTestStub()
	rec := httptest.NewRecorder()
	s.handleSnapshot(rec, httptest.NewRequest(http.MethodGet, "/mobileverify/snapshot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Mobileverify-Root") == "" {
		t.Fatal("missing X-Mobileverify-Root header")
	}
}

func TestSetRegisterPoWBitsCapsAtMax(t *testing.T) {
	s := newHTTPServerTestStub()
	s.SetRegisterPoWBits(MaxRegisterPoWBits + 100)
	if s.powBits != MaxRegisterPoWBits {
		t.Fatalf("powBits = %d, want capped at %d", s.powBits, MaxRegisterPoWBits)
	}
}

func bytesToHex(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hextable[c>>4]
		out[i*2+1] = hextable[c&0x0f]
	}
	return string(out)
}
