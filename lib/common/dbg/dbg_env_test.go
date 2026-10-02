package dbg

import (
	"testing"
	"time"
)

func TestEnvString(t *testing.T) {
	if got := EnvString("DBG_TEST_ENVSTRING_UNSET", "default"); got != "default" {
		t.Fatalf("got %q, want default", got)
	}
	t.Setenv("DBG_TEST_ENVSTRING_SET", "value")
	if got := EnvString("DBG_TEST_ENVSTRING_SET", "default"); got != "value" {
		t.Fatalf("got %q, want value", got)
	}
}

func TestEnvBool(t *testing.T) {
	if got := EnvBool("DBG_TEST_ENVBOOL_UNSET", true); got != true {
		t.Fatalf("got %v, want true (default)", got)
	}
	t.Setenv("DBG_TEST_ENVBOOL_TRUE", "true")
	if got := EnvBool("DBG_TEST_ENVBOOL_TRUE", false); got != true {
		t.Fatalf("got %v, want true", got)
	}
	t.Setenv("DBG_TEST_ENVBOOL_FALSE", "false")
	if got := EnvBool("DBG_TEST_ENVBOOL_FALSE", true); got != false {
		t.Fatalf("got %v, want false", got)
	}
	t.Setenv("DBG_TEST_ENVBOOL_OTHER", "banana")
	if got := EnvBool("DBG_TEST_ENVBOOL_OTHER", true); got != true {
		t.Fatalf("got %v, want default true for unrecognized value", got)
	}
}

func TestEnvInt(t *testing.T) {
	if got := EnvInt("DBG_TEST_ENVINT_UNSET", 2); got != 2 {
		t.Fatalf("got %d, want default 2", got)
	}
	t.Setenv("DBG_TEST_ENVINT_SET", "3")
	if got := EnvInt("DBG_TEST_ENVINT_SET", 0); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestEnvIntPanicsOnBadValue(t *testing.T) {
	t.Setenv("DBG_TEST_ENVINT_BAD", "not-a-number")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for non-numeric EnvInt value")
		}
	}()
	EnvInt("DBG_TEST_ENVINT_BAD", 0)
}

func TestEnvIntPanicsOnOutOfRange(t *testing.T) {
	t.Setenv("DBG_TEST_ENVINT_RANGE", "5")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for out-of-range EnvInt value")
		}
	}()
	EnvInt("DBG_TEST_ENVINT_RANGE", 0)
}

func TestEnvDataSize(t *testing.T) {
	def := EnvDataSize("DBG_TEST_ENVDATASIZE_UNSET", 0)
	if def != 0 {
		t.Fatalf("got %v, want 0 default", def)
	}
	t.Setenv("DBG_TEST_ENVDATASIZE_SET", "1MB")
	got := EnvDataSize("DBG_TEST_ENVDATASIZE_SET", 0)
	if got == 0 {
		t.Fatal("expected non-zero parsed datasize")
	}
}

func TestEnvDataSizePanicsOnBadValue(t *testing.T) {
	t.Setenv("DBG_TEST_ENVDATASIZE_BAD", "not-a-size")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid datasize value")
		}
	}()
	EnvDataSize("DBG_TEST_ENVDATASIZE_BAD", 0)
}

func TestLazyEnvBool(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_BOOL_A", "true")
	f := lazyEnvBool("DBG_TEST_LAZY_BOOL_A")
	if !f() {
		t.Fatal("expected true")
	}

	f2 := lazyEnvBool("DBG_TEST_LAZY_BOOL_UNSET")
	if f2() {
		t.Fatal("expected false for unset var")
	}
}

func TestLazyEnvString(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_STRING_A", "hello")
	f := lazyEnvString("DBG_TEST_LAZY_STRING_A")
	if got := f(); got != "hello" {
		t.Fatalf("got %q, want hello", got)
	}

	f2 := lazyEnvString("DBG_TEST_LAZY_STRING_UNSET")
	if got := f2(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestLazyEnvUint(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_UINT_A", "7")
	f := lazyEnvUint("DBG_TEST_LAZY_UINT_A")
	if got := f(); got != 7 {
		t.Fatalf("got %d, want 7", got)
	}

	f2 := lazyEnvUint("DBG_TEST_LAZY_UINT_UNSET")
	if got := f2(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestLazyEnvUintPanicsOnBadValue(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_UINT_BAD", "nope")
	f := lazyEnvUint("DBG_TEST_LAZY_UINT_BAD")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	f()
}

func TestLazyEnvUint64(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_UINT64_A", "123456789")
	f := lazyEnvUint64("DBG_TEST_LAZY_UINT64_A")
	if got := f(); got != 123456789 {
		t.Fatalf("got %d, want 123456789", got)
	}

	f2 := lazyEnvUint64("DBG_TEST_LAZY_UINT64_UNSET")
	if got := f2(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}

	// Parse error is swallowed silently (not panicking), falls back to 0.
	t.Setenv("DBG_TEST_LAZY_UINT64_BAD", "not-a-number")
	f3 := lazyEnvUint64("DBG_TEST_LAZY_UINT64_BAD")
	if got := f3(); got != 0 {
		t.Fatalf("got %d, want 0 on parse error", got)
	}
}

func TestLazyEnvUint8(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_UINT8_A", "9")
	f := lazyEnvUint8("DBG_TEST_LAZY_UINT8_A")
	if got := f(); got != 9 {
		t.Fatalf("got %d, want 9", got)
	}

	f2 := lazyEnvUint8("DBG_TEST_LAZY_UINT8_UNSET")
	if got := f2(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}

	// "0" is not > 0, so it also falls into the zero-value path.
	t.Setenv("DBG_TEST_LAZY_UINT8_ZERO", "0")
	f3 := lazyEnvUint8("DBG_TEST_LAZY_UINT8_ZERO")
	if got := f3(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestLazyEnvDuration(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_DURATION_A", "5s")
	f := lazyEnvDuration("DBG_TEST_LAZY_DURATION_A")
	if got := f(); got != 5*time.Second {
		t.Fatalf("got %v, want 5s", got)
	}

	f2 := lazyEnvDuration("DBG_TEST_LAZY_DURATION_UNSET")
	if got := f2(); got != 0 {
		t.Fatalf("got %v, want 0", got)
	}
}

func TestLazyEnvDurationPanicsOnBadValue(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_DURATION_BAD", "nope")
	f := lazyEnvDuration("DBG_TEST_LAZY_DURATION_BAD")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	f()
}

func TestLazyEnvIntBounded(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_INTBOUNDED_A", "3")
	f := lazyEnvIntBounded("DBG_TEST_LAZY_INTBOUNDED_A")
	if got := f(); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}

	f2 := lazyEnvIntBounded("DBG_TEST_LAZY_INTBOUNDED_UNSET")
	if got := f2(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestLazyEnvIntBoundedPanicsOnBadValue(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_INTBOUNDED_BAD", "nope")
	f := lazyEnvIntBounded("DBG_TEST_LAZY_INTBOUNDED_BAD")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for non-numeric value")
		}
	}()
	f()
}

func TestLazyEnvIntBoundedPanicsOnOutOfRange(t *testing.T) {
	t.Setenv("DBG_TEST_LAZY_INTBOUNDED_RANGE", "9")
	f := lazyEnvIntBounded("DBG_TEST_LAZY_INTBOUNDED_RANGE")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for out-of-range value")
		}
	}()
	f()
}

func TestKVReadLevelledMetricsDefault(t *testing.T) {
	// Just exercises the package-level var declaration.
	if KVReadLevelledMetrics {
		t.Fatal("expected KVReadLevelledMetrics to default to false")
	}
}
