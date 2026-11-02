package transaction

import (
	"encoding/binary"
	"testing"
)

// A uvarint of 2^64-1 becomes int(-1), which passed the truncation guard and
// panicked on the slice.
func TestCompactTxRejectsNegativeLength(t *testing.T) {
	r := &txCompactReader{p: binary.AppendUvarint(nil, ^uint64(0))}
	if _, err := r.blob(); err == nil {
		t.Fatal("blob with an overflowing length was accepted")
	}
}

func TestCompactTxRejectsOversizedAccessList(t *testing.T) {
	r := &txCompactReader{p: binary.AppendUvarint(nil, ^uint64(0))}
	if _, err := r.accessList(); err == nil {
		t.Fatal("access list with an overflowing length was accepted")
	}
}
