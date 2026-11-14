// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Frame ingest, attestation pool and local frame signer wiring.

package node

import (
	"context"
	"errors"
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/attest"
	framestore "github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/internal/ingest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// enableFrameIngest turns on the gateway-signed frame path of the ingest
// listener when N42_FRAME_GATEWAYS lists at least one gateway address
// (docs/QS_MULTI_MILLION_DESIGN.md, P1a/P1b). Frames land in the frame store,
// which the chain resolves frame-referenced bodies from and, with
// N42_FRAME_BLOCKS=1, the leader fills blocks from. Frames never enter the
// transaction pool.
func (n *Node) enableFrameIngest() {
	gateways, err := ingest.GatewaysFromEnv()
	if err != nil {
		log.Error("Frame ingest disabled: bad gateway list", "env", ingest.EnvFrameGateways, "err", err)
		return
	}
	// Frames signed by a current validator (leaders' local frames of pool
	// transactions) are admitted beside the gateways'.
	isValidator := n.frameValidatorCheck()
	if len(gateways) == 0 && (!internal.FrameBlocksEnabled() || isValidator == nil) {
		return
	}
	st := framestore.NewFromEnv()
	realBC, _ := n.blockChain.(*internal.BlockChain)
	var lookup func(types.Hash) ([]byte, bool)
	if realBC != nil {
		// FRQ1 pulls are served from the Frames table once a block has
		// taken a frame out of the store.
		lookup = func(h types.Hash) ([]byte, bool) {
			var raw []byte
			_ = realBC.ChainDB.View(context.Background(), func(tx kv.Tx) error {
				v, err := rawdb.ReadFrame(tx, h)
				if err == nil && len(v) > 0 {
					raw = append([]byte(nil), v...)
				}
				return err
			})
			return raw, raw != nil
		}
	}
	if err := n.ingestServer.EnableFrames(ingest.FrameConfig{
		Gateways:    gateways,
		IsValidator: isValidator,
		Store:       st,
		Lookup:      lookup,
	}); err != nil {
		log.Error("Frame ingest disabled", "err", err)
		return
	}
	if realBC != nil {
		// Every node resolves frame-referenced bodies from this store;
		// N42_FRAME_BLOCKS=1 additionally makes it the leader's block source.
		realBC.SetFrameStore(st)
		srv := n.ingestServer
		if peers := ingest.PullPeersFromEnv(); len(peers) > 0 {
			realBC.SetFramePuller(func(ctx context.Context, hashes []types.Hash) map[types.Hash][]byte {
				return srv.PullFrames(ctx, peers, hashes)
			})
		}
		log.Info("Frame blocks", "produce", internal.FrameBlocksEnabled(), "pullPeers", len(ingest.PullPeersFromEnv()))
		// Sender recovery at admission (P2a): every admitted frame's senders
		// are recovered in the background under the signer of the block
		// after the current head and cached with the frame.
		cfg := realBC.Config()
		rec := frame.NewSenderRecoverer(st, func() transaction.Signer {
			cur := realBC.CurrentBlock()
			if cur == nil || cur.Header() == nil {
				return nil
			}
			h, ok := cur.Header().(*block.Header)
			if !ok || h == nil || h.Number == nil {
				return nil
			}
			next := new(big.Int).Add(h.Number.ToBig(), big.NewInt(1))
			return transaction.MakeSignerWithTimestamp(cfg, next, h.Time+1)
		}, frame.RecoverWorkersFromEnv())
		if pool := n.frameAttestPool(st, realBC); pool != nil {
			rec.SetSource(pool)
			realBC.SetFrameSenderSource(pool)
		}
		rec.Start(n.ctx)
		log.Info("Frame sender recovery at admission", "workers", frame.RecoverWorkersFromEnv())
	}
}

// frameAttestPool starts frame sender attestation (P8b,
// N42_FRAME_ATTEST=1) on a HotStuff node: this node attests the frames
// whose committee it is on, pushes the attestations to the other
// validators' ingest listeners, and takes the senders of every other frame
// from f+1 matching attestations. Nil when off.
func (n *Node) frameAttestPool(st *framestore.Store, bc *internal.BlockChain) *attest.Pool {
	if !attest.Enabled() {
		return nil
	}
	hs, ok := resolveHotStuffEngine(n.engine)
	if !ok {
		log.Warn("Frame attestation off: not a HotStuff node")
		return nil
	}
	// The validator view is rebuilt only when the engine's set changes.
	var (
		vmu    sync.Mutex
		lastVS *hotstuff.ValidatorSet
		lastV  *attest.Validators
	)
	validators := func() *attest.Validators {
		eng := hs.Engine()
		if eng == nil {
			return nil
		}
		vs := eng.CurrentValidatorSet()
		if vs == nil || vs.IsEmpty() {
			return nil
		}
		vmu.Lock()
		defer vmu.Unlock()
		if vs == lastVS {
			return lastV
		}
		v := &attest.Validators{Sorted: attest.SortAddresses(vs.Addresses()), F: int(vs.FaultTolerance())}
		v.Verify = func(a types.Address, msg, sig []byte) bool {
			i := vs.FindByAddress(a)
			if i < 0 {
				return false
			}
			pk, err := vs.GetPublicKey(hotstuff.ValidatorIndex(i))
			return err == nil && hotstuff.VerifyBLSSignature(sig, pk, msg)
		}
		lastVS, lastV = vs, v
		return v
	}
	self := func() types.Address {
		a, key := hs.SignerKey()
		if key == nil {
			return types.Address{}
		}
		return a
	}
	sign := func(msg []byte) ([]byte, error) {
		_, key := hs.SignerKey()
		if key == nil {
			return nil, errors.New("no validator key")
		}
		return key.Sign(msg).Marshal(), nil
	}
	cfg := bc.Config()
	signer := func() transaction.Signer {
		cur := bc.CurrentBlock()
		if cur == nil || cur.Header() == nil {
			return nil
		}
		h, ok := cur.Header().(*block.Header)
		if !ok || h == nil || h.Number == nil {
			return nil
		}
		next := new(big.Int).Add(h.Number.ToBig(), big.NewInt(1))
		return transaction.MakeSignerWithTimestamp(cfg, next, h.Time+1)
	}
	peers := attest.PeersFromEnv(ingest.PullPeersFromEnv(), n.config.IngestCfg.Addr)
	pusher := attest.NewPusher(peers)
	pusher.Start(n.ctx)
	flush := 20 * time.Millisecond
	if v, err := strconv.Atoi(os.Getenv(attest.EnvFlushMs)); err == nil && v > 0 {
		flush = time.Duration(v) * time.Millisecond
	}
	testK := attest.TestCommitteeFromEnv()
	if testK > 0 {
		f := -1
		if v := validators(); v != nil {
			f = v.F
		}
		log.Warn("!!! N42_ATTEST_COMMITTEE IS SET (TEST ONLY) !!! frame attestation committee size and quorum are overridden; "+
			"a quorum below f+1 lets k colluding committee members forge frame senders",
			"k", testK, "f", f, "reduced", f >= 0 && testK < f+1)
	}
	pool := attest.NewPool(attest.Config{
		Validators: validators, Self: self, Sign: sign, Signer: signer, Store: st,
		CommitteeSize: attest.CommitteeSizeFromEnv(), TestCommittee: testK, Send: pusher.Send, FlushEvery: flush,
	})
	pool.Start(n.ctx)
	n.ingestServer.SetAttestHandler(pool.HandleWire)
	log.Info("Frame sender attestation", "committee", attest.CommitteeSizeFromEnv(), "testCommittee", testK, "peers", peers, "flush", flush)
	return pool
}

// frameValidatorCheck returns the frame admission check for the current
// HotStuff validator set, read at call time so epoch changes apply, or nil
// when the engine is not HotStuff.
func (n *Node) frameValidatorCheck() func(types.Address) bool {
	hs, ok := resolveHotStuffEngine(n.engine)
	if !ok {
		return nil
	}
	return func(a types.Address) bool {
		eng := hs.Engine()
		if eng == nil {
			return false
		}
		vs := eng.CurrentValidatorSet()
		return vs != nil && vs.FindByAddress(a) >= 0
	}
}

// localFrameSigner returns the key this node frames its pool transactions
// under when it leads a frame block (N42_FRAME_BLOCKS=1), or nil.
//
// The etherbase account's own secp256k1 key is used when the keystore has
// it unlocked: its address is the validator address, which every follower
// admits. A HotStuff validator signs consensus messages with BLS only and
// has no secp256k1 key for its address; it uses a key derived from its BLS
// secret (frame.DeriveLocalKey). That address is not the validator address,
// so followers admit those frames only when N42_FRAME_GATEWAYS lists it;
// the address is logged here.
func (n *Node) localFrameSigner(etherbase types.Address) *frame.Signer {
	if !internal.FrameBlocksEnabled() {
		return nil
	}
	acct := accounts.Account{Address: etherbase}
	for _, backend := range n.accman.Backends(keystore.KeyStoreType) {
		ks, ok := backend.(*keystore.KeyStore)
		if !ok {
			continue
		}
		if _, err := ks.SignHash(acct, make([]byte, 32)); err != nil {
			continue
		}
		log.Info("Local frame signer", "address", etherbase, "source", "etherbase keystore")
		return &frame.Signer{Address: etherbase, SignHash: func(h []byte) ([]byte, error) { return ks.SignHash(acct, h) }}
	}
	if _, ok := resolveHotStuffEngine(n.engine); ok {
		blsKey, err := hotstuff.LoadBLSKeyFromDir(n.keyDir, etherbase)
		if err != nil {
			log.Warn("Local frames off: no validator key", "err", err)
			return nil
		}
		key, err := frame.DeriveLocalKey(blsKey.Marshal())
		if err != nil {
			log.Warn("Local frames off", "err", err)
			return nil
		}
		s := frame.KeySigner(key)
		log.Info("Local frame signer", "address", s.Address, "source", "derived from the BLS key", "validator", etherbase,
			"note", "followers admit it only if "+ingest.EnvFrameGateways+" lists it")
		return s
	}
	log.Warn("Local frames off: etherbase key locked and no HotStuff BLS key; pool transactions are not mined in frame blocks")
	return nil
}
