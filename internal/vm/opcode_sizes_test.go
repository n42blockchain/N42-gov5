package vm

import "testing"

func TestIsPushOpcode(t *testing.T) {
	if !IsPushOpcode(PUSH1) || !IsPushOpcode(PUSH32) || !IsPushOpcode(PUSH16) {
		t.Error("expected PUSH1/16/32 to be push opcodes")
	}
	if IsPushOpcode(ADD) || IsPushOpcode(STOP) {
		t.Error("expected non-push opcodes to be false")
	}
}

func TestPushSize(t *testing.T) {
	if PushSize(PUSH1) != 1 {
		t.Errorf("PushSize(PUSH1) = %d, want 1", PushSize(PUSH1))
	}
	if PushSize(PUSH32) != 32 {
		t.Errorf("PushSize(PUSH32) = %d, want 32", PushSize(PUSH32))
	}
	if PushSize(ADD) != 0 {
		t.Errorf("PushSize(ADD) = %d, want 0", PushSize(ADD))
	}
}

func TestGetOpcodeSize(t *testing.T) {
	if GetOpcodeSize(ADD, nil) != 1 {
		t.Errorf("GetOpcodeSize(ADD) = %d, want 1", GetOpcodeSize(ADD, nil))
	}
	if GetOpcodeSize(PUSH1, nil) != 2 {
		t.Errorf("GetOpcodeSize(PUSH1) = %d, want 2", GetOpcodeSize(PUSH1, nil))
	}
	if GetOpcodeSize(PUSH32, nil) != 33 {
		t.Errorf("GetOpcodeSize(PUSH32) = %d, want 33", GetOpcodeSize(PUSH32, nil))
	}
	if GetOpcodeSize(RJUMP, nil) != 3 {
		t.Errorf("GetOpcodeSize(RJUMP) = %d, want 3", GetOpcodeSize(RJUMP, nil))
	}
	if GetOpcodeSize(DUPN, nil) != 2 {
		t.Errorf("GetOpcodeSize(DUPN) = %d, want 2", GetOpcodeSize(DUPN, nil))
	}
	// RJUMPV with code providing count byte
	code := []byte{byte(RJUMPV), 2, 0, 1, 0, 2}
	if got := GetOpcodeSize(RJUMPV, code); got != 6 {
		t.Errorf("GetOpcodeSize(RJUMPV, count=2) = %d, want 6", got)
	}
	// RJUMPV with insufficient code to read count
	if got := GetOpcodeSize(RJUMPV, []byte{byte(RJUMPV)}); got != 2 {
		t.Errorf("GetOpcodeSize(RJUMPV, short code) = %d, want 2", got)
	}
}
