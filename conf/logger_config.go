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

package conf

// LoggerConfig 定义日志配置
//
// 日志轮转策略：
//   - 当单个文件大小超过 MaxSize MB 时，自动切分到新文件
//   - 旧日志文件会被重命名为 name-timestamp.ext 格式
//   - 超过 MaxBackups 数量或 MaxAge 天数的旧文件会被自动删除
//   - 启用 Compress 后，旧文件会被压缩为 .gz 格式以节省空间
//
// 推荐配置：
//   - 生产环境: MaxSize=100, MaxBackups=10, MaxAge=30, Compress=true
//   - 开发环境: MaxSize=10, MaxBackups=3, MaxAge=7, Compress=false
//   - 磁盘紧张: MaxSize=50, MaxBackups=5, MaxAge=7, Compress=true, TotalSizeCap=500
type LoggerConfig struct {
	// LogFile 日志文件名 (留空则只输出到控制台)
	// 相对路径会自动放到 DataDir/log/ 目录下
	LogFile string `json:"name" yaml:"name"`

	// Level 日志级别: trace, debug, info, warn, error, fatal
	Level string `json:"level" yaml:"level"`

	// MaxSize 单个日志文件最大大小 (MB)
	// 超过此大小会自动切分到新文件
	// 默认: 100 MB
	MaxSize int `json:"max_size" yaml:"max_size"`

	// MaxBackups 保留的旧日志文件数量
	// 0 表示不限制数量 (但仍受 MaxAge 限制)
	// 默认: 10
	MaxBackups int `json:"max_count" yaml:"max_count"`

	// MaxAge 日志文件保留天数
	// 超过此天数的文件会被自动删除
	// 0 表示不按时间删除 (但仍受 MaxBackups 限制)
	// 默认: 30 天
	MaxAge int `json:"max_day" yaml:"max_day"`

	// Compress 是否压缩旧日志文件
	// 启用后旧文件会被压缩为 .gz 格式，节省约 90% 空间
	// 默认: true
	Compress bool `json:"compress" yaml:"compress"`

	// TotalSizeCap 日志文件总大小上限 (MB)
	// 当所有日志文件总大小超过此限制时，最旧的文件会被删除
	// 0 表示不限制 (使用 MaxBackups 和 MaxAge 控制)
	// 默认: 0 (不限制)
	TotalSizeCap int `json:"total_size_cap" yaml:"total_size_cap"`

	// LocalTime 是否使用本地时间命名日志文件
	// false 使用 UTC 时间，true 使用本地时间
	// 默认: true
	LocalTime bool `json:"local_time" yaml:"local_time"`

	// Console 是否同时输出到控制台
	// 即使指定了 LogFile，仍然输出到控制台
	// 默认: true (开发时方便)
	Console bool `json:"console" yaml:"console"`

	// JSONFormat 是否使用 JSON 格式输出到文件
	// 控制台输出始终使用文本格式
	// 默认: true (便于日志收集和分析)
	JSONFormat bool `json:"json_format" yaml:"json_format"`
}

// DefaultLoggerConfig 返回默认日志配置
func DefaultLoggerConfig() LoggerConfig {
	return LoggerConfig{
		LogFile:      "",    // 默认只输出到控制台
		Level:        "info",
		MaxSize:      100,   // 100 MB
		MaxBackups:   10,
		MaxAge:       30,    // 30 天
		Compress:     true,
		TotalSizeCap: 0,     // 不限制总大小
		LocalTime:    true,
		Console:      true,
		JSONFormat:   true,
	}
}

// Validate 验证配置有效性
func (c *LoggerConfig) Validate() error {
	if c.MaxSize <= 0 {
		c.MaxSize = 100
	}
	if c.MaxBackups < 0 {
		c.MaxBackups = 10
	}
	if c.MaxAge < 0 {
		c.MaxAge = 30
	}
	return nil
}
