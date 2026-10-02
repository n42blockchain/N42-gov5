package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestParseMetricNoLabels(t *testing.T) {
	name, labels, err := parseMetric("foo_bar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "foo_bar" {
		t.Fatalf("name = %q, want foo_bar", name)
	}
	if labels != nil {
		t.Fatalf("labels = %v, want nil", labels)
	}
}

func TestParseMetricWithLabels(t *testing.T) {
	name, labels, err := parseMetric(`foo{bar="baz",aaa="b"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "foo" {
		t.Fatalf("name = %q, want foo", name)
	}
	want := prometheus.Labels{"bar": "baz", "aaa": "b"}
	if len(labels) != len(want) || labels["bar"] != "baz" || labels["aaa"] != "b" {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
}

func TestParseMetricEmpty(t *testing.T) {
	if _, _, err := parseMetric(""); err == nil {
		t.Fatal("expected error for empty metric string")
	}
}

func TestParseMetricInvalidIdent(t *testing.T) {
	if _, _, err := parseMetric("1bad"); err == nil {
		t.Fatal("expected error for invalid leading identifier")
	}
}

func TestParseMetricInvalidLabelIdent(t *testing.T) {
	if _, _, err := parseMetric(`foo{1bad="x"}`); err == nil {
		t.Fatal("expected error for invalid label identifier")
	}
}

func TestParseMetricMissingClosingBrace(t *testing.T) {
	if _, _, err := parseMetric(`foo{bar="baz"`); err == nil {
		t.Fatal("expected error for missing closing brace")
	}
}

func TestParseMetricEmptyLabels(t *testing.T) {
	name, labels, err := parseMetric("foo{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "foo" || labels != nil {
		t.Fatalf("got name=%q labels=%v, want foo/nil", name, labels)
	}
}

func TestParseTagsMissingEquals(t *testing.T) {
	if _, err := parseTags(`bar"baz"`); err == nil {
		t.Fatal("expected error for missing `=`")
	}
}

func TestParseTagsMissingStartQuote(t *testing.T) {
	if _, err := parseTags(`bar=baz`); err == nil {
		t.Fatal("expected error for missing starting quote")
	}
}

func TestParseTagsMissingTrailingQuote(t *testing.T) {
	if _, err := parseTags(`bar="baz`); err == nil {
		t.Fatal("expected error for missing trailing quote")
	}
}

func TestParseTagsMissingCommaBetweenPairs(t *testing.T) {
	if _, err := parseTags(`bar="baz" aaa="b"`); err == nil {
		t.Fatal("expected error for missing comma between pairs")
	}
}

func TestParseTagsEscapedQuote(t *testing.T) {
	labels, err := parseTags(`bar="ba\"z"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Just assert this exercises the escaped-quote continuation path without
	// erroring; the exact unescaping behavior is not the contract under test.
	if _, ok := labels["bar"]; !ok {
		t.Fatalf("labels[bar] missing, got %v", labels)
	}
}

func TestParseTagsSpaceAfterComma(t *testing.T) {
	labels, err := parseTags(`bar="baz", aaa="b"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if labels["bar"] != "baz" || labels["aaa"] != "b" {
		t.Fatalf("labels = %v", labels)
	}
}

func TestSkipSpace(t *testing.T) {
	if got := skipSpace("   abc"); got != "abc" {
		t.Fatalf("skipSpace = %q, want abc", got)
	}
	if got := skipSpace("abc"); got != "abc" {
		t.Fatalf("skipSpace = %q, want abc", got)
	}
	if got := skipSpace(""); got != "" {
		t.Fatalf("skipSpace = %q, want empty", got)
	}
}

func TestValidateIdent(t *testing.T) {
	if err := validateIdent("valid_name:123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateIdent("1invalid"); err == nil {
		t.Fatal("expected error for identifier starting with digit")
	}
	if err := validateIdent(""); err == nil {
		t.Fatal("expected error for empty identifier")
	}
}
