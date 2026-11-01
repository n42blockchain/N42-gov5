// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func useTestEngine(t *testing.T) *EvmEngine {
	t.Helper()

	old := EE
	EE = &EvmEngine{AppBasePath: t.TempDir()}
	t.Cleanup(func() {
		EE = old
	})
	return EE
}

func TestGetNetInfosUsesDefaultClient(t *testing.T) {
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://www.baidu.com" {
				t.Fatalf("GetNetInfos() requested %s", req.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("stubbed-html")),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() {
		http.DefaultClient = oldClient
	})

	if got := GetNetInfos(); got != "stubbed-html" {
		t.Fatalf("GetNetInfos() = %q, want %q", got, "stubbed-html")
	}
}

func TestTouchFileRoundTrip(t *testing.T) {
	engine := useTestEngine(t)

	touchedPath := TouchFile()
	if filepath.Dir(touchedPath) != engine.AppBasePath {
		t.Fatalf("TouchFile() path = %s, want under %s", touchedPath, engine.AppBasePath)
	}

	content := ReadTouchedFile()
	if !strings.Contains(content, "NAME=wux") {
		t.Fatalf("ReadTouchedFile() missing expected marker: %q", content)
	}
}

func TestEmitRejectsInvalidJSON(t *testing.T) {
	resp := Emit(`{"type":`)

	var decoded EmitResponse
	if err := json.Unmarshal([]byte(resp), &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.Code == 0 {
		t.Fatalf("Emit() code = %d, want non-zero for invalid JSON", decoded.Code)
	}
	if decoded.Message == "" {
		t.Fatal("Emit() should return a parse error message")
	}
}

func TestEmitBlsSignDispatch(t *testing.T) {
	useTestEngine(t)

	const (
		privKey = "202cbf36864a88e348c8f573aa0bc79f5a7119e58251c3580bb08af70cb2dfed"
		msg     = "8ac7230489e80000"
	)
	want, err := BlsSign(privKey, msg)
	if err != nil {
		t.Fatalf("BlsSign() error = %v", err)
	}

	resp := Emit(`{"type":"blssign","val":{"priv_key":"` + privKey + `","msg":"` + msg + `"}}`)

	var decoded EmitResponse
	if err := json.Unmarshal([]byte(resp), &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.Code != 0 {
		t.Fatalf("Emit() code = %d, message = %q", decoded.Code, decoded.Message)
	}

	got, ok := decoded.Data.(string)
	if !ok {
		t.Fatalf("Emit() data type = %T, want string", decoded.Data)
	}
	if got != want {
		t.Fatalf("Emit() data = %q, want %q", got, want)
	}
}

// captureOutput returns what fn writes to stdout and stderr.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
	}()
	fn()
	w.Close()
	return <-done
}

// The Emit log names the request fields but never prints their values,
// which hold private keys and plaintexts.
func TestEmitDoesNotLogRequestValues(t *testing.T) {
	const secret = "202cbf36864a88e348c8f573aa0bc79f5a7119e58251c3580bb08af70cb2dfed"
	for _, typ := range []string{"setting", "blssign", "blspubk", "decrypt", "encrypt"} {
		t.Run(typ, func(t *testing.T) {
			useTestEngine(t)
			out := captureOutput(t, func() {
				Emit(`{"type":"` + typ + `","val":{"priv_key":"` + secret + `","msg":"` + secret + `"}}`)
			})
			if strings.Contains(out, secret) {
				t.Fatalf("log leaks a request value: %q", out)
			}
			if !strings.Contains(out, "fields=[msg,priv_key]") {
				t.Fatalf("log does not name the request fields: %q", out)
			}
		})
	}
}
