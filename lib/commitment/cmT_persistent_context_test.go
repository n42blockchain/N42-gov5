package commitment

// cmT_persistent_context_test.go covers PersistentPatriciaContext's
// SetTxNum/TxNum accessors and the Storage accessor's nil-reader and
// nil-result "not present" paths (mirroring the equivalent Account tests).

import "testing"

type cmTStorageReaderStub struct {
	store map[string]*Update
}

func (r *cmTStorageReaderStub) Storage(plainKey []byte) (*Update, error) {
	if u, ok := r.store[string(plainKey)]; ok {
		return u, nil
	}
	return nil, nil
}

func TestPersistentPatriciaContext_TxNum(t *testing.T) {
	p := NewPersistentPatriciaContext(nil, nil)
	if got := p.TxNum(); got != 0 {
		t.Errorf("default TxNum: got %d, want 0", got)
	}
	p.SetTxNum(42)
	if got := p.TxNum(); got != 42 {
		t.Errorf("TxNum after SetTxNum(42): got %d, want 42", got)
	}
}

func TestPersistentPatriciaContext_Storage(t *testing.T) {
	// Nil reader: "not present" sentinel (DeleteUpdate flag).
	p := NewPersistentPatriciaContext(nil, nil)
	u, err := p.Storage([]byte("any-key"))
	if err != nil {
		t.Fatalf("Storage with nil reader: %v", err)
	}
	if u == nil || u.Flags&DeleteUpdate == 0 {
		t.Errorf("Storage with nil reader: got %+v, want DeleteUpdate sentinel", u)
	}

	// Reader present but key missing: same "not present" sentinel.
	reader := &cmTStorageReaderStub{store: map[string]*Update{}}
	p2 := NewPersistentPatriciaContext(nil, reader)
	u2, err := p2.Storage([]byte("missing-key"))
	if err != nil {
		t.Fatalf("Storage with missing key: %v", err)
	}
	if u2 == nil || u2.Flags&DeleteUpdate == 0 {
		t.Errorf("Storage with missing key: got %+v, want DeleteUpdate sentinel", u2)
	}

	// Reader returns a real update: passed through unchanged.
	want := &Update{Flags: StorageUpdate}
	reader.store[string([]byte("present-key"))] = want
	u3, err := p2.Storage([]byte("present-key"))
	if err != nil {
		t.Fatalf("Storage with present key: %v", err)
	}
	if u3 != want {
		t.Errorf("Storage with present key: got %+v, want the exact reader value", u3)
	}
}
