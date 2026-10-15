package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

// Each item adds base64 padding plus quotes/comma beyond the aggregate 4/3
// expansion; reserve eight bytes per entry as well as fixed identity framing.
const maxAdmissionJSON = sharedexec.MaxBlockBytes*4/3 + sharedexec.MaxTransactions*8 + 4096

// Bound element count during decoding, before a large array can allocate an
// unbounded slice of empty entries. Outer HTTP framing bounds the JSON bytes.
type WireTransactions [][]byte

func (transactions *WireTransactions) UnmarshalJSON(raw []byte) error {
	if len(raw) > maxAdmissionJSON {
		return errors.New("transaction array JSON budget exceeded")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') {
		return errors.New("transaction array required")
	}
	var items WireTransactions
	wireBytes := 0
	for decoder.More() {
		if len(items) >= sharedexec.MaxTransactions {
			return errors.New("transaction array count exceeded")
		}
		var wire []byte
		if err := decoder.Decode(&wire); err != nil {
			return err
		}
		if len(wire) == 0 || len(wire) > sharedexec.MaxBlockBytes || wireBytes > sharedexec.MaxBlockBytes-len(wire) {
			return errors.New("transaction array wire budget exceeded")
		}
		wireBytes += len(wire)
		items = append(items, wire)
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
		return errors.New("invalid transaction array end")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid transaction array framing")
	}
	*transactions = items
	return nil
}

type StatusRequest struct {
	Identity sharedexec.Identity `json:"identity"`
}
type StatusResult struct {
	CanonicalProgress
	QueuedTransactions int    `json:"queued_transactions"`
	ProducerRunning    bool   `json:"producer_running"`
	LastProducerError  string `json:"last_producer_error,omitempty"`
}
type TransactionsRequest struct {
	Identity        sharedexec.Identity `json:"identity"`
	CanonicalHash   types.Hash          `json:"canonical_hash"`
	CanonicalNumber uint64              `json:"canonical_number"`
	Transactions    WireTransactions    `json:"transactions"`
}
type TransactionsResult struct {
	Identity           sharedexec.Identity `json:"identity"`
	CanonicalHash      types.Hash          `json:"canonical_hash"`
	CanonicalNumber    uint64              `json:"canonical_number"`
	Received           int                 `json:"received"`
	QueuedTransactions int                 `json:"queued_transactions"`
}

// API is a private Unix ingress for a concrete metadata replica/producer pair.
// Receipt is volatile queue admission, never a transaction finality response.
type API struct {
	replica   *Replica
	producer  *Producer
	admission sync.Mutex
	lifecycle sync.Mutex
	closed    bool
	handlers  sync.WaitGroup
}

func NewAPI(replica *Replica, producer *Producer) (*API, error) {
	if replica == nil || producer == nil || producer.replica != replica {
		return nil, errors.New("validator API requires its actual replica/producer pair")
	}
	return &API{replica: replica, producer: producer}, nil
}

// Stop is called after the HTTP server closes connections, canceling contexts
// and unblocking decoders. No handler can touch metadata after Stop returns.
func (a *API) Stop() { a.lifecycle.Lock(); a.closed = true; a.lifecycle.Unlock(); a.handlers.Wait() }

func (a *API) status(ctx context.Context) (StatusResult, error) {
	progress, err := a.replica.CanonicalProgress(ctx)
	if err != nil {
		return StatusResult{}, err
	}
	a.producer.mu.Lock()
	defer a.producer.mu.Unlock()
	return StatusResult{CanonicalProgress: progress, QueuedTransactions: len(a.producer.pending), ProducerRunning: a.producer.started && !a.producer.closed, LastProducerError: a.producer.lastError}, nil
}

func decodeAPI(w http.ResponseWriter, r *http.Request, max int64, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("one validator API request required")
	}
	return nil
}
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.lifecycle.Lock()
	if a.closed {
		a.lifecycle.Unlock()
		http.Error(w, "validator API stopped", http.StatusServiceUnavailable)
		return
	}
	a.handlers.Add(1)
	a.lifecycle.Unlock()
	defer a.handlers.Done()
	defer r.Body.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/v1/status" {
		var request StatusRequest
		if err := decodeAPI(w, r, 4096, &request); err != nil || request.Identity != a.replica.identity {
			http.Error(w, "invalid validator status identity/request", http.StatusBadRequest)
			return
		}
		result, err := a.status(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	if r.URL.Path != "/v1/transactions" {
		http.NotFound(w, r)
		return
	}
	// Serialize large base64 decoding, signature admission and response framing.
	a.admission.Lock()
	defer a.admission.Unlock()
	if err := ctx.Err(); err != nil {
		http.Error(w, "admission canceled", http.StatusRequestTimeout)
		return
	}
	var request TransactionsRequest
	if err := decodeAPI(w, r, maxAdmissionJSON, &request); err != nil || request.Identity != a.replica.identity || len(request.Transactions) == 0 {
		http.Error(w, "invalid transaction admission identity/request", http.StatusBadRequest)
		return
	}
	if err := a.producer.SubmitRawTransactionsAtHead(ctx, request.Transactions, request.CanonicalHash, request.CanonicalNumber); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, ErrAdmissionHeadChanged) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	a.producer.mu.Lock()
	queued := len(a.producer.pending)
	a.producer.mu.Unlock()
	result := TransactionsResult{Identity: a.replica.identity, CanonicalHash: request.CanonicalHash, CanonicalNumber: request.CanonicalNumber, Received: len(request.Transactions), QueuedTransactions: queued}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (a *API) ListenUnix(path string) (*http.Server, net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, nil, errors.New("validator API socket requires absolute canonical path")
	}
	dir := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return nil, nil, errors.New("validator API directory must exist without symlinks")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, nil, errors.New("validator API directory must be private")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, nil, errors.New("validator API directory must belong to current user")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, nil, errors.New("validator API socket already exists")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, nil, err
	}
	server := &http.Server{Handler: a, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return a.replica.ctx }}
	return server, listener, nil
}

type APIClient struct {
	identity sharedexec.Identity
	http     *http.Client
}

func NewAPIClient(socket string, identity sharedexec.Identity) *APIClient {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &APIClient{identity: identity, http: &http.Client{Transport: transport, Timeout: 35 * time.Second}}
}
func (c *APIClient) Close() { c.http.CloseIdleConnections() }
func (c *APIClient) call(ctx context.Context, path string, request, result any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(raw) > maxAdmissionJSON {
		return errors.New("validator client request too large")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusConflict {
			return ErrAdmissionHeadChanged
		}
		return errors.New("validator API rejected request")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New("validator API response too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("validator API trailing response")
	}
	return nil
}
func (c *APIClient) Status(ctx context.Context) (StatusResult, error) {
	var result StatusResult
	if err := c.call(ctx, "/v1/status", StatusRequest{Identity: c.identity}, &result); err != nil {
		return result, err
	}
	if result.Identity != c.identity || result.BlockHash == (types.Hash{}) || result.QueuedTransactions < 0 || result.QueuedTransactions > sharedexec.MaxTransactions || (result.Number == 0 && (result.BlockHash != c.identity.GenesisHash || result.CommittedExecutedTransactions != 0)) {
		return result, errors.New("validator status response binding invalid")
	}
	return result, nil
}
func (c *APIClient) Submit(ctx context.Context, head CanonicalProgress, transactions [][]byte) (TransactionsResult, error) {
	var result TransactionsResult
	if head.Identity != c.identity || head.BlockHash == (types.Hash{}) || len(transactions) == 0 || len(transactions) > sharedexec.MaxTransactions {
		return result, errors.New("validator admission identity/head/count invalid")
	}
	count := 0
	for _, raw := range transactions {
		if len(raw) == 0 || len(raw) > sharedexec.MaxBlockBytes || count > sharedexec.MaxBlockBytes-len(raw) {
			return result, errors.New("validator client wire budget exceeded")
		}
		count += len(raw)
	}
	request := TransactionsRequest{Identity: c.identity, CanonicalHash: head.BlockHash, CanonicalNumber: head.Number, Transactions: WireTransactions(transactions)}
	if err := c.call(ctx, "/v1/transactions", request, &result); err != nil {
		return result, err
	}
	if result.Identity != c.identity || result.CanonicalHash != head.BlockHash || result.CanonicalNumber != head.Number || result.Received != len(transactions) || result.QueuedTransactions < 0 || result.QueuedTransactions > sharedexec.MaxTransactions {
		return result, errors.New("validator admission response binding invalid")
	}
	return result, nil
}
