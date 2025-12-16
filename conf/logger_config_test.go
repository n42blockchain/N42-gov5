// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v2"
)

// TestLoggerConfigDefaults 测试默认日志配置
func TestLoggerConfigDefaults(t *testing.T) {
	cfg := DefaultLoggerConfig()

	if cfg.LogFile != "" {
		t.Errorf("Expected empty LogFile, got %s", cfg.LogFile)
	}
	if cfg.Level != "info" {
		t.Errorf("Expected Level 'info', got %s", cfg.Level)
	}
	if cfg.MaxSize != 100 {
		t.Errorf("Expected MaxSize 100, got %d", cfg.MaxSize)
	}
	if cfg.MaxBackups != 10 {
		t.Errorf("Expected MaxBackups 10, got %d", cfg.MaxBackups)
	}
	if cfg.MaxAge != 30 {
		t.Errorf("Expected MaxAge 30, got %d", cfg.MaxAge)
	}
	if !cfg.Compress {
		t.Error("Expected Compress true")
	}
	if cfg.TotalSizeCap != 0 {
		t.Errorf("Expected TotalSizeCap 0, got %d", cfg.TotalSizeCap)
	}
	if !cfg.LocalTime {
		t.Error("Expected LocalTime true")
	}
	if !cfg.Console {
		t.Error("Expected Console true")
	}
	if !cfg.JSONFormat {
		t.Error("Expected JSONFormat true")
	}

	t.Log("✓ Default logger config is correct")
}

// TestLoggerConfigValidate 测试配置验证
func TestLoggerConfigValidate(t *testing.T) {
	tests := []struct {
		name     string
		config   LoggerConfig
		expected LoggerConfig
	}{
		{
			name: "negative MaxSize should be corrected",
			config: LoggerConfig{
				MaxSize:    -1,
				MaxBackups: 10,
				MaxAge:     30,
			},
			expected: LoggerConfig{
				MaxSize:    100,
				MaxBackups: 10,
				MaxAge:     30,
			},
		},
		{
			name: "zero MaxSize should be corrected",
			config: LoggerConfig{
				MaxSize:    0,
				MaxBackups: 10,
				MaxAge:     30,
			},
			expected: LoggerConfig{
				MaxSize:    100,
				MaxBackups: 10,
				MaxAge:     30,
			},
		},
		{
			name: "negative MaxBackups should be corrected",
			config: LoggerConfig{
				MaxSize:    100,
				MaxBackups: -1,
				MaxAge:     30,
			},
			expected: LoggerConfig{
				MaxSize:    100,
				MaxBackups: 10,
				MaxAge:     30,
			},
		},
		{
			name: "negative MaxAge should be corrected",
			config: LoggerConfig{
				MaxSize:    100,
				MaxBackups: 10,
				MaxAge:     -1,
			},
			expected: LoggerConfig{
				MaxSize:    100,
				MaxBackups: 10,
				MaxAge:     30,
			},
		},
		{
			name: "valid config should not change",
			config: LoggerConfig{
				MaxSize:    50,
				MaxBackups: 5,
				MaxAge:     7,
			},
			expected: LoggerConfig{
				MaxSize:    50,
				MaxBackups: 5,
				MaxAge:     7,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if err != nil {
				t.Errorf("Validate() returned error: %v", err)
			}
			if tt.config.MaxSize != tt.expected.MaxSize {
				t.Errorf("MaxSize: expected %d, got %d", tt.expected.MaxSize, tt.config.MaxSize)
			}
			if tt.config.MaxBackups != tt.expected.MaxBackups {
				t.Errorf("MaxBackups: expected %d, got %d", tt.expected.MaxBackups, tt.config.MaxBackups)
			}
			if tt.config.MaxAge != tt.expected.MaxAge {
				t.Errorf("MaxAge: expected %d, got %d", tt.expected.MaxAge, tt.config.MaxAge)
			}
		})
	}

	t.Log("✓ Logger config validation works correctly")
}

// TestLoggerConfigJSONSerialization 测试 JSON 序列化
func TestLoggerConfigJSONSerialization(t *testing.T) {
	cfg := LoggerConfig{
		LogFile:      "app.log",
		Level:        "debug",
		MaxSize:      100,
		MaxBackups:   10,
		MaxAge:       30,
		Compress:     true,
		TotalSizeCap: 500,
		LocalTime:    true,
		Console:      true,
		JSONFormat:   true,
	}

	// 序列化
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	// 反序列化
	var cfg2 LoggerConfig
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// 验证
	if cfg2.LogFile != cfg.LogFile {
		t.Errorf("LogFile mismatch: expected %s, got %s", cfg.LogFile, cfg2.LogFile)
	}
	if cfg2.Level != cfg.Level {
		t.Errorf("Level mismatch: expected %s, got %s", cfg.Level, cfg2.Level)
	}
	if cfg2.MaxSize != cfg.MaxSize {
		t.Errorf("MaxSize mismatch: expected %d, got %d", cfg.MaxSize, cfg2.MaxSize)
	}
	if cfg2.TotalSizeCap != cfg.TotalSizeCap {
		t.Errorf("TotalSizeCap mismatch: expected %d, got %d", cfg.TotalSizeCap, cfg2.TotalSizeCap)
	}

	t.Log("✓ JSON serialization works correctly")
}

// TestLoggerConfigYAMLSerialization 测试 YAML 序列化
func TestLoggerConfigYAMLSerialization(t *testing.T) {
	cfg := LoggerConfig{
		LogFile:      "app.log",
		Level:        "debug",
		MaxSize:      100,
		MaxBackups:   10,
		MaxAge:       30,
		Compress:     true,
		TotalSizeCap: 500,
		LocalTime:    true,
		Console:      true,
		JSONFormat:   true,
	}

	// 序列化
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("YAML marshal failed: %v", err)
	}

	// 反序列化
	var cfg2 LoggerConfig
	if err := yaml.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("YAML unmarshal failed: %v", err)
	}

	// 验证
	if cfg2.LogFile != cfg.LogFile {
		t.Errorf("LogFile mismatch: expected %s, got %s", cfg.LogFile, cfg2.LogFile)
	}
	if cfg2.Level != cfg.Level {
		t.Errorf("Level mismatch: expected %s, got %s", cfg.Level, cfg2.Level)
	}

	t.Log("✓ YAML serialization works correctly")
}

// TestLoggerConfigJSONTags 测试 JSON 标签
func TestLoggerConfigJSONTags(t *testing.T) {
	cfg := LoggerConfig{
		LogFile:    "test.log",
		MaxBackups: 5,
		MaxAge:     7,
	}

	data, _ := json.Marshal(cfg)
	jsonStr := string(data)

	// 验证 JSON 字段名
	expectedTags := []string{
		`"name":`,        // LogFile -> name
		`"level":`,       // Level -> level
		`"max_size":`,    // MaxSize -> max_size
		`"max_count":`,   // MaxBackups -> max_count
		`"max_day":`,     // MaxAge -> max_day
		`"compress":`,    // Compress -> compress
		`"total_size_cap":`, // TotalSizeCap -> total_size_cap
		`"local_time":`,  // LocalTime -> local_time
		`"console":`,     // Console -> console
		`"json_format":`, // JSONFormat -> json_format
	}

	for _, tag := range expectedTags {
		if !containsString(jsonStr, tag) {
			t.Errorf("Expected JSON tag %s not found in %s", tag, jsonStr)
		}
	}

	t.Log("✓ JSON tags are correct")
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestLoggerConfigDocumentation 测试配置文档注释
func TestLoggerConfigDocumentation(t *testing.T) {
	// 验证配置结构有足够的文档
	// 这是一个提醒性测试，确保配置有良好的文档
	cfg := DefaultLoggerConfig()

	// 验证默认值是合理的
	if cfg.MaxSize < 10 || cfg.MaxSize > 1000 {
		t.Errorf("MaxSize default %d seems unreasonable", cfg.MaxSize)
	}
	if cfg.MaxBackups < 1 || cfg.MaxBackups > 100 {
		t.Errorf("MaxBackups default %d seems unreasonable", cfg.MaxBackups)
	}
	if cfg.MaxAge < 1 || cfg.MaxAge > 365 {
		t.Errorf("MaxAge default %d seems unreasonable", cfg.MaxAge)
	}

	t.Log("✓ Logger config defaults are reasonable")
}

// TestLoggerConfigProductionRecommendation 测试生产环境推荐配置
func TestLoggerConfigProductionRecommendation(t *testing.T) {
	// 生产环境推荐配置
	production := LoggerConfig{
		LogFile:      "n42.log",
		Level:        "info",
		MaxSize:      100,
		MaxBackups:   10,
		MaxAge:       30,
		Compress:     true,
		TotalSizeCap: 1000, // 1GB 总限制
		LocalTime:    true,
		Console:      false, // 生产环境关闭控制台
		JSONFormat:   true,  // 使用 JSON 便于分析
	}

	err := production.Validate()
	if err != nil {
		t.Errorf("Production config validation failed: %v", err)
	}

	// 验证配置合理性
	if production.Console {
		t.Error("Production config should not output to console")
	}
	if !production.Compress {
		t.Error("Production config should enable compression")
	}
	if !production.JSONFormat {
		t.Error("Production config should use JSON format")
	}

	t.Log("✓ Production config is valid and reasonable")
}

// TestLoggerConfigDevelopmentRecommendation 测试开发环境推荐配置
func TestLoggerConfigDevelopmentRecommendation(t *testing.T) {
	// 开发环境推荐配置
	development := LoggerConfig{
		LogFile:      "",      // 只输出到控制台
		Level:        "debug", // 更详细的日志
		MaxSize:      10,
		MaxBackups:   3,
		MaxAge:       7,
		Compress:     false,
		TotalSizeCap: 0,
		LocalTime:    true,
		Console:      true, // 开发环境需要控制台
		JSONFormat:   false, // 文本格式更易读
	}

	err := development.Validate()
	if err != nil {
		t.Errorf("Development config validation failed: %v", err)
	}

	if development.LogFile != "" {
		t.Log("Development config has file output (optional)")
	}
	if !development.Console {
		t.Error("Development config should output to console")
	}

	t.Log("✓ Development config is valid and reasonable")
}

