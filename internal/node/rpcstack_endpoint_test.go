package node

import (
	"runtime"
	"strings"
	"testing"
)

// TestIPCEndpointResolution covers the resolver behind the IPC no-op
// regression: on Windows a bare ipcpath must become a named-pipe address (the
// only form npipe.Listen accepts); elsewhere it lands under the datadir.
func TestIPCEndpointResolution(t *testing.T) {
	got := ipcEndpoint("n42.ipc", "/data/node")
	if runtime.GOOS == "windows" {
		wantPrefix := `\\.\pipe\`
		if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, "n42.ipc") {
			t.Fatalf("windows endpoint = %q, want a named pipe under %q", got, wantPrefix)
		}
		// An already-qualified pipe path is passed through unchanged.
		qualified := `\\.\pipe\custom`
		if p := ipcEndpoint(qualified, "/data/node"); p != qualified {
			t.Fatalf("qualified pipe path mangled: %q", p)
		}
	} else {
		if !strings.HasSuffix(got, "n42.ipc") || !strings.Contains(got, "data") {
			t.Fatalf("unix endpoint = %q, want datadir-joined n42.ipc", got)
		}
	}
}
