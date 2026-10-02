package abi

import "testing"

func TestParseSelectorErrors(t *testing.T) {
	cases := []string{
		"",                // empty identifier
		"1abc(uint256)",   // invalid identifier start
		"foo",             // missing parens entirely
		"foo(uint256",     // unterminated composite type
		"foo(uint256))",   // trailing garbage after the closing paren
		"foo(uint256,)",   // dangling comma / empty type after comma
		"foo(uint256[)",   // malformed array type: missing ']'
		"foo(uint256[a])", // malformed array type: non-digit inside brackets
	}
	for _, in := range cases {
		if _, err := ParseSelector(in); err == nil {
			t.Errorf("ParseSelector(%q) expected an error, got none", in)
		}
	}
}

func TestParseElementaryTypeErrors(t *testing.T) {
	if _, _, err := parseElementaryType(""); err == nil {
		t.Error("expected error for empty input")
	}
	if _, _, err := parseElementaryType("1uint256"); err == nil {
		t.Error("expected error for an identifier that doesn't start with a letter")
	}
	if _, _, err := parseElementaryType("uint256["); err == nil {
		t.Error("expected error for an unterminated array suffix")
	}
	if _, _, err := parseElementaryType("uint256[a]"); err == nil {
		t.Error("expected error for a non-digit, non-']' character inside brackets")
	}
}

func TestParseCompositeTypeErrors(t *testing.T) {
	if _, _, err := parseCompositeType(""); err == nil {
		t.Error("expected error for empty input")
	}
	if _, _, err := parseCompositeType("uint256)"); err == nil {
		t.Error("expected error when input doesn't start with '('")
	}
	if _, _, err := parseCompositeType("(uint256"); err == nil {
		t.Error("expected error for an unterminated tuple")
	}
	if _, _, err := parseCompositeType("()"); err == nil {
		t.Error("expected error for an empty tuple (no element type)")
	}
}

func TestParseTypeEmptyInput(t *testing.T) {
	if _, _, err := parseType(""); err == nil {
		t.Error("expected error for empty type string")
	}
}

func TestAssembleArgsUnexpectedType(t *testing.T) {
	if _, err := assembleArgs([]interface{}{42}); err == nil {
		t.Error("expected error for an unsupported argument element type")
	}
}
