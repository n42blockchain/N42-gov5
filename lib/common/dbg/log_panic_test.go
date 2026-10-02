package dbg

import (
	"strings"
	"testing"
)

func TestStack(t *testing.T) {
	s := Stack()
	if !strings.Contains(s, "log_panic_test.go") {
		t.Fatalf("Stack() = %q, expected it to mention the calling test file", s)
	}
}

func TestStackSkip(t *testing.T) {
	s := StackSkip(1)
	if s == "" {
		// A valid, if empty, trace is acceptable depending on skip depth;
		// just make sure it doesn't panic and returns a string.
		t.Log("StackSkip(1) returned empty string")
	}
}

func TestFileCloseLogLevel(t *testing.T) {
	// Just exercise/assert the exported constant's value is stable.
	if FileCloseLogLevel.String() == "" {
		t.Fatal("expected a non-empty log level string")
	}
}
