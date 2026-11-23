package sync

import (
	"strings"
	"testing"
)

// A block_by_hash miss is answered by n42-26 with the bare code 0x01 and by
// gov5/n42-rs with 0x02 plus a framed message. Both must read as a remote
// error code (a miss), never as a transport fault.
func TestReadChunkedBlockAcceptsBothMissEncodings(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, fp *fakeP2P) []byte
		code  string
	}{
		{"bare code", func(t *testing.T, fp *fakeP2P) []byte { return []byte{responseCodeInvalidRequest} }, "error code 1"},
		{"code and message", func(t *testing.T, fp *fakeP2P) []byte {
			b, err := createErrorResponse(responseCodeServerError, "block not found", fp)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}, "error code 2: block not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakeP2P(t)
			client, server := connectedStreamServer(t, fp)
			defer client.Close()
			payload := tc.write(t, fp)
			go func() {
				_, _ = server.Write(payload)
				_ = server.Close()
			}()
			_, err := ReadChunkedBlock(client, fp, true)
			if err == nil {
				t.Fatal("expected a miss error")
			}
			if !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("got %q, want it to report %q", err, tc.code)
			}
			if strings.Contains(err.Error(), "failed to read status code") {
				t.Fatalf("miss reported as a transport fault: %v", err)
			}
		})
	}
}
