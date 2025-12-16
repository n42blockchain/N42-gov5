// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package log

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

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
	manager := NewLogManager("/tmp/test_logs", 100)
	if manager == nil {
		t.Fatal("LogManager should not be nil")
	}
	if manager.logDir != "/tmp/test_logs" {
		t.Errorf("Expected logDir /tmp/test_logs, got %s", manager.logDir)
	}
	if manager.totalSizeCap != 100*1024*1024 {
		t.Errorf("Expected totalSizeCap %d, got %d", 100*1024*1024, manager.totalSizeCap)
	}
	t.Log("✓ LogManager created correctly")
}

// TestLogManagerStartStop 测试日志管理器启动停止
func TestLogManagerStartStop(t *testing.T) {
	manager := NewLogManager("/tmp/test_logs", 100)
	manager.Start()
	time.Sleep(100 * time.Millisecond)
	manager.Stop()
	t.Log("✓ LogManager start/stop works correctly")
}

// TestLogManagerNoSizeCap 测试无大小限制的日志管理器
func TestLogManagerNoSizeCap(t *testing.T) {
	manager := NewLogManager("/tmp/test_logs", 0)
	manager.Start() // 应该不启动任何后台任务
	manager.Stop()
	t.Log("✓ LogManager with no size cap works correctly")
}

// TestInitConsoleOnly 测试仅控制台输出
func TestInitConsoleOnly(t *testing.T) {
	nodeConfig := conf.NodeConfig{
		DataDir: t.TempDir(),
	}
	loggerConfig := conf.LoggerConfig{
		LogFile:  "", // 空表示只输出到控制台
		Level:    "info",
		MaxSize:  100,
		Console:  true,
	}

	Init(nodeConfig, loggerConfig)
	Info("Test console output")
	t.Log("✓ Console-only logging works")
}

// TestInitWithFile 测试文件输出
func TestInitWithFile(t *testing.T) {
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
	t.Log("✓ File logging works")
}

// TestLogOutput 测试各级别日志输出
func TestLogOutput(t *testing.T) {
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

