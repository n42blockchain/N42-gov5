package netutil

import (
	"errors"
	"net"
	"testing"
)

type tempError struct{ temporary bool }

func (e tempError) Error() string   { return "temp error" }
func (e tempError) Temporary() bool { return e.temporary }

type timeoutError struct{ timeout bool }

func (e timeoutError) Error() string { return "timeout error" }
func (e timeoutError) Timeout() bool { return e.timeout }

func TestIsTemporaryError(t *testing.T) {
	if !IsTemporaryError(tempError{true}) {
		t.Error("expected temporary error to be reported as temporary")
	}
	if IsTemporaryError(tempError{false}) {
		t.Error("expected non-temporary error to not be reported as temporary")
	}
	if IsTemporaryError(errors.New("plain error")) {
		t.Error("expected plain error to not be temporary")
	}
}

func TestIsTimeout(t *testing.T) {
	if !IsTimeout(timeoutError{true}) {
		t.Error("expected timeout error to be reported as timeout")
	}
	if IsTimeout(timeoutError{false}) {
		t.Error("expected non-timeout error to not be reported as timeout")
	}
	if IsTimeout(errors.New("plain error")) {
		t.Error("expected plain error to not be a timeout")
	}
}

func TestIsTimeoutRealNetError(t *testing.T) {
	// net.DNSError implements the Timeout() interface.
	var err error = &net.DNSError{IsTimeout: true}
	if !IsTimeout(err) {
		t.Error("expected DNS timeout error to be reported as timeout")
	}
}
