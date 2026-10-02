package abi

import (
	"reflect"
	"testing"
)

func TestOverloadedArgNameErrorsAndOverload(t *testing.T) {
	if _, err := overloadedArgName("_", map[string]string{}); err == nil {
		t.Error("expected error for a purely underscored field name")
	}

	names := map[string]string{"Foo": "x"}
	got, err := overloadedArgName("foo", names)
	if err != nil {
		t.Fatalf("overloadedArgName() error: %v", err)
	}
	if got != "Foo0" {
		t.Errorf("overloadedArgName() = %q, want %q", got, "Foo0")
	}
}

func TestFormatSliceString(t *testing.T) {
	if got := formatSliceString(reflect.Uint64, -1); got != "[]uint64" {
		t.Errorf("formatSliceString(dynamic) = %q, want %q", got, "[]uint64")
	}
	if got := formatSliceString(reflect.Uint64, 3); got != "[3]uint64" {
		t.Errorf("formatSliceString(fixed) = %q, want %q", got, "[3]uint64")
	}
}

func TestTypeGetTypeFunctionAndHash(t *testing.T) {
	fnType, err := NewType("function", "", nil)
	if err != nil {
		t.Fatalf("NewType(function) error: %v", err)
	}
	rt := fnType.GetType()
	if rt.Kind() != reflect.Array || rt.Len() != 24 {
		t.Errorf("GetType() for function = %v, want [24]byte", rt)
	}
}
