// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package log

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/conf"
	prefixed "github.com/n42blockchain/N42/log/logrus-prefixed-formatter"
	"github.com/sirupsen/logrus"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

var (
	root = &logger{ctx: []interface{}{}, mapPool: sync.Pool{
		New: func() any {
			return map[string]interface{}{}
		},
	}}
	terminal = logrus.New()

	// logManager 管理日志清理
	logManager *LogManager
)

type Lvl int

const skipLevel = 3

const (
	LvlCrit Lvl = iota
	LvlFatal
	LvlError
	LvlWarn
	LvlInfo
	LvlDebug
	LvlTrace
)

// LogManager 管理日志文件的自动清理
type LogManager struct {
	logDir       string
	totalSizeCap int64 // 字节
	checkInterval time.Duration
	cancel       context.CancelFunc
	mu           sync.Mutex
}

// NewLogManager 创建日志管理器
func NewLogManager(logDir string, totalSizeCapMB int) *LogManager {
	return &LogManager{
		logDir:        logDir,
		totalSizeCap:  int64(totalSizeCapMB) * 1024 * 1024,
		checkInterval: 1 * time.Hour, // 每小时检查一次
	}
}

// Start 启动后台清理任务
func (m *LogManager) Start() {
	if m.totalSizeCap <= 0 {
		return // 不限制总大小，不需要启动清理任务
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	go func() {
		ticker := time.NewTicker(m.checkInterval)
		defer ticker.Stop()

		// 启动时先检查一次
		m.cleanup()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.cleanup()
			}
		}
	}()
}

// Stop 停止后台清理任务
func (m *LogManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

// cleanup 执行清理
func (m *LogManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 获取所有日志文件
	files, err := m.getLogFiles()
	if err != nil {
		return
	}

	// 计算总大小
	var totalSize int64
	for _, f := range files {
		totalSize += f.size
	}

	// 如果超过限制，删除最旧的文件
	for totalSize > m.totalSizeCap && len(files) > 1 {
		oldest := files[0]
		if err := os.Remove(oldest.path); err == nil {
			totalSize -= oldest.size
			files = files[1:]
			Info("Log cleanup: removed old file", "file", filepath.Base(oldest.path), "size_mb", oldest.size/1024/1024)
		}
	}
}

type logFileInfo struct {
	path    string
	size    int64
	modTime time.Time
}

func (m *LogManager) getLogFiles() ([]logFileInfo, error) {
	var files []logFileInfo

	err := filepath.Walk(m.logDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 忽略错误，继续遍历
		}
		if info.IsDir() {
			return nil
		}
		// 只处理 .log 和 .log.gz 文件
		ext := filepath.Ext(path)
		if ext == ".log" || ext == ".gz" {
			files = append(files, logFileInfo{
				path:    path,
				size:    info.Size(),
				modTime: info.ModTime(),
			})
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// 按修改时间排序（最旧的在前）
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	return files, nil
}

// Init 初始化日志系统
//
// 日志策略：
//   - 当 LogFile 为空时，仅输出到控制台
//   - 当 LogFile 不为空时，输出到文件（可配置是否同时输出到控制台）
//   - 日志文件自动按大小切分、按数量/时间清理、可选压缩
func Init(nodeConfig conf.NodeConfig, config conf.LoggerConfig) {
	// 验证配置
	_ = config.Validate()

	// 设置控制台输出格式
	formatter := new(prefixed.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05"
	formatter.FullTimestamp = true
	formatter.DisableColors = false

	// 设置日志级别
	lvl, _ := logrus.ParseLevel(config.Level)

	// 如果没有指定日志文件，只输出到控制台
	if config.LogFile == "" {
		terminal.SetFormatter(formatter)
		terminal.SetLevel(lvl)
		terminal.SetOutput(os.Stdout)
		return
	}

	// 创建日志目录
	logDir := filepath.Join(nodeConfig.DataDir, "log")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create log directory: %v\n", err)
		return
	}

	// 配置文件输出
	logPath := filepath.Join(logDir, config.LogFile)

	// 创建 lumberjack 日志轮转器
	lj := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    config.MaxSize,    // MB
		MaxBackups: config.MaxBackups,
		MaxAge:     config.MaxAge,     // 天
		Compress:   config.Compress,
		LocalTime:  config.LocalTime,
	}

	// 设置文件输出格式
	var fileFormatter logrus.Formatter
	if config.JSONFormat {
		jsonFormatter := new(logrus.JSONFormatter)
		jsonFormatter.TimestampFormat = "2006-01-02 15:04:05"
		fileFormatter = jsonFormatter
	} else {
		textFormatter := new(prefixed.TextFormatter)
		textFormatter.TimestampFormat = "2006-01-02 15:04:05"
		textFormatter.FullTimestamp = true
		textFormatter.DisableColors = true // 文件中不使用颜色
		fileFormatter = textFormatter
	}

	terminal.SetFormatter(fileFormatter)
	terminal.SetLevel(lvl)

	// 设置输出目标
	if config.Console {
		// 同时输出到文件和控制台
		terminal.SetOutput(io.MultiWriter(lj, os.Stdout))
	} else {
		// 仅输出到文件
		terminal.SetOutput(lj)
	}

	// 启动日志管理器（如果设置了总大小限制）
	if config.TotalSizeCap > 0 {
		logManager = NewLogManager(logDir, config.TotalSizeCap)
		logManager.Start()
	}

	// 打印日志配置信息
	Info("Logger initialized",
		"file", logPath,
		"level", config.Level,
		"max_size_mb", config.MaxSize,
		"max_backups", config.MaxBackups,
		"max_age_days", config.MaxAge,
		"compress", config.Compress,
		"total_size_cap_mb", config.TotalSizeCap,
	)
}

// Close 关闭日志系统，停止后台任务
func Close() {
	if logManager != nil {
		logManager.Stop()
	}
}

func InitMobileLogger(filepath string, isDebug bool) {
	if !isDebug {
		return
	}
	formatter := new(prefixed.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05"
	formatter.FullTimestamp = true
	formatter.DisableColors = false
	terminal.SetFormatter(formatter)
	if isDebug {
		terminal.SetLevel(logrus.DebugLevel)
	} else {
		terminal.SetLevel(logrus.InfoLevel)
	}
	terminal.SetOutput(&lumberjack.Logger{
		Filename:   filepath,
		MaxSize:    10, //10MB
		MaxBackups: 2,
		LocalTime:  false,
		Compress:   false,
	})
}

// New returns a new logger with the given context.
// New is a convenient alias for Root().New
func New(ctx ...interface{}) Logger {
	return root.New(ctx...)
}

// Root returns the root logger
func Root() Logger {
	return root
}

// Trace is a convenient alias for Root().Trace
func Trace(msg string, ctx ...interface{}) {
	root.write(msg, LvlTrace, ctx, skipLevel)
}

func Tracef(msg string, ctx ...interface{}) {
	root.write(fmt.Sprintf(msg, ctx...), LvlTrace, []interface{}{}, skipLevel)
}

// Debug is a convenient alias for Root().Debug
func Debug(msg string, ctx ...interface{}) {
	root.write(msg, LvlDebug, ctx, skipLevel)
}

func Debugf(msg string, ctx ...interface{}) {
	root.write(fmt.Sprintf(msg, ctx...), LvlDebug, []interface{}{}, skipLevel)
}

// Info is a convenient alias for Root().Info
func Info(msg string, ctx ...interface{}) {
	root.write(msg, LvlInfo, ctx, skipLevel)
}

// Infof is a convenient alias for Root().Info
func Infof(msg string, ctx ...interface{}) {
	root.write(fmt.Sprintf(msg, ctx...), LvlInfo, []interface{}{}, skipLevel)
}

// Warn is a convenient alias for Root().Warn
func Warn(msg string, ctx ...interface{}) {
	root.write(msg, LvlWarn, ctx, skipLevel)
}

// Warnf is a convenient alias for Root().Warn
func Warnf(msg string, ctx ...interface{}) {
	root.write(fmt.Sprintf(msg, ctx...), LvlWarn, []interface{}{}, skipLevel)
}

// Error is a convenient alias for Root().Error
func Error(msg string, ctx ...interface{}) {
	root.write(msg, LvlError, ctx, skipLevel)
}

// Errorf is a convenient alias for Root().Error
func Errorf(msg string, ctx ...interface{}) {
	root.write(fmt.Sprintf(msg, ctx...), LvlError, []interface{}{}, skipLevel)
}

// Crit is a convenient alias for Root().Crit
func Crit(msg string, ctx ...interface{}) {
	root.write(msg, LvlCrit, ctx, skipLevel)
	os.Exit(1)
}

// Critf is a convenient alias for Root().Crit
func Critf(msg string, ctx ...interface{}) {
	root.write(fmt.Sprintf(msg, ctx...), LvlCrit, []interface{}{}, skipLevel)
	os.Exit(1)
}

// A Logger writes key/value pairs to a Handler
type Logger interface {
	// New returns a new Logger that has this logger's context plus the given context
	New(ctx ...interface{}) Logger

	// Log a message at the given level with context key/value pairs
	Trace(msg string, ctx ...interface{})
	Debug(msg string, ctx ...interface{})
	Info(msg string, ctx ...interface{})
	Warn(msg string, ctx ...interface{})
	Error(msg string, ctx ...interface{})
	Crit(msg string, ctx ...interface{})
}

// TerminalStringer is an analogous interface to the stdlib stringer, allowing
// own types to have custom shortened serialization formats when printed to the
// screen.
type TerminalStringer interface {
	TerminalString() string
}
