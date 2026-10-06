package hotstuff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// Once set, this marker makes the separate store mandatory on subsequent
// starts, even if the opt-in environment setting is removed.
var voteStoreKey = []byte("separate_vote_store_v1")

// HasIndependentVoteStore also treats a malformed marker as present, so reset
// tools cannot silently delete the checkpoint and bypass the separate journal.
func HasIndependentVoteStore(tx kv.Tx) (bool, error) {
	return tx.Has(modules.HotStuffState, voteStoreKey)
}

type voteEpochRecord struct {
	Epoch      uint64
	Validators []ValidatorInfo
	F          uint32
}
type voteEpochSnapshot struct{ Active, Staged *voteEpochRecord }

func (e *ConsensusEngine) snapshotVoteEpoch() *voteEpochSnapshot {
	snapshot := &voteEpochSnapshot{}
	if epoch, vals, f, ok := e.epochManager.CurrentEpochInfo(); ok {
		snapshot.Active = &voteEpochRecord{epoch, vals, f}
	}
	if epoch, vals, f, ok := e.epochManager.StagedEpochInfo(); ok {
		snapshot.Staged = &voteEpochRecord{epoch, vals, f}
	}
	return snapshot
}

func readVoteEpoch(tx kv.Tx) (*voteEpochSnapshot, error) {
	snapshot := &voteEpochSnapshot{}
	epoch, vals, f, ok, err := LoadActiveEpoch(tx)
	if err != nil {
		return nil, err
	}
	if ok {
		snapshot.Active = &voteEpochRecord{epoch, vals, f}
	}
	epoch, vals, f, err = LoadStagedEpoch(tx)
	if err != nil {
		return nil, err
	}
	if len(vals) > 0 {
		snapshot.Staged = &voteEpochRecord{epoch, vals, f}
	}
	return snapshot, nil
}

func mergeEpochRecord(a, b *voteEpochRecord) (*voteEpochRecord, error) {
	if a == nil {
		return b, nil
	}
	if b == nil {
		return a, nil
	}
	if a.Epoch > b.Epoch {
		return a, nil
	}
	if b.Epoch > a.Epoch {
		return b, nil
	}
	if a.F != b.F || len(a.Validators) != len(b.Validators) {
		return nil, fmt.Errorf("conflicting validator sets for epoch %d", a.Epoch)
	}
	for i, v := range a.Validators {
		other := b.Validators[i]
		if v.Address != other.Address || !bytes.Equal(v.PublicKey.Marshal(), other.PublicKey.Marshal()) {
			return nil, fmt.Errorf("conflicting validator sets for epoch %d", a.Epoch)
		}
	}
	return a, nil
}

func mergeVoteEpoch(a, b *voteEpochSnapshot) (*voteEpochSnapshot, error) {
	if a == nil {
		a = &voteEpochSnapshot{}
	}
	if b == nil {
		b = &voteEpochSnapshot{}
	}
	active, err := mergeEpochRecord(a.Active, b.Active)
	if err != nil {
		return nil, err
	}
	stagedA, stagedB := a.Staged, b.Staged
	if active != nil {
		if stagedA != nil && stagedA.Epoch <= active.Epoch {
			stagedA = nil
		}
		if stagedB != nil && stagedB.Epoch <= active.Epoch {
			stagedB = nil
		}
	}
	staged, err := mergeEpochRecord(stagedA, stagedB)
	if err != nil {
		return nil, err
	}
	return &voteEpochSnapshot{active, staged}, nil
}

func saveVoteEpoch(tx kv.RwTx, snapshot *voteEpochSnapshot) error {
	if snapshot == nil || snapshot.Active == nil {
		return fmt.Errorf("vote snapshot has no active validator set")
	}
	old, err := readVoteEpoch(tx)
	if err != nil {
		return err
	}
	snapshot, err = mergeVoteEpoch(snapshot, old)
	if err != nil {
		return err
	}
	if err = SaveActiveEpoch(tx, snapshot.Active.Epoch, snapshot.Active.Validators, snapshot.Active.F); err != nil {
		return err
	}
	if snapshot.Staged == nil {
		return ClearStagedEpoch(tx)
	}
	return SaveStagedEpoch(tx, snapshot.Staged.Epoch, snapshot.Staged.Validators, snapshot.Staged.F)
}

// ConfigureVoteStore must run before Start. Disabling requested cannot bypass
// a marker left by an earlier run. The directory belongs to this node's datadir.
func (s *Service) ConfigureVoteStore(path string, genesis types.Hash, requested bool) error {
	ce := s.engine.Engine()
	if ce == nil {
		return fmt.Errorf("vote store: engine not initialized")
	}
	chainID := ""
	if s.engine.chainConfig != nil && s.engine.chainConfig.ChainID != nil {
		chainID = s.engine.chainConfig.ChainID.String()
	}
	binding, err := json.Marshal(struct {
		Version   int
		Genesis   types.Hash
		ChainID   string
		PublicKey []byte
	}{1, genesis, chainID, ce.secretKey.PublicKey().Marshal()})
	if err != nil {
		return err
	}
	s.voteStorePath, s.voteStoreBinding, s.voteStoreRequested = path, binding, requested
	return nil
}

func validRecoveryState(state *ConsensusState) error {
	if state != nil && (state.LockedQC.View > state.View || state.LastCommittedQC.View > state.View) {
		return fmt.Errorf("vote store: QC exceeds snapshot view")
	}
	return nil
}

// openVoteStore reconciles the durable journal into the main checkpoint before
// normal recovery. No engine events or votes may run during this operation.
func (s *Service) openVoteStore() (retErr error) {
	if s.db == nil {
		return nil
	}
	var marker []byte
	var marked bool
	if err := s.db.View(s.ctx, func(tx kv.Tx) error {
		var err error
		marked, err = HasIndependentVoteStore(tx)
		if err != nil {
			return err
		}
		v, e := tx.GetOne(modules.HotStuffState, voteStoreKey)
		marker = bytes.Clone(v)
		return e
	}); err != nil {
		return err
	}
	if !marked && !s.voteStoreRequested {
		return nil
	}
	if s.voteStorePath == "" || len(s.voteStoreBinding) == 0 {
		return fmt.Errorf("independent vote store is required but not configured")
	}
	if marked && !bytes.Equal(marker, s.voteStoreBinding) {
		return fmt.Errorf("vote store marker identity mismatch")
	}
	db, err := mdbx.NewMDBX(log.New()).Path(s.voteStorePath).MapSize(64 * datasize.MB).GrowthStep(4 * datasize.MB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.TableCfg{modules.HotStuffState: kv.TableCfgItem{}} }).Open(s.ctx)
	if err != nil {
		return fmt.Errorf("open independent vote store: %w", err)
	}
	defer func() {
		if retErr != nil {
			db.Close()
		}
	}()
	var journal *ConsensusState
	var epochs *voteEpochSnapshot
	var binding []byte
	var bindingPresent bool
	err = db.View(s.ctx, func(tx kv.Tx) error {
		var err error
		bindingPresent, err = HasIndependentVoteStore(tx)
		if err != nil {
			return err
		}
		v, e := tx.GetOne(modules.HotStuffState, voteStoreKey)
		if e != nil {
			return e
		}
		binding = bytes.Clone(v)
		journal, e = LoadConsensusState(tx)
		if e != nil {
			return e
		}
		if e = validRecoveryState(journal); e != nil {
			return e
		}
		epochs, e = readVoteEpoch(tx)
		return e
	})
	if err != nil {
		return fmt.Errorf("read independent vote store: %w", err)
	}
	if len(binding) == 0 {
		if marked || bindingPresent || journal != nil || epochs.Active != nil || epochs.Staged != nil {
			return fmt.Errorf("independent vote store identity missing")
		}
	} else if !bytes.Equal(binding, s.voteStoreBinding) {
		return fmt.Errorf("independent vote store identity mismatch")
	}
	if marked && journal == nil {
		return fmt.Errorf("independent vote store lost its consensus record")
	}
	if journal != nil {
		if epochs.Active == nil {
			return fmt.Errorf("independent vote store lost its active epoch")
		}
		err = s.db.Update(s.ctx, func(tx kv.RwTx) error {
			main, e := LoadConsensusState(tx)
			if e != nil {
				return e
			}
			if e = validRecoveryState(main); e != nil {
				return e
			}
			if e = SaveConsensusState(tx, journal); e != nil {
				return e
			}
			return saveVoteEpoch(tx, epochs)
		})
		if err != nil {
			return fmt.Errorf("merge independent vote store: %w", err)
		}
	}
	s.voteDB = db
	return nil
}

func (s *Service) seedVoteStore() error {
	if s.voteDB == nil {
		return nil
	}
	ce := s.engine.Engine()
	ce.mu.Lock()
	ce.captureVoteEpoch = true
	state := ce.snapshotState()
	ce.mu.Unlock()
	if err := s.voteDB.Update(s.ctx, func(tx kv.RwTx) error {
		if err := SaveConsensusState(tx, state); err != nil {
			return err
		}
		if err := saveVoteEpoch(tx, state.VoteEpoch); err != nil {
			return err
		}
		return tx.Put(modules.HotStuffState, voteStoreKey, s.voteStoreBinding)
	}); err != nil {
		return fmt.Errorf("seed independent vote store: %w", err)
	}
	// Make the new database's directory entries durable before the main store
	// publishes the marker that requires them on recovery.
	for _, path := range []string{s.voteStorePath, filepath.Dir(s.voteStorePath)} {
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return fmt.Errorf("sync vote store directory: %w", err)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return s.db.Update(s.ctx, func(tx kv.RwTx) error { return tx.Put(modules.HotStuffState, voteStoreKey, s.voteStoreBinding) })
}

type stoppedVoteJournal struct{}

func (stoppedVoteJournal) JournalVote(*ConsensusState) error { return context.Canceled }
func (s *Service) closeVoteStore() {
	if s.voteDB == nil {
		return
	}
	// Serializes with an in-flight vote and keeps late execution notifications
	// from publishing votes after the durable journal has closed.
	s.voteStoreClose.Do(func() {
		s.engine.Engine().SetVoteJournal(stoppedVoteJournal{})
		s.voteDB.Close()
	})
	// Keep the pointer immutable for event goroutines; stoppedVoteJournal prevents
	// future calls. Start is not reusable after Stop.
}
