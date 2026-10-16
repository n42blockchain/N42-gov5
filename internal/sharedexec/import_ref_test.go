package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func TestImportReferenceJoinsOneRealExecutionOverUnix(t *testing.T) {
	chain, b, _ := executionFixture(t)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	socket := startExecutionSocket(t, server)
	client := NewClient(socket, server.Identity())
	defer client.Close()
	header := b.Header().(*block.Header)
	request := ImportRefRequest{Identity: server.Identity(), BlockHash: b.Hash()}
	miss, err := client.ImportRef(context.Background(), request, header)
	if err != nil || !miss.NeedBody || miss.Imported != nil || server.ImportCalls() != 0 {
		t.Fatalf("reference miss created work: %+v %v", miss, err)
	}
	server.mu.Lock()
	count := len(server.jobs)
	server.mu.Unlock()
	if count != 0 {
		t.Fatal("reference miss registered a placeholder")
	}
	var executions atomic.Uint64
	chain.SetExecutedHook(func(types.Hash, types.Hash, types.Hash, uint64, []byte) { executions.Add(1) })
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Import(context.Background(), ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !result.Durable {
		t.Fatalf("real import: %+v %v", result, err)
	}
	// References to completed work must finish even while execution is locked.
	server.execute.Lock()
	defer server.execute.Unlock()
	var group sync.WaitGroup
	failures := make(chan error, 7)
	for range 7 {
		group.Add(1)
		go func() {
			defer group.Done()
			c := NewClient(socket, server.Identity())
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ref, err := c.ImportRef(ctx, request, header)
			if err != nil {
				failures <- err
				return
			}
			if ref.NeedBody || ref.Imported == nil || *ref.Imported != result {
				failures <- errors.New("reference result differs")
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if server.ImportCalls() != 1 {
		t.Fatal("reference performed another import")
	}
	deadline := time.Now().Add(time.Second)
	for executions.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if executions.Load() != 1 {
		t.Fatalf("actual execution hook count=%d", executions.Load())
	}
}

func TestImportReferenceCancellationKeepsRunningOwner(t *testing.T) {
	chain, b, _ := executionFixture(t)
	server, err := New(chain, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, _ := b.Marshal()
	server.execute.Lock()
	var unlock sync.Once
	defer unlock.Do(server.execute.Unlock)
	owner := make(chan error, 1)
	go func() {
		_, err := server.Import(context.Background(), ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
		owner <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		registered := len(server.jobs) == 1
		server.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner not registered")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	refDone := make(chan error, 1)
	go func() {
		_, err := server.ImportRef(ctx, ImportRefRequest{Identity: server.Identity(), BlockHash: b.Hash()})
		refDone <- err
	}()
	select {
	case err := <-refDone:
		t.Fatalf("inflight reference returned early: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if err := <-refDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel reference: %v", err)
	}
	server.mu.Lock()
	count := len(server.jobs)
	server.mu.Unlock()
	if count != 1 {
		t.Fatal("reference cancellation deleted owner")
	}
	unlock.Do(server.execute.Unlock)
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
	if server.ImportCalls() != 1 {
		t.Fatal("reference cancellation duplicated execution")
	}
}

type importRefTransport func(*http.Request) (*http.Response, error)

func (f importRefTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestImportReferenceClientRejectsInvalidResults(t *testing.T) {
	chain, b, _ := executionFixture(t)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, _ := b.Marshal()
	executed, err := server.Import(context.Background(), ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !executed.Durable {
		t.Fatal(err)
	}
	request := ImportRefRequest{Identity: server.Identity(), BlockHash: b.Hash()}
	for _, modify := range []func(*ImportRefResult){
		func(r *ImportRefResult) { r.Identity.Namespace += "wrong" }, func(r *ImportRefResult) { r.BlockHash[0] ^= 1 },
		func(r *ImportRefResult) { r.NeedBody = true }, func(r *ImportRefResult) { r.Imported = nil },
		func(r *ImportRefResult) { r.Imported.ParentHash[0] ^= 1 }, func(r *ImportRefResult) { r.Imported.Number++ },
		func(r *ImportRefResult) { r.Imported.Result.Root[0] ^= 1 }, func(r *ImportRefResult) { r.Imported.Result.ReceiptHash[0] ^= 1 },
		func(r *ImportRefResult) { r.Imported.Result.GasUsed++ }, func(r *ImportRefResult) { r.Imported.Result.Bloom[0] ^= 1 },
	} {
		value := executed
		result := ImportRefResult{ImportRefRequest: request, Imported: &value}
		modify(&result)
		data, _ := json.Marshal(result)
		calls := 0
		client := &Client{identity: server.Identity(), http: &http.Client{Transport: importRefTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path != "/v1/import-ref" {
				t.Error("invalid reply triggered body fallback")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
		})}}
		if _, err := client.ImportRef(context.Background(), request, b.Header().(*block.Header)); err == nil {
			t.Fatal("invalid reference evidence accepted")
		}
		if calls != 1 {
			t.Fatal("invalid response was retried")
		}
	}
}

func TestImportReferenceStrictFramingAndIdentity(t *testing.T) {
	chain, b, _ := executionFixture(t)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	request := ImportRefRequest{Identity: server.Identity(), BlockHash: b.Hash()}
	request.Identity.ChainID = "wrong"
	if _, err := server.ImportRef(context.Background(), request); err == nil {
		t.Fatal("wrong chain reference")
	}
	request.Identity = server.Identity()
	data, _ := json.Marshal(request)
	for _, payload := range [][]byte{append(bytes.Clone(data), '{', '}'), append(bytes.Clone(data[:len(data)-1]), []byte(",\"sender\":\"claim\"}")...), bytes.Repeat([]byte{' '}, 4097)} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/import-ref", bytes.NewReader(payload)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed request status=%d", response.Code)
		}
	}
	if server.ImportCalls() != 0 {
		t.Fatal("reference framing triggered execution")
	}
}

func TestImportReferenceTransientAndEvictedJobsReturnNeedBody(t *testing.T) {
	chain, b, _ := executionFixture(t)
	server, err := New(chain, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, _ := b.Marshal()
	request := ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw}
	if result, err := server.Import(context.Background(), request); err != nil || !result.Durable {
		t.Fatalf("first import: %+v %v", result, err)
	}
	// A new transient job evicts the old completed entry at capacity one, but
	// must not become a cached execution result in its place.
	header := b.Header().(*block.Header)
	header.ParentHash[31] ^= 1
	missing := block.NewBlock(header, b.Transactions()).(*block.Block)
	missingRaw, _ := missing.Marshal()
	transient, err := server.Import(context.Background(), ImportRequest{Identity: server.Identity(), BlockHash: missing.Hash(), BlockRLP: missingRaw})
	if err != nil || transient.State != "accepted" || transient.Executed || transient.Durable {
		t.Fatalf("unknown parent was treated as executed: %+v %v", transient, err)
	}
	for _, hash := range []types.Hash{missing.Hash(), b.Hash()} {
		result, err := server.ImportRef(context.Background(), ImportRefRequest{Identity: server.Identity(), BlockHash: hash})
		if err != nil || !result.NeedBody || result.Imported != nil {
			t.Fatalf("transient/evicted reference retained evidence: %+v %v", result, err)
		}
	}
	// The explicit full-body fallback uses the existing durable DB check; a
	// missing registry entry does not force the already applied block to execute.
	if result, err := server.Import(context.Background(), request); err != nil || !result.Durable || server.ImportCalls() != 1 {
		t.Fatalf("evicted fallback re-executed: %+v %v calls=%d", result, err, server.ImportCalls())
	}
}

func TestImportReferenceBuildOnlyMissDoesNotTakeExecute(t *testing.T) {
	chain, _, _, template, _, _ := unsignedBuildFixture(t)
	server, err := New(chain, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	built, err := server.Build(context.Background(), buildRequest(t, server.Identity(), template))
	if err != nil {
		t.Fatal(err)
	}
	server.execute.Lock()
	var unlock sync.Once
	defer unlock.Do(server.execute.Unlock)
	type lookup struct {
		result ImportRefResult
		err    error
	}
	done := make(chan lookup, 1)
	go func() {
		result, err := server.ImportRef(context.Background(), ImportRefRequest{Identity: server.Identity(), BlockHash: built.BlockHash})
		done <- lookup{result, err}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil || !outcome.result.NeedBody || outcome.result.Imported != nil || server.ImportCalls() != 0 {
			t.Fatalf("unsigned build is not a sealed import: %+v %v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("reference miss acquired execution lock")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.jobs) != 0 {
		t.Fatal("build-only reference miss registered import work")
	}
}
