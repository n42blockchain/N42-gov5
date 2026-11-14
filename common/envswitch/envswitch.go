// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package envswitch is the process-wide registry of the N42_* environment
// switches. Every switch is read through one of the typed readers below (or
// announced with Declare when the read itself is lazy), so the node can list
// which switches are set, in which role they were meant to apply, and which
// N42_* names in the environment nobody reads (typos fail silently otherwise).
//
// The readers read the environment exactly once, at call time, and are meant
// to be called from package-level variable initialisers. The package imports
// only the standard library.
package envswitch

import (
	"fmt"
	"os"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scope tells in which role a switch is meant to take effect.
type Scope int

const (
	// Perf switches change speed or memory use only, not results.
	Perf Scope = iota
	// Diag switches add logging, dumps, traces or timing.
	Diag
	// Config switches select a mode or a path or a size.
	Config
	// Consensus switches change a rule every validator must apply alike.
	Consensus
	// BenchOnly switches exist for benchmarks and tests and must not be set
	// on a production node.
	BenchOnly
)

// String returns the lower-case scope name.
func (s Scope) String() string {
	switch s {
	case Perf:
		return "perf"
	case Diag:
		return "diag"
	case Config:
		return "config"
	case Consensus:
		return "consensus"
	case BenchOnly:
		return "bench-only"
	}
	return "scope(" + strconv.Itoa(int(s)) + ")"
}

// Kind names the parse rule of a reader.
type Kind string

// The reader kinds. KindRaw and KindDeclared do not parse: the call site keeps
// its own rule, so they never conflict with another kind.
const (
	KindIs1       Kind = "is1"       // true only for "1"
	KindOptIn     Kind = "optin"     // default off; "1","true","TRUE","on","ON","yes" enable
	KindNot0      Kind = "not0"      // default on; only "0" disables
	KindDefaultOn Kind = "defaulton" // default on; "0","false","FALSE","off","OFF","no" disable
	KindParseBool Kind = "parsebool" // strconv.ParseBool, false on error
	KindInt       Kind = "int"       // positive int, else the default
	KindUint64    Kind = "uint64"    // ParseUint base 10, else the default
	KindMillis    Kind = "millis"    // non-negative milliseconds, else the default
	KindString    Kind = "string"    // the value, or the default when empty
	KindRaw       Kind = "raw"       // registered; the site parses os.Getenv's value itself
	KindDeclared  Kind = "declared"  // registered; the site reads the environment lazily
)

// Info describes one registered switch.
type Info struct {
	Name    string
	Kind    Kind
	Scope   Scope
	Doc     string
	Default string   // human-readable default
	Raw     string   // current value in the environment ("" when unset)
	IsSet   bool     // present in the environment
	Sites   []string // registering call sites, "pkg/file.go:line"
}

type entry struct {
	name  string
	kind  Kind
	scope Scope
	doc   string
	def   string
	sites []string
}

var (
	mu       sync.Mutex
	registry = map[string]*entry{}
	allowed  = map[string]bool{}
	// conflicts collects registrations that disagree with an earlier one.
	conflicts []string
	moduleRe  = "github.com/n42blockchain/N42/"
)

func wildcard(k Kind) bool { return k == KindRaw || k == KindDeclared }

// site returns "pkg/file.go:line" of the caller skip frames above register.
func site(skip int) string {
	pc, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return "?"
	}
	pkg := ""
	if fn := runtime.FuncForPC(pc); fn != nil {
		n := strings.TrimPrefix(fn.Name(), moduleRe)
		slash := strings.LastIndex(n, "/")
		dot := strings.Index(n[slash+1:], ".")
		if dot >= 0 {
			pkg = n[:slash+1+dot]
		}
	}
	return path.Join(pkg, path.Base(file)) + ":" + strconv.Itoa(line)
}

func register(name string, kind Kind, scope Scope, doc, def string, skip int) {
	s := site(skip + 1)
	mu.Lock()
	defer mu.Unlock()
	e, ok := registry[name]
	if !ok {
		registry[name] = &entry{name: name, kind: kind, scope: scope, doc: doc, def: def, sites: []string{s}}
		return
	}
	switch {
	case e.kind != kind && !wildcard(e.kind) && !wildcard(kind):
		conflicts = append(conflicts, fmt.Sprintf("%s: kind %s at %s conflicts with %s at %s", name, kind, s, e.kind, e.sites[0]))
	case e.scope != scope:
		conflicts = append(conflicts, fmt.Sprintf("%s: scope %s at %s conflicts with %s at %s", name, scope, s, e.scope, e.sites[0]))
	}
	if wildcard(e.kind) && !wildcard(kind) {
		e.kind, e.def = kind, def
	}
	if e.doc == "" {
		e.doc = doc
	}
	for _, x := range e.sites {
		if x == s {
			return
		}
	}
	e.sites = append(e.sites, s)
}

// Conflicts reports registrations of one name that disagree on the parse kind
// or the scope. Tests call it; the registry itself never panics at run time.
func Conflicts() []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), conflicts...)
}

// CheckConflicts panics when Conflicts is not empty. It is meant for tests.
func CheckConflicts() {
	if c := Conflicts(); len(c) > 0 {
		panic("envswitch: conflicting registrations:\n" + strings.Join(c, "\n"))
	}
}

// Is1 reports whether the switch is exactly "1".
func Is1(name string, scope Scope, doc string) bool {
	register(name, KindIs1, scope, doc, "off", 1)
	return os.Getenv(name) == "1"
}

// parseOptIn is the rule shared by every opt-in switch.
func parseOptIn(v string) bool {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return true
	}
	return false
}

// OptIn is a default-off switch that "1", "true", "TRUE", "on", "ON" and
// "yes" turn on.
func OptIn(name string, scope Scope, doc string) bool {
	register(name, KindOptIn, scope, doc, "off", 1)
	return parseOptIn(os.Getenv(name))
}

// Not0 is a default-on switch that only "0" turns off.
func Not0(name string, scope Scope, doc string) bool {
	register(name, KindNot0, scope, doc, "on", 1)
	return os.Getenv(name) != "0"
}

// DefaultOn is a default-on switch that "0", "false", "FALSE", "off", "OFF"
// and "no" turn off.
func DefaultOn(name string, scope Scope, doc string) bool {
	register(name, KindDefaultOn, scope, doc, "on", 1)
	switch os.Getenv(name) {
	case "0", "false", "FALSE", "off", "OFF", "no":
		return false
	}
	return true
}

// ParseBool is strconv.ParseBool of the value, false when unset or invalid.
func ParseBool(name string, scope Scope, doc string) bool {
	register(name, KindParseBool, scope, doc, "off", 1)
	v, _ := strconv.ParseBool(os.Getenv(name))
	return v
}

// Int reads a positive integer; anything unset, unparseable or non-positive
// yields def.
func Int(name string, def int, scope Scope, doc string) int {
	register(name, KindInt, scope, doc, strconv.Itoa(def), 1)
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Uint64 reads a base-10 unsigned integer; anything unset or unparseable
// yields def.
func Uint64(name string, def uint64, scope Scope, doc string) uint64 {
	register(name, KindUint64, scope, doc, strconv.FormatUint(def, 10), 1)
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Millis reads a non-negative number of milliseconds; anything unset,
// unparseable or negative yields def.
func Millis(name string, def time.Duration, scope Scope, doc string) time.Duration {
	register(name, KindMillis, scope, doc, def.String(), 1)
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return def
}

// String returns the value, or def when it is empty.
func String(name, def string, scope Scope, doc string) string {
	register(name, KindString, scope, doc, strconv.Quote(def), 1)
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Raw registers the switch and returns os.Getenv(name) untouched; the call
// site keeps its own parsing.
func Raw(name string, scope Scope, doc string) string {
	register(name, KindRaw, scope, doc, "", 1)
	return os.Getenv(name)
}

// Declare registers a switch whose read stays lazy at the call site.
func Declare(name string, scope Scope, doc string) {
	register(name, KindDeclared, scope, doc, "", 1)
}

// Allow marks names as known although no Go code reads them (shell wrappers
// consume them), so Unknown does not report them.
func Allow(names ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, n := range names {
		allowed[n] = true
	}
}

func toInfo(e *entry) Info {
	raw, set := os.LookupEnv(e.name)
	return Info{
		Name: e.name, Kind: e.kind, Scope: e.scope, Doc: e.doc, Default: e.def,
		Raw: raw, IsSet: set, Sites: append([]string(nil), e.sites...),
	}
}

// All returns every registered switch sorted by name.
func All() []Info {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Info, 0, len(registry))
	for _, e := range registry {
		out = append(out, toInfo(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Set returns the registered switches present in the environment, sorted by
// name.
func Set() []Info {
	all := All()
	out := all[:0]
	for _, i := range all {
		if i.IsSet {
			out = append(out, i)
		}
	}
	return out
}

// Unknown returns the N42_* names in environ (os.Environ() form) that no
// reader registered and Allow did not list, sorted.
func Unknown(environ []string) []string {
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "N42_") || seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := registry[name]; ok || allowed[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
