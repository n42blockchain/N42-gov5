package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
)

const canonicalAccountsLimit = 1000
const canonicalAccountsResponseBytes = 384 << 10

// CanonicalAccountsRequest pins a read-only recovery query to one owner session
// and canonical state. Multiple batches are usable together only at this same
// tuple. Volatile admission ACKs never determine the returned nonces.
type CanonicalAccountsRequest struct {
	Identity        Identity        `json:"identity"`
	Epoch           [16]byte        `json:"epoch"`
	ManifestHash    types.Hash      `json:"manifest_hash"`
	CanonicalHash   types.Hash      `json:"canonical_hash"`
	CanonicalNumber uint64          `json:"canonical_number"`
	Addresses       []types.Address `json:"addresses"`
}

type CanonicalAccount struct {
	Address  types.Address `json:"address"`
	Exists   bool          `json:"exists"`
	Nonce    uint64        `json:"nonce"`
	Balance  string        `json:"balance"`
	CodeHash types.Hash    `json:"code_hash"`
}

type CanonicalAccountsResult struct {
	Identity        Identity           `json:"identity"`
	Epoch           [16]byte           `json:"epoch"`
	ManifestHash    types.Hash         `json:"manifest_hash"`
	CanonicalHash   types.Hash         `json:"canonical_hash"`
	CanonicalNumber uint64             `json:"canonical_number"`
	Accounts        []CanonicalAccount `json:"accounts"`
}

func validCanonicalAccountsRequest(request CanonicalAccountsRequest, identity Identity) bool {
	if request.Identity != identity || request.Epoch == ([16]byte{}) || request.ManifestHash == (types.Hash{}) || request.CanonicalHash == (types.Hash{}) || (request.CanonicalNumber == 0 && request.CanonicalHash != identity.GenesisHash) || len(request.Addresses) < 1 || len(request.Addresses) > canonicalAccountsLimit {
		return false
	}
	seen := make(map[types.Address]bool, len(request.Addresses))
	for _, address := range request.Addresses {
		if seen[address] {
			return false
		}
		seen[address] = true
	}
	return true
}

// CanonicalAccounts returns actual canonical account state only. It does not
// admit transactions, reconcile an ambiguous frame or authorize source replay.
func (s *Server) CanonicalAccounts(ctx context.Context, request CanonicalAccountsRequest) (CanonicalAccountsResult, error) {
	var out CanonicalAccountsResult
	if !validCanonicalAccountsRequest(request, s.identity) {
		return out, errors.New("invalid bounded canonical account request")
	}
	request.Addresses = append([]types.Address(nil), request.Addresses...)
	s.mu.Lock()
	q := s.ingress
	if s.closed || q == nil {
		s.mu.Unlock()
		return out, errors.New("canonical account recovery unavailable")
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	s.execute.Lock()
	defer s.execute.Unlock()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := s.ctx.Err(); err != nil {
		return out, err
	}
	q.mu.Lock()
	if q.closed || q.epoch != request.Epoch || q.manifestHash != request.ManifestHash {
		q.mu.Unlock()
		return out, errors.New("canonical account session binding mismatch")
	}
	stamp := q.stamp
	ready := !q.reconciling && stamp.hash == request.CanonicalHash && stamp.number == request.CanonicalNumber
	q.mu.Unlock()
	if !ready {
		return out, ErrIngressSnapshotChanged
	}
	snapshot, err := s.chain.ReadPerformanceCanonicalAccounts(ctx, request.Addresses)
	if errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) {
		return out, ErrIngressSnapshotChanged
	}
	if err != nil {
		return out, err
	}
	if snapshot.Hash != request.CanonicalHash || snapshot.Number != request.CanonicalNumber {
		return out, ErrIngressSnapshotChanged
	}
	q.mu.Lock()
	stillReady := !q.closed && !q.reconciling && q.stamp == stamp && s.ctx.Err() == nil
	q.mu.Unlock()
	if !stillReady {
		return out, ErrIngressSnapshotChanged
	}
	out = CanonicalAccountsResult{Identity: s.identity, Epoch: q.epoch, ManifestHash: q.manifestHash, CanonicalHash: snapshot.Hash, CanonicalNumber: snapshot.Number, Accounts: make([]CanonicalAccount, len(request.Addresses))}
	for i, address := range request.Addresses {
		a := snapshot.Accounts[address]
		out.Accounts[i] = CanonicalAccount{Address: address, Exists: snapshot.Exists[address], Nonce: a.Nonce, Balance: a.Balance.Dec(), CodeHash: a.CodeHash}
	}
	return out, nil
}

func (s *Server) serveCanonicalAccounts(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var request CanonicalAccountsRequest
	if err := d.Decode(&request); err != nil {
		http.Error(w, "invalid canonical account request", http.StatusBadRequest)
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		http.Error(w, "one canonical account request required", http.StatusBadRequest)
		return
	}
	result, err := s.CanonicalAccounts(r.Context(), request)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, ErrIngressSnapshotChanged) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (c *Client) CanonicalAccounts(ctx context.Context, request CanonicalAccountsRequest) (CanonicalAccountsResult, error) {
	var out CanonicalAccountsResult
	if !validCanonicalAccountsRequest(request, c.identity) {
		return out, errors.New("invalid client canonical account request")
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > 64<<10 {
		return out, errors.New("invalid canonical account request size")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/ingress-accounts", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return out, ErrIngressSnapshotChanged
	}
	if response.StatusCode != http.StatusOK {
		return out, errors.New("execution owner rejected canonical account query")
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, canonicalAccountsResponseBytes+1))
	if err != nil || len(raw) > canonicalAccountsResponseBytes {
		return out, errors.New("invalid canonical account response size")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return CanonicalAccountsResult{}, err
	}
	if err := d.Decode(new(any)); err != io.EOF || out.Identity != request.Identity || out.Epoch != request.Epoch || out.ManifestHash != request.ManifestHash || out.CanonicalHash != request.CanonicalHash || out.CanonicalNumber != request.CanonicalNumber || len(out.Accounts) != len(request.Addresses) {
		return CanonicalAccountsResult{}, errors.New("canonical account response binding mismatch")
	}
	empty := account.NewAccount()
	for i, a := range out.Accounts {
		balance, ok := new(big.Int).SetString(a.Balance, 10)
		if a.Address != request.Addresses[i] || !ok || balance.Sign() < 0 || balance.BitLen() > 256 || balance.String() != a.Balance {
			return CanonicalAccountsResult{}, errors.New("canonical account response account mismatch")
		}
		if !a.Exists && (a.Nonce != 0 || balance.Sign() != 0 || a.CodeHash != empty.CodeHash) {
			return CanonicalAccountsResult{}, errors.New("missing canonical account carried state")
		}
	}
	return out, nil
}
