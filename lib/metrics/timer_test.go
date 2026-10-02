package metrics

import "testing"

func TestNewHistTimerAndPutSince(t *testing.T) {
	tm := NewHistTimer("test_timer_base")
	if tm.name != "test_timer_base" {
		t.Fatalf("name = %q, want test_timer_base", tm.name)
	}
	tm.PutSince()
}

func TestNewHistTimerStripsTagsFromName(t *testing.T) {
	tm := NewHistTimer(`test_timer_tagged{foo="bar"}`)
	if tm.name != "test_timer_tagged" {
		t.Fatalf("name = %q, want test_timer_tagged", tm.name)
	}
}

func TestHistTimerTagEvenPairs(t *testing.T) {
	base := NewHistTimer("test_timer_tag_base")
	tagged := base.Tag("foo", "bar", "baz", "qux")
	if tagged.name != "test_timer_tag_base" {
		t.Fatalf("tagged name = %q, want test_timer_tag_base", tagged.name)
	}
	tagged.PutSince()
}

func TestHistTimerTagOddPairs(t *testing.T) {
	base := NewHistTimer("test_timer_tag_odd_base")
	// Odd number of args triggers the UNEQUAL_KEY_VALUE_TAGS fallback path.
	tagged := base.Tag("foo")
	if tagged.name != "test_timer_tag_odd_base" {
		t.Fatalf("tagged name = %q, want test_timer_tag_odd_base", tagged.name)
	}
}

func TestHistTimerTagNoPairs(t *testing.T) {
	base := NewHistTimer("test_timer_tag_none_base")
	tagged := base.Tag()
	if tagged.name != "test_timer_tag_none_base" {
		t.Fatalf("tagged name = %q, want test_timer_tag_none_base", tagged.name)
	}
}

func TestHistTimerChild(t *testing.T) {
	base := NewHistTimer("test_timer_child_base")
	child := base.Child("sub")
	if child.name != "test_timer_child_base_sub" {
		t.Fatalf("child name = %q, want test_timer_child_base_sub", child.name)
	}

	// Leading underscore on the suffix is trimmed.
	child2 := base.Child("_sub2")
	if child2.name != "test_timer_child_base_sub2" {
		t.Fatalf("child2 name = %q, want test_timer_child_base_sub2", child2.name)
	}
}
