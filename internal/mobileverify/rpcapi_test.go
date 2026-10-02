package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

type rpcapiTestSubmitter struct {
	open int
}

func (s *rpcapiTestSubmitter) Submit(r *Receipt) (MobileIndex, error) { return 0, nil }
func (s *rpcapiTestSubmitter) OpenWindows() int                       { return s.open }

func TestNewAPIWiresFields(t *testing.T) {
	reg := NewRegistry()
	certs := NewCertStore(10)
	wins := &rpcapiTestSubmitter{open: 3}
	alarms := NewAlarmBuffer(10)

	api := NewAPI(reg, certs, wins, alarms)
	if api.reg != reg || api.certs != certs || api.wins != wins || api.alarms != alarms {
		t.Fatalf("NewAPI() did not wire all fields: %+v", api)
	}
}

func TestGetAnchorsWithoutReaderReturnsEmpty(t *testing.T) {
	api := NewAPI(NewRegistry(), NewCertStore(10), &rpcapiTestSubmitter{}, NewAlarmBuffer(10))
	got := api.GetAnchors(5)
	if got == nil || len(got) != 0 {
		t.Fatalf("GetAnchors() = %v, want empty slice", got)
	}
}

func TestSetAnchorReaderAndGetAnchorsClampsN(t *testing.T) {
	api := NewAPI(NewRegistry(), NewCertStore(10), &rpcapiTestSubmitter{}, NewAlarmBuffer(10))
	var gotN int
	api.SetAnchorReader(func(n int) []AnchorView {
		gotN = n
		return []AnchorView{{Epoch: 1}}
	})

	got := api.GetAnchors(5)
	if gotN != 5 || len(got) != 1 {
		t.Fatalf("GetAnchors(5) = (n=%d, %v), want (5, one anchor)", gotN, got)
	}

	// n <= 0 or n > 512 clamps to 100.
	api.GetAnchors(0)
	if gotN != 100 {
		t.Fatalf("GetAnchors(0) passed n=%d to reader, want clamped to 100", gotN)
	}
	api.GetAnchors(1000)
	if gotN != 100 {
		t.Fatalf("GetAnchors(1000) passed n=%d to reader, want clamped to 100", gotN)
	}
}

func TestGetCertificatesEmpty(t *testing.T) {
	api := NewAPI(NewRegistry(), NewCertStore(10), &rpcapiTestSubmitter{}, NewAlarmBuffer(10))
	got := api.GetCertificates(types.HexToHash("0xdead"))
	if len(got) != 0 {
		t.Fatalf("GetCertificates() = %v, want empty for an unknown block", got)
	}
}

func TestStatusReportsGauges(t *testing.T) {
	wins := &rpcapiTestSubmitter{open: 7}
	api := NewAPI(NewRegistry(), NewCertStore(10), wins, NewAlarmBuffer(10))
	status := api.Status()
	if status["openWindows"] != 7 {
		t.Fatalf("Status()[\"openWindows\"] = %v, want 7", status["openWindows"])
	}
	if status["registry"] != 0 || status["pending"] != 0 || status["certBlocks"] != 0 {
		t.Fatalf("Status() = %+v, want zero gauges for a fresh registry", status)
	}
}

func TestAlarmsWithoutBufferReturnsEmpty(t *testing.T) {
	api := &API{}
	got := api.Alarms()
	if got == nil || len(got) != 0 {
		t.Fatalf("Alarms() = %v, want empty slice when alarms is nil", got)
	}
}

func TestAlarmsReturnsRecent(t *testing.T) {
	alarms := NewAlarmBuffer(10)
	alarms.Record(DivergenceAlarm{BlockNumber: 1})
	api := NewAPI(NewRegistry(), NewCertStore(10), &rpcapiTestSubmitter{}, alarms)
	got := api.Alarms()
	if len(got) != 1 || got[0].BlockNumber != 1 {
		t.Fatalf("Alarms() = %+v, want one alarm for block 1", got)
	}
}
