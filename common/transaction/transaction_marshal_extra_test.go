package transaction

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

func TestTransactionWithSignatureValues(t *testing.T) {
	r := require.New(t)
	tx := newLegacyForAccessors()
	v, rr, s := uint256.NewInt(99), uint256.NewInt(2), uint256.NewInt(3)
	got, err := tx.WithSignatureValues(v, rr, s)
	r.NoError(err)
	r.Same(tx, got)

	gv, gr, gs := tx.RawSignatureValues()
	r.Equal(v, gv)
	r.Equal(rr, gr)
	r.Equal(s, gs)
}

func TestTransactionMarshalTo(t *testing.T) {
	r := require.New(t)
	tx := newLegacyForAccessors()
	full, err := tx.Marshal()
	r.NoError(err)

	buf := make([]byte, len(full))
	n, err := tx.MarshalTo(buf)
	r.NoError(err)
	r.Equal(len(full), n)
	r.Equal(full, buf)
}
