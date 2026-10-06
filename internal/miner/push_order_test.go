package miner

import (
	"os"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

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

// S19 (docs/QS_BLOCK_TIME_BUDGET.md 6co): N42_LEADER_WRITE_AFTER_JOURNAL_TIMEOUT_MS
// parsing -- default 150ms, any positive integer overrides it, anything else
// (empty, zero, negative, non-numeric) falls back to the default rather than
// producing a zero or negative wait.
func TestParseLeaderWriteAfterJournalTimeoutMs(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want time.Duration
	}{
		{"", 150 * time.Millisecond},
		{"0", 150 * time.Millisecond},
		{"-5", 150 * time.Millisecond},
		{"abc", 150 * time.Millisecond},
		{"1", 1 * time.Millisecond},
		{"300", 300 * time.Millisecond},
	} {
		if got := parseLeaderWriteAfterJournalTimeoutMs(tc.v); got != tc.want {
			t.Errorf("parseLeaderWriteAfterJournalTimeoutMs(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

// N42_LEADER_WRITE_AFTER_JOURNAL must default to off, matching
// N42_PUSH_BEFORE_WRITE/N42_PROPOSE_BEFORE_WRITE above: this changes the
// leader's own write timing and has to be earned by a round, not defaulted in.
func TestLeaderWriteAfterJournalDefaultsOff(t *testing.T) {
	if os.Getenv("N42_LEADER_WRITE_AFTER_JOURNAL") == "1" {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 is set in this test binary's environment")
	}
	if LeaderWriteAfterJournalOn() {
		t.Fatal("leader-write-after-journal must default to off")
	}
}

// commitVoteJournalWaiterSatisfiedByNothingUnexpected is a compile-time check
// that the interface's method set is exactly what worker.go's call site
// expects -- a mismatched signature here would otherwise only surface as a
// silent "unsupported" lwWhy at runtime, never a build failure.
var _ commitVoteJournalWaiter = (*fakeCommitVoteJournalWaiter)(nil)

type fakeCommitVoteJournalWaiter struct{}

func (fakeCommitVoteJournalWaiter) WaitForCommitVoteJournal(hash types.Hash, timeout time.Duration) (time.Duration, string) {
	return 0, "journal"
}
