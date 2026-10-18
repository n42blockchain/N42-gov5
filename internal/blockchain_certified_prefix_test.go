package internal

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/params"
)

func TestCertifiedCanonicalPrefixNeverJoinsInflightWrite(t *testing.T) {
	// No database is installed: this guard must neither read it nor join the
	// incomplete job (whose failure recovery can write/reload the QMDB tree).
	job := &lagWriteJob{done: make(chan struct{}), err: errors.New("unjoined write failure")}
	bc := &BlockChain{engine: &hotstuff.HotStuff{}, chainConfig: &params.ChainConfig{Performance: &params.PerformanceChainConfig{}}, qmdbEnabled: true}
	bc.lagWrite.inflight = job
	proof, existing, err := bc.VerifyCertifiedCanonicalPrefix(&hotstuff.QuorumCertificate{BlockHash: types.Hash{1}})
	if err != nil || existing || proof != (CertifiedCanonicalHead{}) || bc.lagWrite.inflight != job || bc.lagWrite.failed.Load() != 0 {
		t.Fatalf("read-only prefix guard joined or altered pending write: %+v %t %v", proof, existing, err)
	}
}
