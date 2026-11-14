// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package envswitch

import (
	"strings"
	"testing"
	"time"
)

func TestIs1(t *testing.T) {
	for v, want := range map[string]bool{"": false, "1": true, "0": false, "true": false, "on": false, "2": false, " 1": false} {
		t.Setenv("N42_T_IS1", v)
		if got := Is1("N42_T_IS1", Perf, "d"); got != want {
			t.Errorf("Is1(%q)=%v want %v", v, got, want)
		}
	}
}

func TestOptIn(t *testing.T) {
	for v, want := range map[string]bool{"": false, "1": true, "true": true, "TRUE": true, "on": true, "ON": true, "yes": true,
		"0": false, "True": false, "YES": false, "off": false, "2": false} {
		t.Setenv("N42_T_OPTIN", v)
		if got := OptIn("N42_T_OPTIN", Perf, "d"); got != want {
			t.Errorf("OptIn(%q)=%v want %v", v, got, want)
		}
	}
}

func TestNot0(t *testing.T) {
	for v, want := range map[string]bool{"": true, "1": true, "0": false, "false": true, "off": true, "abc": true} {
		t.Setenv("N42_T_NOT0", v)
		if got := Not0("N42_T_NOT0", Perf, "d"); got != want {
			t.Errorf("Not0(%q)=%v want %v", v, got, want)
		}
	}
}

func TestDefaultOn(t *testing.T) {
	for v, want := range map[string]bool{"": true, "1": true, "0": false, "false": false, "FALSE": false, "off": false,
		"OFF": false, "no": false, "False": true, "abc": true, "on": true} {
		t.Setenv("N42_T_DEFON", v)
		if got := DefaultOn("N42_T_DEFON", Perf, "d"); got != want {
			t.Errorf("DefaultOn(%q)=%v want %v", v, got, want)
		}
	}
}

func TestParseBool(t *testing.T) {
	for v, want := range map[string]bool{"": false, "1": true, "t": true, "T": true, "true": true, "TRUE": true, "True": true,
		"0": false, "false": false, "on": false, "abc": false, "yes": false} {
		t.Setenv("N42_T_PB", v)
		if got := ParseBool("N42_T_PB", Diag, "d"); got != want {
			t.Errorf("ParseBool(%q)=%v want %v", v, got, want)
		}
	}
}

func TestInt(t *testing.T) {
	for v, want := range map[string]int{"": 7, "3": 3, "0": 7, "-2": 7, "abc": 7, " 3": 7, "3x": 7, "100": 100} {
		t.Setenv("N42_T_INT", v)
		if got := Int("N42_T_INT", 7, Config, "d"); got != want {
			t.Errorf("Int(%q)=%d want %d", v, got, want)
		}
	}
}

func TestUint64(t *testing.T) {
	for v, want := range map[string]uint64{"": 9, "0": 0, "5": 5, "-1": 9, "abc": 9, "18446744073709551615": 18446744073709551615, "18446744073709551616": 9} {
		t.Setenv("N42_T_U64", v)
		if got := Uint64("N42_T_U64", 9, Config, "d"); got != want {
			t.Errorf("Uint64(%q)=%d want %d", v, got, want)
		}
	}
}

func TestMillis(t *testing.T) {
	for v, want := range map[string]time.Duration{"": time.Second, "0": 0, "250": 250 * time.Millisecond, "-1": time.Second, "abc": time.Second, "1.5": time.Second} {
		t.Setenv("N42_T_MS", v)
		if got := Millis("N42_T_MS", time.Second, Perf, "d"); got != want {
			t.Errorf("Millis(%q)=%v want %v", v, got, want)
		}
	}
}

func TestStringAndRaw(t *testing.T) {
	t.Setenv("N42_T_STR", "")
	if got := String("N42_T_STR", "def", Config, "d"); got != "def" {
		t.Errorf("String empty=%q", got)
	}
	t.Setenv("N42_T_STR", "x y")
	if got := String("N42_T_STR", "def", Config, "d"); got != "x y" {
		t.Errorf("String set=%q", got)
	}
	t.Setenv("N42_T_RAW", " 1 ")
	if got := Raw("N42_T_RAW", Config, "d"); got != " 1 " {
		t.Errorf("Raw=%q", got)
	}
}

func TestRegistryAndInfo(t *testing.T) {
	t.Setenv("N42_T_REG", "1")
	Is1("N42_T_REG", BenchOnly, "reg doc")
	Is1("N42_T_REG", BenchOnly, "reg doc")
	Raw("N42_T_REG", BenchOnly, "reg doc") // wildcard kind: no conflict
	var got *Info
	for _, i := range All() {
		if i.Name == "N42_T_REG" {
			i := i
			got = &i
		}
	}
	if got == nil {
		t.Fatal("not registered")
	}
	if got.Kind != KindIs1 || got.Scope != BenchOnly || got.Doc != "reg doc" || !got.IsSet || got.Raw != "1" || got.Default != "off" {
		t.Errorf("info=%+v", *got)
	}
	if len(got.Sites) != 3 {
		t.Errorf("sites=%v", got.Sites)
	}
	for _, s := range got.Sites {
		if !strings.HasPrefix(s, "common/envswitch/envswitch_test.go:") {
			t.Errorf("site %q", s)
		}
	}
	found := false
	for _, i := range Set() {
		if i.Name == "N42_T_REG" {
			found = true
		}
	}
	if !found {
		t.Error("Set misses a switch present in the environment")
	}
	if len(Conflicts()) != 0 {
		t.Errorf("unexpected conflicts %v", Conflicts())
	}
}

func TestConflict(t *testing.T) {
	Is1("N42_T_CONF", Perf, "d")
	Not0("N42_T_CONF", Perf, "d")
	c := Conflicts()
	if len(c) != 1 || !strings.Contains(c[0], "N42_T_CONF") {
		t.Fatalf("conflicts=%v", c)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("CheckConflicts did not panic")
			}
		}()
		CheckConflicts()
	}()
	mu.Lock()
	conflicts = nil
	mu.Unlock()
}

func TestUnknown(t *testing.T) {
	Declare("N42_T_KNOWN", Diag, "d")
	Allow("N42_T_SHELL")
	got := Unknown([]string{"PATH=/bin", "N42_T_KNOWN=1", "N42_T_SHELL=1", "N42_T_TYPO=1", "N42_T_TYPO=2", "N42_A_TYPO=", "N42_NOEQ"})
	want := []string{"N42_A_TYPO", "N42_NOEQ", "N42_T_TYPO"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Unknown=%v want %v", got, want)
	}
}

func TestScopeString(t *testing.T) {
	for s, w := range map[Scope]string{Perf: "perf", Diag: "diag", Config: "config", Consensus: "consensus", BenchOnly: "bench-only"} {
		if s.String() != w {
			t.Errorf("%d=%q", int(s), s.String())
		}
	}
}
