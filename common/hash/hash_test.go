package hash

import (
	"bytes"
	"testing"
)

type testDerivableList struct {
	items [][]byte
}

func (l testDerivableList) Len() int {
	return len(l.items)
}

func (l testDerivableList) EncodeIndex(i int, buf *bytes.Buffer) {
	buf.Write(l.items[i])
}

func TestDeriveShaEmptyLegacyUsesNilHash(t *testing.T) {
	t.Parallel()
	if got := DeriveSha(testDerivableList{}); got != NilHash {
		t.Fatalf("DeriveSha(empty) = %s, want NilHash %s", got, NilHash)
	}
}

func TestDeriveShaV2EmptyUsesEmptyRootHash(t *testing.T) {
	t.Parallel()
	if got := DeriveShaV2(testDerivableList{}); got != EmptyRootHash {
		t.Fatalf("DeriveShaV2(empty) = %s, want EmptyRootHash %s", got, EmptyRootHash)
	}
}
