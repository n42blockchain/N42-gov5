package coldresolve

import (
	"context"

	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// freezerSetter is the subset of *freezer.Freezer used to install a cold
// resolver on a named table (so this stays decoupled + testable).
type freezerSetter interface {
	SetColdResolver(table string, r freezer.ColdResolver) bool
}

// FreezerInstallService installs a cold resolver on a freezer table at Start —
// after the freezer is open. Implements ethel.Service. Used to wire the receipts
// per-block cold-read path (the freezer maps blockNum→fileNum via its own .cidx;
// the resolver fetches the trimmed file by name).
type FreezerInstallService struct {
	fz    freezerSetter
	table string
	r     freezer.ColdResolver
}

// NewFreezerInstallService builds the install service.
func NewFreezerInstallService(fz freezerSetter, table string, r freezer.ColdResolver) *FreezerInstallService {
	return &FreezerInstallService{fz: fz, table: table, r: r}
}

func (s *FreezerInstallService) Name() string { return "cold-resolver-" + s.table }

func (s *FreezerInstallService) Start(context.Context) error {
	if s.fz == nil || s.r == nil {
		return nil
	}
	if s.fz.SetColdResolver(s.table, s.r) {
		log.Info("eth-el: cold resolver installed on freezer table", "table", s.table)
	} else {
		log.Warn("eth-el: freezer table not open; cold resolver not installed", "table", s.table)
	}
	return nil
}

func (s *FreezerInstallService) Stop() error { return nil }
