package snapsync

import (
	"errors"
	"testing"
)

type fakeByteReader struct {
	data []byte
	err  error
}

func (r *fakeByteReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n := copy(p, r.data)
	return n, nil
}

func TestReadSnapStatusCodeOK(t *testing.T) {
	code, msg, err := readSnapStatusCode(&fakeByteReader{data: []byte{0}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 || msg != "" {
		t.Fatalf("got code=%d msg=%q, want 0/\"\"", code, msg)
	}
}

func TestReadSnapStatusCodeError(t *testing.T) {
	code, msg, err := readSnapStatusCode(&fakeByteReader{data: []byte{5}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 5 || msg == "" {
		t.Fatalf("got code=%d msg=%q, want nonzero code and a message", code, msg)
	}
}

func TestReadSnapStatusCodeReadError(t *testing.T) {
	wantErr := errors.New("boom")
	if _, _, err := readSnapStatusCode(&fakeByteReader{err: wantErr}); err != wantErr {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestErrorFromCode(t *testing.T) {
	err := errorFromCode(7, "oops")
	if err == nil || err.Error() != "oops" {
		t.Fatalf("errorFromCode() = %v, want an error with message %q", err, "oops")
	}
}

// TestManagerStop covers Manager.Stop(), which simply waits for any tracked
// goroutines (none here) to finish.
func TestManagerStop(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, nil)
	mgr := NewManager(nil, p2pStub, db, 0, nil)
	mgr.Stop() // must return promptly with no in-flight goroutines
}
