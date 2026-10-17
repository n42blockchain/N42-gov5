package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func canonicalAccountRequest(s StrictIngressStatus, addresses ...types.Address) CanonicalAccountsRequest {
	return CanonicalAccountsRequest{Identity: s.Identity, Epoch: s.Epoch, ManifestHash: s.ManifestHash, CanonicalHash: s.CanonicalHash, CanonicalNumber: s.CanonicalNumber, Addresses: addresses}
}

func TestCanonicalAccountsUnixRequiresActualCanonicalState(t *testing.T) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 32, 1<<20)
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	status, _ := f.server.StrictIngressStatus()
	missing := types.HexToAddress("0xdeadbeef")
	request := canonicalAccountRequest(status, f.from[0], f.from[1], missing)
	initial, err := client.CanonicalAccounts(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !initial.Accounts[0].Exists || initial.Accounts[0].Nonce != 0 || initial.Accounts[0].Balance == "0" || !initial.Accounts[1].Exists || initial.Accounts[2].Exists || initial.Accounts[2].Nonce != 0 || initial.Accounts[2].Balance != "0" {
		t.Fatalf("existing zero nonce and absent account conflated: %+v", initial)
	}
	if initial.Roots != nil {
		t.Fatal("default response unexpectedly includes optional roots")
	}
	defaultRaw, _ := json.Marshal(initial)
	requestRaw, _ := json.Marshal(request)
	if bytes.Contains(defaultRaw, []byte(`"roots"`)) || bytes.Contains(requestRaw, []byte(`"include_roots"`)) {
		t.Fatal("default wire schema changed")
	}
	withRoots := request
	withRoots.IncludeRoots = true
	rooted, err := client.CanonicalAccounts(ctx, withRoots)
	if err != nil || rooted.Roots == nil || rooted.Roots.Root == (types.Hash{}) || rooted.Roots.Root != rooted.Roots.AppliedRoot {
		t.Fatalf("explicit roots not bound to actual canonical tree: %+v %v", rooted.Roots, err)
	}
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	built, err := client.BuildFromQueue(ctx, queueRequest(t, f, f.template.Header().(*block.Header), 6))
	if err != nil {
		t.Fatal(err)
	}
	sealed := queueSealImport(t, f, client, f.leader, built)
	if got, err := client.CanonicalAccounts(ctx, request); !errors.Is(err, ErrIngressSnapshotChanged) || len(got.Accounts) != 0 {
		t.Fatalf("speculative nonce escaped: %+v %v", got, err)
	}
	commit := CommitRequest{Identity: f.server.Identity(), BlockHash: sealed.Hash(), ParentHash: sealed.ParentHash(), Number: 1}
	if _, err := client.CommitCertified(ctx, commit, queueQC(f.keys, sealed.Hash(), 1, 4)); err == nil {
		t.Fatal("four votes committed state")
	}
	if got, err := client.CanonicalAccounts(ctx, request); !errors.Is(err, ErrIngressSnapshotChanged) || len(got.Accounts) != 0 {
		t.Fatalf("four votes exposed speculative nonce: %+v %v", got, err)
	}
	if _, err := client.CommitCertified(ctx, commit, queueQC(f.keys, sealed.Hash(), 1, 5)); err != nil {
		t.Fatal(err)
	}
	if got, err := client.CanonicalAccounts(ctx, request); !errors.Is(err, ErrIngressSnapshotChanged) || len(got.Accounts) != 0 {
		t.Fatalf("old tuple silently refreshed: %+v %v", got, err)
	}
	status, _ = f.server.StrictIngressStatus()
	request = canonicalAccountRequest(status, request.Addresses...)
	got, err := client.CanonicalAccounts(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	withRoots = request
	withRoots.IncludeRoots = true
	committedRoots, err := client.CanonicalAccounts(ctx, withRoots)
	if err != nil || committedRoots.Roots == nil || committedRoots.Roots.Root != sealed.Header().(*block.Header).Root || committedRoots.Roots.AppliedRoot != committedRoots.Roots.Root {
		t.Fatalf("committed root/account tuple differs: %+v %v", committedRoots.Roots, err)
	}
	for i := 0; i < 2; i++ {
		if !got.Accounts[i].Exists || got.Accounts[i].Nonce != 3 || got.Accounts[i].Balance == initial.Accounts[i].Balance {
			t.Fatalf("canonical account not updated: %+v", got.Accounts[i])
		}
	}
	if got.Accounts[2] != initial.Accounts[2] || f.server.ImportCalls() != 1 || f.server.BuildCalls() != 1 || status.NewlyAdmitted != 6 || status.PendingTransactions != 0 {
		t.Fatal("read-only query changed execution/admission state")
	}
}

func TestCanonicalAccountsRejectsBindingAndMalformedInput(t *testing.T) {
	f := newStrictIngressFixture(t, 32, 1<<20)
	s, _ := f.server.StrictIngressStatus()
	base := canonicalAccountRequest(s, f.from[0])
	for _, change := range []func(*CanonicalAccountsRequest){
		func(r *CanonicalAccountsRequest) { r.Identity.ChainID = "1" },
		func(r *CanonicalAccountsRequest) { r.Epoch[0] ^= 1 },
		func(r *CanonicalAccountsRequest) { r.ManifestHash[0] ^= 1 },
		func(r *CanonicalAccountsRequest) { r.CanonicalNumber++ },
		func(r *CanonicalAccountsRequest) { r.Addresses = nil },
		func(r *CanonicalAccountsRequest) { r.Addresses = []types.Address{f.from[0], f.from[0]} },
		func(r *CanonicalAccountsRequest) { r.Addresses = make([]types.Address, canonicalAccountsLimit+1) },
	} {
		request := base
		change(&request)
		if got, err := f.server.CanonicalAccounts(context.Background(), request); err == nil || len(got.Accounts) != 0 {
			t.Fatalf("invalid query returned state: %+v %v", got, err)
		}
	}
	raw, _ := json.Marshal(base)
	for _, payload := range [][]byte{append(raw, []byte(" {}")...), bytes.Replace(raw, []byte(`"addresses"`), []byte(`"unknown"`), 1), bytes.Repeat([]byte("x"), (64<<10)+1)} {
		response := httptest.NewRecorder()
		f.server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/ingress-accounts", bytes.NewReader(payload)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed request status %d", response.Code)
		}
	}
	f.server.Close()
	if _, err := f.server.CanonicalAccounts(context.Background(), base); err == nil {
		t.Fatal("closed owner returned account state")
	}
}

type canonicalAccountsRoundTrip func(*http.Request) (*http.Response, error)

func (f canonicalAccountsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCanonicalAccountsClientBoundsAndBinding(t *testing.T) {
	identity := corpusDescriptorForTest().Identity
	request := CanonicalAccountsRequest{Identity: identity, Epoch: [16]byte{1}, ManifestHash: types.HexToHash("0x42"), CanonicalHash: identity.GenesisHash, Addresses: make([]types.Address, canonicalAccountsLimit)}
	result := CanonicalAccountsResult{Identity: request.Identity, Epoch: request.Epoch, ManifestHash: request.ManifestHash, CanonicalHash: request.CanonicalHash, Accounts: make([]CanonicalAccount, len(request.Addresses))}
	max := new(uint256.Int).SetAllOne().Dec()
	for i := range request.Addresses {
		request.Addresses[i] = types.BytesToAddress([]byte{byte(i >> 8), byte(i), 1})
		result.Accounts[i] = CanonicalAccount{Address: request.Addresses[i], Exists: true, Nonce: math.MaxUint64, Balance: max, CodeHash: types.HexToHash("0x55")}
	}
	payload, _ := json.Marshal(result)
	if len(payload) <= 256<<10 || len(payload) > canonicalAccountsResponseBytes {
		t.Fatalf("maximum-width fixture does not exercise response bound: %d", len(payload))
	}
	client := &Client{identity: identity, http: &http.Client{Transport: canonicalAccountsRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})}}
	if got, err := client.CanonicalAccounts(context.Background(), request); err != nil || len(got.Accounts) != canonicalAccountsLimit {
		t.Fatalf("maximum legal response failed: %d %v", len(got.Accounts), err)
	}
	for _, change := range []func(*CanonicalAccountsResult){
		func(r *CanonicalAccountsResult) { r.Identity.ChainID = "1" },
		func(r *CanonicalAccountsResult) { r.Epoch[0] ^= 1 },
		func(r *CanonicalAccountsResult) { r.CanonicalHash[0] ^= 1 },
		func(r *CanonicalAccountsResult) { r.Accounts = r.Accounts[:1] },
		func(r *CanonicalAccountsResult) { r.Accounts[0].Address[0] ^= 1 },
		func(r *CanonicalAccountsResult) { r.Accounts[0].Balance = "-1" },
		func(r *CanonicalAccountsResult) { r.Accounts[0].Exists = false },
	} {
		altered := result
		altered.Accounts = append([]CanonicalAccount(nil), result.Accounts...)
		change(&altered)
		payload, _ = json.Marshal(altered)
		if got, err := client.CanonicalAccounts(context.Background(), request); err == nil || len(got.Accounts) != 0 {
			t.Fatalf("altered response accepted: %+v %v", got, err)
		}
	}
	payload = []byte(strings.Repeat("x", canonicalAccountsResponseBytes+1))
	if _, err := client.CanonicalAccounts(context.Background(), request); err == nil {
		t.Fatal("oversize response accepted")
	}
}

func TestCanonicalAccountsOptionalRootsFailClosed(t *testing.T) {
	identity := corpusDescriptorForTest().Identity
	request := CanonicalAccountsRequest{Identity: identity, Epoch: [16]byte{1}, ManifestHash: types.Hash{2}, CanonicalHash: identity.GenesisHash, Addresses: []types.Address{{1}}, IncludeRoots: true}
	response := CanonicalAccountsResult{Identity: identity, Epoch: request.Epoch, ManifestHash: request.ManifestHash, CanonicalHash: request.CanonicalHash, Accounts: []CanonicalAccount{{Address: request.Addresses[0], Exists: true, Balance: "1"}}, Roots: &CanonicalRoots{Root: types.Hash{3}, AppliedRoot: types.Hash{3}}}
	var payload []byte
	client := &Client{identity: identity, http: &http.Client{Transport: canonicalAccountsRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})}}
	payload, _ = json.Marshal(response)
	if got, err := client.CanonicalAccounts(context.Background(), request); err != nil || got.Roots == nil {
		t.Fatalf("matching explicit roots rejected: %+v %v", got, err)
	}
	for _, roots := range []*CanonicalRoots{nil, {}, {Root: types.Hash{3}, AppliedRoot: types.Hash{4}}} {
		response.Roots = roots
		payload, _ = json.Marshal(response)
		got, err := client.CanonicalAccounts(context.Background(), request)
		if err == nil || errors.Is(err, ErrIngressSnapshotChanged) || len(got.Accounts) != 0 || got.Roots != nil {
			t.Fatalf("missing/mismatched roots accepted or retryable: %+v %v", got, err)
		}
	}
}
