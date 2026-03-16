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

package evmsdk

import (
	"encoding/json"
	"io"
	"net/http"
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
