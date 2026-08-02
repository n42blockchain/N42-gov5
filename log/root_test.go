// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package log

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/sirupsen/logrus"
)

func resetLoggerGlobals(t *testing.T) {
	t.Helper()

	oldTerminal := terminal
	oldLogManager := logManager
	oldLogWriter := logWriter

	Close()
	terminal = logrus.New()
	logManager = nil
	logWriter = nil

	t.Cleanup(func() {
		Close()
		terminal = oldTerminal
		logManager = oldLogManager
		logWriter = oldLogWriter
	})
}

// TestLogLevels 测试日志级别
func TestLogLevels(t *testing.T) {
	tests := []struct {
		level Lvl
		name  string
	}{
		{LvlCrit, "Crit"},
		{LvlFatal, "Fatal"},
		{LvlError, "Error"},
		{LvlWarn, "Warn"},
		{LvlInfo, "Info"},
		{LvlDebug, "Debug"},
		{LvlTrace, "Trace"},
	}

	for i, tt := range tests {
		if int(tt.level) != i {
			t.Errorf("Level %s expected value %d, got %d", tt.name, i, tt.level)
		}
	}
	t.Log("✓ All log levels are correctly defined")
}

// TestLoggerInterface 测试 Logger 接口
func TestLoggerInterface(t *testing.T) {
	// 验证 logger 实现了 Logger 接口
	var _ Logger = &logger{}
	t.Log("✓ logger implements Logger interface")
}

// TestRootLogger 测试根日志器
func TestRootLogger(t *testing.T) {
	root := Root()
	if root == nil {
		t.Fatal("Root logger should not be nil")
	}
	t.Log("✓ Root logger is available")
}

// TestNewLogger 测试创建新日志器
func TestNewLogger(t *testing.T) {
	log := New("module", "test")
	if log == nil {
		t.Fatal("New logger should not be nil")
	}
	t.Log("✓ New logger created successfully")
}

// TestLogManagerCreation 测试日志管理器创建
func TestLogManagerCreation(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	manager := NewLogManager(logDir, 100)
	if manager == nil {
		t.Fatal("LogManager should not be nil")
	}
	if manager.logDir != logDir {
		t.Errorf("Expected logDir %s, got %s", logDir, manager.logDir)
	}
	if manager.totalSizeCap != 100*1024*1024 {
		t.Errorf("Expected totalSizeCap %d, got %d", 100*1024*1024, manager.totalSizeCap)
	}
	t.Log("✓ LogManager created correctly")
}

// TestLogManagerStartStop 测试日志管理器启动停止
func TestLogManagerStartStop(t *testing.T) {
	manager := NewLogManager(filepath.Join(t.TempDir(), "logs"), 100)
	manager.Start()
	if manager.cancel == nil {
		t.Fatal("Start() did not install a cancel function")
	}
	manager.Stop()
	t.Log("✓ LogManager start/stop works correctly")
}

// TestLogManagerNoSizeCap 测试无大小限制的日志管理器
func TestLogManagerNoSizeCap(t *testing.T) {
	manager := NewLogManager(filepath.Join(t.TempDir(), "logs"), 0)
	manager.Start() // 应该不启动任何后台任务
	if manager.cancel != nil {
		t.Fatal("Start() should not create a cleanup loop when size cap is disabled")
	}
	manager.Stop()
	t.Log("✓ LogManager with no size cap works correctly")
}

// TestInitConsoleOnly 测试仅控制台输出
//
// An empty LogFile no longer means "console, unconditionally". It means
// "console when a human is watching" — and when stdout is a file or pipe the
// node takes over a rotating file instead, because nothing can bound a
// descriptor the shell owns. N42_LOG_STDOUT=1 asks for the old behaviour and is
// what this test pins; TestRedirectedStdoutIsRotated covers the other branch.
func TestInitConsoleOnly(t *testing.T) {
	resetLoggerGlobals(t)
	t.Setenv("N42_LOG_STDOUT", "1")

	nodeConfig := conf.NodeConfig{
		DataDir: t.TempDir(),
	}
	loggerConfig := conf.LoggerConfig{
		LogFile: "", // 空表示只输出到控制台
		Level:   "info",
		MaxSize: 100,
		Console: true,
	}

	Init(nodeConfig, loggerConfig)
	Info("Test console output")
	if logWriter != nil {
		t.Fatal("console-only init should not create a file writer")
	}
	t.Log("✓ Console-only logging works")
}

// TestInitWithFile 测试文件输出
func TestInitWithFile(t *testing.T) {
	resetLoggerGlobals(t)

	tmpDir := t.TempDir()
	nodeConfig := conf.NodeConfig{
		DataDir: tmpDir,
	}
	loggerConfig := conf.LoggerConfig{
		LogFile:    "test.log",
		Level:      "debug",
		MaxSize:    10,
		MaxBackups: 3,
		MaxAge:     1,
		Compress:   false,
		Console:    true,
		JSONFormat: true,
		LocalTime:  true,
	}

	Init(nodeConfig, loggerConfig)
	Info("Test file output")

	// 检查日志目录是否创建
	logDir := filepath.Join(tmpDir, "log")
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		t.Errorf("Log directory was not created: %s", logDir)
	}

	Close()
	logPath := filepath.Join(logDir, "test.log")
	if info, err := os.Stat(logPath); err != nil {
		t.Fatalf("Log file was not created: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("Log file should not be empty")
	}
	t.Log("✓ File logging works")
}

// TestLogOutput 测试各级别日志输出
func TestLogOutput(t *testing.T) {
	resetLoggerGlobals(t)

	tmpDir := t.TempDir()
	nodeConfig := conf.NodeConfig{
		DataDir: tmpDir,
	}
	loggerConfig := conf.LoggerConfig{
		LogFile:    "test.log",
		Level:      "trace",
		MaxSize:    10,
		Console:    false,
		JSONFormat: true,
	}

	Init(nodeConfig, loggerConfig)

	// 测试各级别日志
	Trace("trace message")
	Debug("debug message")
	Info("info message")
	Warn("warn message")
	Error("error message")

	// 测试格式化日志
	Tracef("trace %s", "formatted")
	Debugf("debug %s", "formatted")
	Infof("info %s", "formatted")
	Warnf("warn %s", "formatted")
	Errorf("error %s", "formatted")

	// 测试带上下文的日志
	Info("with context", "key1", "value1", "key2", 123)

	Close()
	logPath := filepath.Join(tmpDir, "log", "test.log")
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", logPath, err)
	}
	if len(content) == 0 {
		t.Fatal("expected log output file to contain data")
	}
	t.Log("✓ All log levels output correctly")
}

// TestLoggerWithContext 测试带上下文的日志器
func TestLoggerWithContext(t *testing.T) {
	log := New("module", "test", "version", "1.0")
	log.Info("test message", "extra", "data")
	t.Log("✓ Logger with context works")
}

// TestLogFileInfo 测试日志文件信息结构
func TestLogFileInfo(t *testing.T) {
	info := logFileInfo{
		path:    "/tmp/test.log",
		size:    1024,
		modTime: time.Now(),
	}

	if info.path != "/tmp/test.log" {
		t.Errorf("Expected path /tmp/test.log, got %s", info.path)
	}
	if info.size != 1024 {
		t.Errorf("Expected size 1024, got %d", info.size)
	}
	t.Log("✓ logFileInfo structure works correctly")
}

// TestCtxToArray 测试 Ctx 转换
func TestCtxToArray(t *testing.T) {
	ctx := Ctx{
		"key1": "value1",
		"key2": 123,
	}

	arr := ctx.toArray()
	if len(arr) != 4 { // 2 key-value pairs = 4 elements
		t.Errorf("Expected array length 4, got %d", len(arr))
	}
	t.Log("✓ Ctx.toArray works correctly")
}

// TestNormalizeOddLength 测试奇数长度上下文的规范化
func TestNormalizeOddLength(t *testing.T) {
	// 奇数长度应该被补齐
	ctx := []interface{}{"key1", "value1", "key2"}
	normalized := normalize(ctx)
	if len(normalized) != 4 {
		t.Errorf("Expected normalized length 4, got %d", len(normalized))
	}
	if normalized[3] != nil {
		t.Errorf("Expected last element to be nil, got %v", normalized[3])
	}
	t.Log("✓ normalize handles odd length correctly")
}

func TestLogManagerCleanupRemovesOldestFiles(t *testing.T) {
	resetLoggerGlobals(t)

	logDir := t.TempDir()
	manager := NewLogManager(logDir, 1)
	manager.totalSizeCap = 8

	oldest := filepath.Join(logDir, "oldest.log")
	middle := filepath.Join(logDir, "middle.log")
	newest := filepath.Join(logDir, "newest.log.gz")

	writeLogFixture := func(path string, size int, modTime time.Time) {
		t.Helper()
		data := make([]byte, size)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("Chtimes(%s) error = %v", path, err)
		}
	}

	baseTime := time.Now().Add(-time.Hour)
	writeLogFixture(oldest, 4, baseTime)
	writeLogFixture(middle, 3, baseTime.Add(time.Minute))
	writeLogFixture(newest, 2, baseTime.Add(2*time.Minute))

	manager.cleanup()

	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("expected oldest log file to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(middle); err != nil {
		t.Fatalf("expected middle log file to remain: %v", err)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatalf("expected newest log file to remain: %v", err)
	}
}

// BenchmarkLogInfo 基准测试 Info 日志
func BenchmarkLogInfo(b *testing.B) {
	tmpDir := b.TempDir()
	nodeConfig := conf.NodeConfig{
		DataDir: tmpDir,
	}
	loggerConfig := conf.LoggerConfig{
		LogFile:    "bench.log",
		Level:      "info",
		MaxSize:    100,
		Console:    false,
		JSONFormat: true,
	}
	Init(nodeConfig, loggerConfig)
	defer Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info("benchmark message", "iteration", i)
	}
}

// TestConfigCoercesUnboundedBackups pins the coercion that keeps an error storm
// from filling a server's disk. lumberjack reads MaxBackups==0 as "keep every
// backup forever", which the config used to advertise as a valid choice.
func TestConfigCoercesUnboundedBackups(t *testing.T) {
	for _, in := range []int{0, -1} {
		cfg := conf.LoggerConfig{MaxBackups: in}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(%d): %v", in, err)
		}
		if cfg.MaxBackups <= 0 {
			t.Fatalf("MaxBackups %d stayed unbounded after Validate (got %d)", in, cfg.MaxBackups)
		}
	}
	if def := conf.DefaultLoggerConfig(); def.TotalSizeCap <= 0 {
		t.Fatalf("default TotalSizeCap is unbounded (%d)", def.TotalSizeCap)
	}
}

// TestRedirectedStdoutIsRotated is the regression for the hole this fixes: with
// no LogFile configured the node wrote to stdout, operators redirected that to
// a file, and nothing could rotate a descriptor the shell owns — so the log was
// unbounded on exactly the deployment path IDC servers use. Under `go test`
// stdout is not a terminal, which is the case being covered.
func TestRedirectedStdoutIsRotated(t *testing.T) {
	if isTerminal(os.Stdout) {
		t.Skip("stdout is a terminal; the redirected path is what this covers")
	}
	resetLoggerGlobals(t)

	dir := t.TempDir()
	cfg := conf.DefaultLoggerConfig()
	cfg.MaxSize = 1 // MB
	cfg.MaxBackups = 2
	cfg.Compress = false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	Init(conf.NodeConfig{DataDir: dir}, cfg)

	// Write far more than MaxSize*(MaxBackups+1) would hold.
	line := string(make([]byte, 512))
	for i := 0; i < 40000; i++ {
		Info(line)
	}
	Close()

	logDir := filepath.Join(dir, "log")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("node did not take ownership of a log directory: %v", err)
	}
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	if total == 0 {
		t.Fatal("no log bytes were written")
	}
	// The synchronous bound is MaxSize*(1 current + MaxBackups); allow slack for
	// the file that is mid-rotation.
	limit := int64(cfg.MaxSize) * int64(cfg.MaxBackups+2) * 1024 * 1024
	if total > limit {
		t.Fatalf("log directory grew to %d bytes, above the %d byte bound", total, limit)
	}
	t.Logf("wrote ~20 MB, log directory bounded at %d bytes across %d files", total, len(entries))
}
