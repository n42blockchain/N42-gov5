package temporal

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/kv/order"
)

func TestDomainRangeRejectsUnsupportedModes(t *testing.T) {
	tx := &Tx{}

	if _, err := tx.DomainRange(kv.AccountsDomain, nil, nil, 0, order.Desc, -1); err == nil || !strings.Contains(err.Error(), "descending order") {
		t.Fatalf("DomainRange() error = %v, want descending order error", err)
	}
	if _, err := tx.DomainRange(kv.StorageDomain, nil, nil, 0, order.Asc, -1); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("DomainRange() error = %v, want not implemented error", err)
	}
}

func TestHistoryRangeRejectsUnsupportedModes(t *testing.T) {
	tx := &Tx{}

	if _, err := tx.HistoryRange(kv.AccountsHistory, 0, 1, order.Desc, -1); err == nil || !strings.Contains(err.Error(), "descending order") {
		t.Fatalf("HistoryRange() error = %v, want descending order error", err)
	}
	if _, err := tx.HistoryRange(kv.AccountsHistory, 0, 1, order.Asc, 1); err == nil || !strings.Contains(err.Error(), "explicit limit") {
		t.Fatalf("HistoryRange() error = %v, want explicit limit error", err)
	}
}

func TestDomainGetAsOfReturnsError(t *testing.T) {
	tx := &Tx{}

	if _, _, err := tx.DomainGetAsOf(kv.AccountsDomain, nil, nil, 0); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("DomainGetAsOf() error = %v, want not implemented error", err)
	}
}

func TestNewRequiresHistoryV3(t *testing.T) {
	db := memdb.NewTestDB(t)

	_, err := New(db, nil)
	if err == nil || !strings.Contains(err.Error(), "history.v3") {
		t.Fatalf("New() error = %v, want history.v3 error", err)
	}
}
