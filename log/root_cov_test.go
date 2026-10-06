package log

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/natefinch/lumberjack.v2"
)

func TestSetLevelClampsToValidRange(t *testing.T) {
	orig := GetLevel()
	defer SetLevel(orig)

	SetLevel(-100)
	if got := GetLevel(); got != int(LvlCrit) {
		t.Fatalf("SetLevel(-100) -> GetLevel() = %d, want %d", got, int(LvlCrit))
	}

	SetLevel(100)
	if got := GetLevel(); got != int(LvlTrace) {
		t.Fatalf("SetLevel(100) -> GetLevel() = %d, want %d", got, int(LvlTrace))
	}

	SetLevel(int(LvlInfo))
	if got := GetLevel(); got != int(LvlInfo) {
		t.Fatalf("SetLevel(LvlInfo) -> GetLevel() = %d, want %d", got, int(LvlInfo))
	}
}

func TestLogDirNoWriter(t *testing.T) {
	saved := logWriter
	logWriter = nil
	defer func() { logWriter = saved }()

	if got := LogDir(); got != "" {
		t.Fatalf("LogDir() with nil writer = %q, want empty", got)
	}
}

func TestLogDirWithWriter(t *testing.T) {
	saved := logWriter
	defer func() { logWriter = saved }()

	dir := t.TempDir()
	path := filepath.Join(dir, "n42.log")
	logWriter = &lumberjack.Logger{Filename: path}

	if got := LogDir(); got != dir {
		t.Fatalf("LogDir() = %q, want %q", got, dir)
	}
}

func TestInitMobileLoggerSkipsWhenNotDebug(t *testing.T) {
	// Should be a safe no-op; nothing to assert beyond "does not panic".
	InitMobileLogger(filepath.Join(t.TempDir(), "mobile.log"), false)
}

func TestInitMobileLoggerDebugWritesFile(t *testing.T) {
	savedOut := terminal.Out
	savedLevel := terminal.GetLevel()
	savedFormatter := terminal.Formatter
	defer func() {
		terminal.SetOutput(savedOut)
		terminal.SetLevel(savedLevel)
		terminal.SetFormatter(savedFormatter)
	}()

	dir := t.TempDir()
	path := filepath.Join(dir, "mobile.log")
	InitMobileLogger(path, true)

	terminal.Info("mobile logger smoke test")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected mobile log file to be created: %v", err)
	}
}

func TestPackageLevelLoggingWrappers(t *testing.T) {
	// These exercise the package-level convenience wrappers around root.write.
	// They should not panic regardless of current level.
	orig := GetLevel()
	defer SetLevel(orig)
	SetLevel(int(LvlTrace))

	Trace("trace msg", "k", "v")
	Tracef("trace %s", "fmt")
	Debug("debug msg", "k", "v")
	Debugf("debug %s", "fmt")
	Info("info msg", "k", "v")
	Infof("info %s", "fmt")
	Warn("warn msg", "k", "v")
	Warnf("warn %s", "fmt")
	Error("error msg", "k", "v")
	Errorf("error %s", "fmt")
}

func TestNewAndRootHelpers(t *testing.T) {
	l := New("component", "test")
	if l == nil {
		t.Fatal("New returned nil logger")
	}
	if Root() == nil {
		t.Fatal("Root() returned nil")
	}
}

func TestColorSupportedEnvOverrides(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if colorSupported(os.Stdout) {
		t.Fatal("NO_COLOR=1 should disable colour regardless of terminal detection")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("N42_LOG_COLOR", "1")
	if !colorSupported(os.Stdout) {
		t.Fatal("N42_LOG_COLOR=1 should force colour on")
	}
}

func TestNewFileFormatterVariants(t *testing.T) {
	if f := newFileFormatter(true); f == nil {
		t.Fatal("expected a JSON formatter")
	}
	if f := newFileFormatter(false); f == nil {
		t.Fatal("expected a text formatter")
	}
}
