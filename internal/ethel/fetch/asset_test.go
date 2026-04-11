// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import "testing"

func TestAssetValidate(t *testing.T) {
	good := Asset{
		Name:      "leaves.cdat",
		SizeBytes: 1024,
		Sources:   []Source{{Kind: SourceHTTPS, URI: "https://example.com/x"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good asset: unexpected err: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*Asset)
		want string
	}{
		{"empty name", func(a *Asset) { a.Name = "" }, "Name is required"},
		{"zero size", func(a *Asset) { a.SizeBytes = 0 }, "SizeBytes is required"},
		{"no sources", func(a *Asset) { a.Sources = nil }, "at least one Source"},
		{"path traversal dotdot", func(a *Asset) { a.Name = "x/../../etc/passwd" }, "path traversal"},
		{"absolute unix", func(a *Asset) { a.Name = "/etc/passwd" }, "path traversal"},
		{"absolute windows", func(a *Asset) { a.Name = "C:/secrets" }, "path traversal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := good
			a.Sources = append([]Source{}, good.Sources...)
			c.mut(&a)
			err := a.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), c.want)
			}
		})
	}
}

func TestAssetSourcesByKind(t *testing.T) {
	a := Asset{
		Sources: []Source{
			{Kind: SourceHTTPS, URI: "https://a"},
			{Kind: SourceBT, URI: "magnet:1"},
			{Kind: SourceHTTPS, URI: "https://b"},
		},
	}
	https := a.SourcesByKind(SourceHTTPS)
	if len(https) != 2 || https[0].URI != "https://a" || https[1].URI != "https://b" {
		t.Fatalf("SourcesByKind https: %+v", https)
	}
	if !a.HasSourceKind(SourceBT) {
		t.Fatalf("HasSourceKind(BT): expected true")
	}
	if a.HasSourceKind(SourceWebRTC) {
		t.Fatalf("HasSourceKind(WebRTC): expected false")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
