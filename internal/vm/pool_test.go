package vm

import "testing"

func TestGetPutUint256(t *testing.T) {
	v := GetUint256()
	if v == nil {
		t.Fatal("GetUint256 returned nil")
	}
	v.SetUint64(42)
	PutUint256(v)

	v2 := GetUint256()
	if v2 == nil {
		t.Fatal("GetUint256 returned nil on second call")
	}
	// Pool semantics don't guarantee the same object comes back, but it must
	// be cleared if it is the recycled one.
	PutUint256(v2)
	PutUint256(nil) // must not panic
}

func TestGetPutHashBuffer(t *testing.T) {
	b := GetHashBuffer()
	if b == nil || len(*b) != 32 {
		t.Fatalf("GetHashBuffer returned invalid buffer: %v", b)
	}
	(*b)[0] = 0xFF
	PutHashBuffer(b)

	// wrong-size buffer should not be accepted (must not panic either)
	wrong := make([]byte, 16)
	PutHashBuffer(&wrong)
	PutHashBuffer(nil)
}

func TestGetPutByteSlice(t *testing.T) {
	s := GetByteSlice(16)
	if len(s) != 16 {
		t.Fatalf("GetByteSlice(16) len = %d, want 16", len(s))
	}
	for _, b := range s {
		if b != 0 {
			t.Fatalf("expected zeroed slice, got %v", s)
		}
	}
	PutByteSlice(s)

	big := GetByteSlice(1000)
	if len(big) != 1000 {
		t.Fatalf("GetByteSlice(1000) len = %d, want 1000", len(big))
	}
	PutByteSlice(big) // wrong cap, should just be a no-op
}
