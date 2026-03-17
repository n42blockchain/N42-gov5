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

func TestDeriveShaEmptyUsesEthereumEmptyTrieRoot(t *testing.T) {
	t.Parallel()

	if got := DeriveSha(testDerivableList{}); got != EmptyRootHash {
		t.Fatalf("DeriveSha(empty) = %s, want %s", got, EmptyRootHash)
	}
}
