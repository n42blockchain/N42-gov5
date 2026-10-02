package abi

import (
	"testing"
)

func TestParseSelectorSimple(t *testing.T) {
	sel, err := ParseSelector("foo()")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if sel.Name != "foo" || sel.Type != "function" || len(sel.Inputs) != 0 {
		t.Errorf("got %+v", sel)
	}
}

func TestParseSelectorSingleArg(t *testing.T) {
	sel, err := ParseSelector("bar(uint256)")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if sel.Name != "bar" || len(sel.Inputs) != 1 || sel.Inputs[0].Type != "uint256" {
		t.Errorf("got %+v", sel)
	}
}

func TestParseSelectorMultipleArgs(t *testing.T) {
	sel, err := ParseSelector("baz(uint32,bool)")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if len(sel.Inputs) != 2 || sel.Inputs[0].Type != "uint32" || sel.Inputs[1].Type != "bool" {
		t.Errorf("got %+v", sel)
	}
}

func TestParseSelectorArrayArg(t *testing.T) {
	sel, err := ParseSelector("f(uint256[])")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if len(sel.Inputs) != 1 || sel.Inputs[0].Type != "uint256[]" {
		t.Errorf("got %+v", sel)
	}
	sel2, err := ParseSelector("g(uint256[5])")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if sel2.Inputs[0].Type != "uint256[5]" {
		t.Errorf("got %+v", sel2)
	}
}

func TestParseSelectorTuple(t *testing.T) {
	sel, err := ParseSelector("f((address,bytes))")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if len(sel.Inputs) != 1 || sel.Inputs[0].Type != "tuple" {
		t.Fatalf("got %+v", sel)
	}
	if len(sel.Inputs[0].Components) != 2 {
		t.Errorf("expected 2 tuple components, got %+v", sel.Inputs[0].Components)
	}
}

func TestParseSelectorTupleArray(t *testing.T) {
	sel, err := ParseSelector("f((address,bytes)[])")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if sel.Inputs[0].Type != "tuple[]" {
		t.Errorf("got %+v", sel.Inputs[0])
	}
}

func TestParseSelectorNestedTuple(t *testing.T) {
	sel, err := ParseSelector("f((address,(uint256,bool)))")
	if err != nil {
		t.Fatalf("ParseSelector error: %v", err)
	}
	if len(sel.Inputs) != 1 || len(sel.Inputs[0].Components) != 2 {
		t.Fatalf("got %+v", sel)
	}
	nested := sel.Inputs[0].Components[1]
	if nested.Type != "tuple" || len(nested.Components) != 2 {
		t.Errorf("got %+v", nested)
	}
}

func TestParseSelectorErrors(t *testing.T) {
	cases := []string{
		"",
		"1foo()",
		"foo(",
		"foo)",
		"foo(uint256",
		"foo(uint256))",
		"foo()extra",
		"foo(,)",
	}
	for _, c := range cases {
		if _, err := ParseSelector(c); err == nil {
			t.Errorf("ParseSelector(%q) expected error, got nil", c)
		}
	}
}

func TestIsDigitAlphaIdentSymbol(t *testing.T) {
	if !isDigit('5') || isDigit('a') {
		t.Error("isDigit failed")
	}
	if !isAlpha('a') || !isAlpha('Z') || isAlpha('5') {
		t.Error("isAlpha failed")
	}
	if !isIdentifierSymbol('$') || !isIdentifierSymbol('_') || isIdentifierSymbol('a') {
		t.Error("isIdentifierSymbol failed")
	}
}

func TestParseTokenErrors(t *testing.T) {
	if _, _, err := parseToken("", false); err == nil {
		t.Error("expected error for empty token")
	}
	if _, _, err := parseToken("1abc", false); err == nil {
		t.Error("expected error for invalid token start")
	}
}

func TestParseIdentifierWithSymbols(t *testing.T) {
	name, rest, err := parseIdentifier("$foo_bar(uint256)")
	if err != nil {
		t.Fatalf("parseIdentifier error: %v", err)
	}
	if name != "$foo_bar" || rest != "(uint256)" {
		t.Errorf("got name=%q rest=%q", name, rest)
	}
}
