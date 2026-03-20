package api

import (
	"testing"
)

func TestInferContentType(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"", "text/html; charset=utf-8"},
		{"index.html", "text/html; charset=utf-8"},
		{"style.css", "text/css; charset=utf-8"},
		{"app.js", "application/javascript; charset=utf-8"},
		{"data.json", "application/json; charset=utf-8"},
		{"logo.png", "image/png"},
		{"photo.jpg", "image/jpeg"},
		{"icon.svg", "image/svg+xml"},
		{"favicon.ico", "image/x-icon"},
		{"font.woff2", "font/woff2"},
		{"readme.txt", "text/plain; charset=utf-8"},
		{"module.wasm", "application/wasm"},
		{"unknown.xyz", "application/octet-stream"},
		{"no-ext", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := inferContentType(tt.path)
			if got != tt.expected {
				t.Errorf("inferContentType(%q) = %q, want %q", tt.path, got, tt.expected)
			}
		})
	}
}

func TestBuildCalldata(t *testing.T) {
	tests := []struct {
		path     string
		expected int // expected length
	}{
		{"", 0},
		{"index.html", 10},
		{"assets/style.css", 16},
	}
	for _, tt := range tests {
		result := buildCalldata(tt.path)
		if len(result) != tt.expected {
			t.Errorf("buildCalldata(%q) len = %d, want %d", tt.path, len(result), tt.expected)
		}
	}
}
