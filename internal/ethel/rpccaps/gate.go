package rpccaps

import "fmt"

// Gate enforces a node's mode capability at request time. The RPC layer holds one
// Gate (built from the node's configured Mode) and calls Check before dispatching
// each eth_ method, so an unsupported call gets a clean, explicit error instead of
// a wrong/opaque result. Advertised() lets the node publish its real surface.
type Gate struct {
	Mode Mode
	// AllowSlow serves methods that are only Support==Slow (e.g. receipts by
	// re-execution). Off by default — Erigon's lesson is that serving receipts by
	// re-execution is not viable, so a node should reject rather than hang.
	AllowSlow bool
}

// ErrNotInMode is the sentinel for a method gated out by the node's mode/scope.
type ErrNotInMode struct {
	Method string
	Scope  Scope
	Mode   Mode
	Sup    Support
}

func (e *ErrNotInMode) Error() string {
	scope := "latest"
	if e.Scope == Historical {
		scope = "historical"
	}
	return fmt.Sprintf("rpccaps: %s (%s) not available in mode %s (support=%s); "+
		"use a node mode that retains the required data", e.Method, scope, e.Mode, e.Sup)
}

// Check returns nil if the method at scope is serviceable in this mode, else an
// *ErrNotInMode. Window and (when AllowSlow) Slow are accepted; No and
// (by default) Slow are rejected.
func (g Gate) Check(method string, scope Scope) error {
	sup := Serviceable(g.Mode, method, scope)
	switch sup {
	case Yes, Window:
		return nil
	case Slow:
		if g.AllowSlow {
			return nil
		}
		return &ErrNotInMode{method, scope, g.Mode, sup}
	default: // No
		return &ErrNotInMode{method, scope, g.Mode, sup}
	}
}

// Advertised returns the eth_ methods this mode can serve at Latest scope
// (Yes/Window, plus Slow when AllowSlow), sorted, for capability publishing.
func (g Gate) Advertised() []string {
	var out []string
	for _, m := range Methods() {
		if g.Check(m, Latest) == nil {
			out = append(out, m)
		}
	}
	sortStrings(out)
	return out
}

// sortStrings: tiny insertion sort to avoid importing sort for one small slice.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
