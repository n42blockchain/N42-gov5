package miner

import "testing"

func TestParsePushBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"", false}, {"0", false}, {"false", false}, {"off", false}, {"no", false},
		{"maybe", false}, {"2", false}, {" 1", false},
		{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {"on", true},
	} {
		if got := parsePushBeforeWrite(tc.v); got != tc.want {
			t.Errorf("parsePushBeforeWrite(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

// The default has to be OFF. This reorders the block production critical path,
// and a build that silently enabled it would change every node's behaviour with
// no round behind it.
func TestPushBeforeWriteDefaultsOff(t *testing.T) {
	t.Setenv("N42_PUSH_BEFORE_WRITE", "")
	if parsePushBeforeWrite("") {
		t.Fatal("push-before-write must default to off")
	}
}
