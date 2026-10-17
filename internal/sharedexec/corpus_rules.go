package sharedexec

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// CorpusRecipient implements the fixed original public recipient derivation.
// A validated descriptor always supplies nonzero count.
func CorpusRecipient(index, count uint32) types.Address {
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[:], index%count)
	return types.BytesToAddress(crypto.Keccak256([]byte(CorpusRecipientRule), encoded[:])[12:])
}

// ValidateCorpusGenesis retains the original private EIP155 source profile.
func ValidateCorpusGenesis(g *conf.Genesis) error {
	if g == nil || g.Config == nil || g.Config.Performance == nil || g.Config.HotStuff == nil {
		return errors.New("explicit private performance genesis required")
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if err := g.Config.ValidatePerformanceSelection("private", profile); err != nil {
		return err
	}
	if g.Timestamp != params.PerformanceGenesisTime || !bytes.Equal(g.ExtraData, []byte(params.PerformanceGenesisMarker)) || g.Config.DeferredExecutionTime != nil || g.Config.HotStuff.InteropV4 || g.Config.HotStuff.CommitteePool != nil {
		return errors.New("unsupported supply genesis mode/marker")
	}
	return nil
}
