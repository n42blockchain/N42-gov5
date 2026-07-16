// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"github.com/n42blockchain/N42/common/types"
)

// API is the mobileverify_* JSON-RPC namespace (design §8.6: the mobile
// surface lives in its own namespace so "is this consensus input?" is
// answerable from the call site — it never is). Read-only queries over
// the pipeline's stores; mutation happens only through the phone-facing
// HTTP endpoints.
type API struct {
	reg    *Registry
	certs  *CertStore
	wins   *WindowManager
	alarms *AlarmBuffer
}

// NewAPI creates the RPC service.
func NewAPI(reg *Registry, certs *CertStore, wins *WindowManager, alarms *AlarmBuffer) *API {
	return &API{reg: reg, certs: certs, wins: wins, alarms: alarms}
}

// GetCertificates returns a block's attestation certificates (majority
// cohort first; more than one entry means registered devices diverged
// on this block's re-execution).
func (a *API) GetCertificates(blockHash types.Hash) []certJSON {
	return encodeCerts(a.certs.Get(blockHash), a.reg.IndexBound())
}

// Status reports pipeline gauges.
func (a *API) Status() map[string]any {
	return map[string]any{
		"registry":    a.reg.Count(),
		"pending":     a.reg.PendingCount(),
		"accumulator": a.reg.Root().Hex(),
		"openWindows": a.wins.OpenWindows(),
		"certBlocks":  a.certs.Len(),
	}
}

// Alarms returns recent divergence alarms (design §6), newest first.
func (a *API) Alarms() []DivergenceAlarm {
	if a.alarms == nil {
		return []DivergenceAlarm{}
	}
	return a.alarms.Recent(100)
}
