package jsonrpc

import (
	"testing"

	mapset "github.com/deckarep/golang-set"
)

func TestParseOriginURL(t *testing.T) {
	scheme, host, port, err := parseOriginURL("https://example.com:8080")
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if scheme != "https" || host != "example.com" || port != "8080" {
		t.Errorf("got %q %q %q", scheme, host, port)
	}

	// bare hostname, no scheme
	scheme2, host2, _, err2 := parseOriginURL("example.com")
	if err2 != nil {
		t.Fatalf("unexpected err %v", err2)
	}
	if scheme2 != "" || host2 != "example.com" {
		t.Errorf("got %q %q", scheme2, host2)
	}
}

func TestRuleAllowsOrigin(t *testing.T) {
	if !ruleAllowsOrigin("https://example.com", "https://example.com") {
		t.Error("expected exact match to be allowed")
	}
	if ruleAllowsOrigin("https://example.com", "https://other.com") {
		t.Error("expected mismatch to be rejected")
	}
	if !ruleAllowsOrigin("example.com", "example.com") {
		t.Error("expected bare hostname match")
	}
	if ruleAllowsOrigin("https://example.com:8080", "https://example.com:9090") {
		t.Error("expected port mismatch to be rejected")
	}
}

func TestOriginIsAllowed(t *testing.T) {
	set := mapset.NewSet()
	set.Add("https://allowed.com")
	if !originIsAllowed(set, "https://allowed.com") {
		t.Error("expected allowed origin to pass")
	}
	if originIsAllowed(set, "https://denied.com") {
		t.Error("expected unlisted origin to fail")
	}
}
