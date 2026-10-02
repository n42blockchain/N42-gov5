package log

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

// g41CaptureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. The Print* helpers in pretty_formatter.go write
// directly to os.Stdout, so this is the only way to exercise them.
func g41CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestTitleCase(t *testing.T) {
	if got := titleCase(""); got != "" {
		t.Fatalf("titleCase(\"\") = %q, want empty", got)
	}
	if got := titleCase("hello"); got != "Hello" {
		t.Fatalf("titleCase(hello) = %q, want Hello", got)
	}
}

func TestHighlightKeywords(t *testing.T) {
	f := NewPrettyFormatter()
	if got := f.highlightKeywords("node started successfully"); got == "node started successfully" {
		t.Fatal("expected success keyword to be highlighted")
	}
	if got := f.highlightKeywords("connection failed"); got == "connection failed" {
		t.Fatal("expected failure keyword to be highlighted")
	}
	if got := f.highlightKeywords("nothing interesting"); got != "nothing interesting" {
		t.Fatalf("expected unmodified message, got %q", got)
	}
}

func TestTruncateHash(t *testing.T) {
	short := "0x1234"
	if got := truncateHash(short); got != short {
		t.Fatalf("truncateHash(short) = %q, want unchanged", got)
	}
	long := "0x1111111111111111111111111111111111111111"
	got := truncateHash(long)
	if got == long {
		t.Fatal("expected long hash to be truncated")
	}
}

func TestFormatPeerID(t *testing.T) {
	short := "peer1"
	if got := FormatPeerID(short); got != short {
		t.Fatalf("FormatPeerID(short) = %q, want unchanged", got)
	}
	long := "QmSomeVeryLongPeerIDStringThatExceedsTwentyChars"
	if got := FormatPeerID(long); got == long {
		t.Fatal("expected long peer id to be truncated")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]bool{
		500 * time.Millisecond: true,
		5 * time.Second:        true,
		90 * time.Second:       true,
		90 * time.Minute:       true,
	}
	for d := range cases {
		if got := FormatDuration(d); got == "" {
			t.Errorf("FormatDuration(%v) returned empty string", d)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []uint64{0, 500, 1024, 1024 * 1024, 1024 * 1024 * 1024}
	for _, b := range cases {
		if got := FormatBytes(b); got == "" {
			t.Errorf("FormatBytes(%d) returned empty string", b)
		}
	}
}

func TestGetTerminalWidthAndIsTerminalOut(t *testing.T) {
	// Just exercise for panics; exact values depend on the test runner's stdout.
	_ = getTerminalWidth()
	_ = isTerminalOut()
}

func TestClearProgressLineNoOp(t *testing.T) {
	printMu.Lock()
	progressActive = false
	printMu.Unlock()

	out := g41CaptureStdout(t, func() {
		printMu.Lock()
		clearProgressLine()
		printMu.Unlock()
	})
	if out != "" {
		t.Fatalf("expected no output when progress bar inactive, got %q", out)
	}
}

func TestClearProgressLineActive(t *testing.T) {
	printMu.Lock()
	progressActive = true
	printMu.Unlock()

	out := g41CaptureStdout(t, func() {
		printMu.Lock()
		clearProgressLine()
		printMu.Unlock()
	})
	if out == "" {
		t.Fatal("expected a newline to be printed when progress bar was active")
	}
}

func TestPrintBannerSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() {
		PrintBanner("v1.0.0", "private", "hotstuff", "0xdead", 42, "linux", "go1.26", 8, "/tmp/data")
	})
	if out == "" {
		t.Fatal("expected PrintBanner to produce output")
	}
}

func TestPrintStartupProgressSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() {
		PrintStartupProgress(1, 5, "database")
	})
	if out == "" {
		t.Fatal("expected PrintStartupProgress to produce output")
	}
}

func TestPrintSubItemSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintSubItem("sub detail") })
	if out == "" {
		t.Fatal("expected PrintSubItem to produce output")
	}
}

func TestPrintSuccessSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintSuccess("it worked") })
	if out == "" {
		t.Fatal("expected PrintSuccess to produce output")
	}
}

func TestPrintErrorSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintError("it failed") })
	if out == "" {
		t.Fatal("expected PrintError to produce output")
	}
}

func TestPrintWarningSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintWarning("careful") })
	if out == "" {
		t.Fatal("expected PrintWarning to produce output")
	}
}

func TestPrintShutdownBannerSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintShutdownBanner() })
	if out == "" {
		t.Fatal("expected PrintShutdownBanner to produce output")
	}
}

func TestPrintShutdownStepSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintShutdownStep(1, 3, "p2p") })
	if out == "" {
		t.Fatal("expected PrintShutdownStep to produce output")
	}
}

func TestPrintShutdownCompleteSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintShutdownComplete() })
	if out == "" {
		t.Fatal("expected PrintShutdownComplete to produce output")
	}
}

func TestPrintWaitingSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintWaiting("waiting for peers", 2, 5) })
	if out == "" {
		t.Fatal("expected PrintWaiting to produce output")
	}
}

func TestPrintSectionAndDividerSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() { PrintSection("Blockchain") })
	if out == "" {
		t.Fatal("expected PrintSection to produce output")
	}
	out = g41CaptureStdout(t, func() { PrintDivider() })
	if out == "" {
		t.Fatal("expected PrintDivider to produce output")
	}
}

func TestPrintKeyValueSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() {
		PrintKeyValue([][2]string{{"OS", "linux"}, {"Go", "go1.26"}})
	})
	if out == "" {
		t.Fatal("expected PrintKeyValue to produce output")
	}
}

func TestPrintSystemInfoSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() {
		PrintSystemInfo("linux", "go1.26", 8, "/tmp/data")
	})
	if out == "" {
		t.Fatal("expected PrintSystemInfo to produce output")
	}
}

func TestPrintProgressBarSmoke(t *testing.T) {
	printMu.Lock()
	progressActive = false
	printMu.Unlock()
	out := g41CaptureStdout(t, func() {
		PrintProgressBar("Syncing", 50, 100, 500.0, "5h58m", 3)
	})
	if out == "" {
		t.Fatal("expected PrintProgressBar to produce output")
	}
	printMu.Lock()
	progressActive = false
	printMu.Unlock()
}

func TestPrintStatusLineSmoke(t *testing.T) {
	printMu.Lock()
	progressActive = false
	printMu.Unlock()
	out := g41CaptureStdout(t, func() {
		PrintStatusLine("p2p", "3 peers (in:1 out:2)")
	})
	if out == "" {
		t.Fatal("expected PrintStatusLine to produce output")
	}
}

func TestPrintStatusLineSkippedDuringProgress(t *testing.T) {
	printMu.Lock()
	progressActive = true
	printMu.Unlock()
	out := g41CaptureStdout(t, func() {
		PrintStatusLine("p2p", "3 peers")
	})
	printMu.Lock()
	progressActive = false
	printMu.Unlock()
	if out != "" {
		t.Fatalf("expected no output while progress bar active, got %q", out)
	}
}

func TestPrintErrorBoxSmoke(t *testing.T) {
	out := g41CaptureStdout(t, func() {
		PrintErrorBox("Fatal Error", []string{"detail one", "a somewhat longer detail line here"})
	})
	if out == "" {
		t.Fatal("expected PrintErrorBox to produce output")
	}
}
