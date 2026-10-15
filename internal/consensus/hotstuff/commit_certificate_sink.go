package hotstuff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// CommitCertificateSink is a public-certificate transport, not owner authority.
// Remote ACK and the local WAL are different databases and are not atomic.
type CommitCertificateSink interface {
	CommitCertificate(context.Context, *QuorumCertificate) error
}

var certificatePrefix = []byte("performance-certificate/")
var certificatePendingPrefix = []byte("performance-certificate-pending/")

const maxCertificatePending = 256

var ErrPersistedCommitCertificate = errors.New("invalid persisted commit certificate")

type certificateRecord struct {
	Version   uint64            `json:"version"`
	ChainID   string            `json:"chain_id"`
	Namespace string            `json:"namespace"`
	Genesis   types.Hash        `json:"genesis"`
	QC        QuorumCertificate `json:"qc"`
}

// SetCommitCertificateSink must run before Start. Only an explicit performance
// service may opt in. A nil/default service keeps its existing canonical path.
func (s *Service) SetCommitCertificateSink(sink CommitCertificateSink) error {
	if sink == nil || s.engine == nil || s.engine.chainConfig == nil || s.engine.chainConfig.Performance == nil || s.db == nil || s.engine.Engine() == nil || s.engine.config == nil || len(s.engine.config.Validators) != 7 {
		return errors.New("certificate sink requires explicit seven-validator performance service and durable metadata store")
	}
	if s.commitCh != nil || s.certificateSink != nil {
		return errors.New("certificate sink must be set once before service Start")
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if err := s.engine.chainConfig.ValidatePerformanceSelection("private", profile); err != nil {
		return err
	}
	s.certificateSink = sink
	return nil
}

func certificateKey(prefix []byte, hash types.Hash) []byte {
	key := make([]byte, 0, len(prefix)+len(hash))
	key = append(key, prefix...)
	return append(key, hash[:]...)
}

func (s *Service) decodeCertificate(tx kv.Tx, hash types.Hash) (*certificateRecord, error) {
	raw, err := tx.GetOne(modules.HotStuffState, certificateKey(certificatePrefix, hash))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, errors.New("pending commit certificate record missing or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record certificateRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing commit certificate record")
	}
	genesis, err := rawdb.ReadCanonicalHash(tx, 0)
	if err != nil {
		return nil, err
	}
	cfg := s.engine.chainConfig
	if cfg == nil || cfg.ChainID == nil || cfg.Performance == nil || record.Version != 1 || record.ChainID != cfg.ChainID.String() || record.Namespace != cfg.Performance.NetworkNamespace || genesis == (types.Hash{}) || record.Genesis != genesis || record.QC.BlockHash != hash {
		return nil, errors.New("commit certificate persisted identity mismatch")
	}
	return &record, nil
}

// CommitCertificate durably saves the full certificate and pending marker
// alongside consensus state BEFORE calling the network sink. Only a durable
// remote ACK clears pending; losing the ACK leaves an idempotent retry.
func (s *Service) CommitCertificate(ctx context.Context, qc *QuorumCertificate) error {
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()
	if s.certificateSink == nil {
		return errors.New("commit certificate sink is not configured")
	}
	if qc == nil {
		return errors.New("commit certificate required")
	}
	copyQC := qc.Clone()
	if err := s.engine.VerifyPerformanceCommitQC(&copyQC); err != nil {
		return err
	}
	hook := s.newStateHook() // Resolve engine locks before opening the writer.
	var record *certificateRecord
	var pending bool
	err := s.db.Update(ctx, func(tx kv.RwTx) error {
		key := certificateKey(certificatePrefix, copyQC.BlockHash)
		existing, err := tx.GetOne(modules.HotStuffState, key)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			record, err = s.decodeCertificate(tx, copyQC.BlockHash)
			if err != nil {
				return err
			}
			marker, err := tx.GetOne(modules.HotStuffState, certificateKey(certificatePendingPrefix, copyQC.BlockHash))
			if err != nil {
				return err
			}
			pending = len(marker) > 0
		} else {
			count := 0
			if err := tx.ForPrefix(modules.HotStuffState, certificatePendingPrefix, func(_, _ []byte) error { count++; return nil }); err != nil {
				return err
			}
			if count >= maxCertificatePending {
				return errors.New("pending certificate capacity full; cannot discard committed proof")
			}
			genesis, err := rawdb.ReadCanonicalHash(tx, 0)
			if err != nil {
				return err
			}
			if genesis == (types.Hash{}) {
				return errors.New("commit certificate metadata genesis missing")
			}
			cfg := s.engine.chainConfig
			record = &certificateRecord{Version: 1, ChainID: cfg.ChainID.String(), Namespace: cfg.Performance.NetworkNamespace, Genesis: genesis, QC: copyQC}
			raw, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err := tx.Put(modules.HotStuffState, key, raw); err != nil {
				return err
			}
			if err := tx.Put(modules.HotStuffState, certificateKey(certificatePendingPrefix, copyQC.BlockHash), []byte{1}); err != nil {
				return err
			}
			pending = true
		}
		if hook != nil {
			return hook.run(tx)
		}
		return nil
	})
	if err != nil {
		s.cancel()
		return fmt.Errorf("persist commit certificate (service stopped): %w", err)
	}
	if stateHookCommitted(hook, err) {
		s.setLastPersistedView(hook.view)
	}
	if !pending {
		return nil
	}
	return s.deliverCertificate(ctx, record)
}

func (s *Service) deliverCertificate(ctx context.Context, record *certificateRecord) error {
	qc := record.QC.Clone()
	if err := s.engine.VerifyPerformanceCommitQC(&qc); err != nil {
		s.cancel()
		return fmt.Errorf("%w: %v", ErrPersistedCommitCertificate, err)
	}
	if err := s.certificateSink.CommitCertificate(ctx, &qc); err != nil {
		return err
	}
	// No cross-database atomicity is assumed: if this write fails, pending
	// remains and the already durable remote commit is replayed idempotently.
	return s.db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Delete(modules.HotStuffState, certificateKey(certificatePendingPrefix, record.QC.BlockHash))
	})
}

func (s *Service) pendingCertificates(ctx context.Context) ([]certificateRecord, error) {
	var records []certificateRecord
	err := s.db.View(ctx, func(tx kv.Tx) error {
		return tx.ForPrefix(modules.HotStuffState, certificatePendingPrefix, func(key, value []byte) error {
			if len(key) != len(certificatePendingPrefix)+32 || len(value) != 1 || value[0] != 1 || len(records) >= maxCertificatePending {
				return errors.New("malformed or oversized pending certificate set")
			}
			var hash types.Hash
			copy(hash[:], key[len(certificatePendingPrefix):])
			record, err := s.decodeCertificate(tx, hash)
			if err != nil {
				return err
			}
			records = append(records, *record)
			return nil
		})
	})
	return records, err
}

// PendingCommitCertificates exposes durable pending evidence for runtime
// recovery/health checks; returned certificates are independent decoded copies.
func (s *Service) PendingCommitCertificates(ctx context.Context) ([]QuorumCertificate, error) {
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()
	if s.certificateSink == nil {
		return nil, errors.New("commit certificate sink not configured")
	}
	records, err := s.pendingCertificates(ctx)
	if err != nil {
		return nil, err
	}
	qcs := make([]QuorumCertificate, len(records))
	for i, record := range records {
		qcs[i] = record.QC.Clone()
	}
	return qcs, nil
}

// ReplayCommitCertificates runs on startup and after imports. Height ordering
// lets direct-extension commits catch up; missing local bodies remain pending.
func (s *Service) ReplayCommitCertificates(ctx context.Context) error {
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()
	if s.certificateSink == nil {
		return errors.New("commit certificate sink not configured")
	}
	records, err := s.pendingCertificates(ctx)
	if err != nil {
		s.cancel()
		return fmt.Errorf("%w: %v", ErrPersistedCommitCertificate, err)
	}
	heights := make(map[types.Hash]uint64, len(records))
	if err := s.db.View(ctx, func(tx kv.Tx) error {
		for _, record := range records {
			height := rawdb.ReadHeaderNumber(tx, record.QC.BlockHash)
			if height == nil {
				heights[record.QC.BlockHash] = ^uint64(0)
			} else {
				heights[record.QC.BlockHash] = *height
			}
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool { return heights[records[i].QC.BlockHash] < heights[records[j].QC.BlockHash] })
	var first error
	for i := range records {
		if err := s.deliverCertificate(ctx, &records[i]); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Service) certificateReplayLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			_ = s.ReplayCommitCertificates(s.ctx) // Durable pending survives transport failure.
		}
	}
}
