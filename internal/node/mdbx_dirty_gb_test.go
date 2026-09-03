package node

import (
	"testing"
)

func TestMdbxDirtyGB(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want uint64
		why  string
	}{
		{"", 0, "unset must leave MDBX's computed default alone, not force a value"},
		{"4", 4, "a plain number is GB"},
		{"0", 0, "zero is not a limit; treat it as unset rather than passing 0 to DirtySpace"},
		{"-1", 0, "negative is nonsense; fall back rather than wrap around to a huge limit"},
		{"abc", 0, "garbage must not become a limit"},
		{"18446744073709551616", 0, "overflow must not wrap"},
	} {
		t.Setenv("N42_MDBX_DIRTY_GB", tc.env)
		if got := mdbxDirtyGB(); got != tc.want {
			t.Errorf("N42_MDBX_DIRTY_GB=%q -> %d, want %d: %s", tc.env, got, tc.want, tc.why)
		}
	}
}
