package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

type stubQueryBackend struct {
	blockFn       func(context.Context, BlockArgs) (*Block, error)
	transactionFn func(context.Context, types.Hash) (*Transaction, error)
	accountFn     func(context.Context, types.Address, *uint64) (*Account, error)
	logsFn        func(context.Context, LogFilter) ([]*Log, error)
}

func (s *stubQueryBackend) Block(ctx context.Context, args BlockArgs) (*Block, error) {
	if s.blockFn == nil {
		return nil, errors.New("unexpected block query")
	}
	return s.blockFn(ctx, args)
}

func (s *stubQueryBackend) Transaction(ctx context.Context, hash types.Hash) (*Transaction, error) {
	if s.transactionFn == nil {
		return nil, errors.New("unexpected transaction query")
	}
	return s.transactionFn(ctx, hash)
}

func (s *stubQueryBackend) Account(ctx context.Context, addr types.Address, blockNr *uint64) (*Account, error) {
	if s.accountFn == nil {
		return nil, errors.New("unexpected account query")
	}
	return s.accountFn(ctx, addr, blockNr)
}

func (s *stubQueryBackend) Logs(ctx context.Context, filter LogFilter) ([]*Log, error) {
	if s.logsFn == nil {
		return nil, errors.New("unexpected logs query")
	}
	return s.logsFn(ctx, filter)
}

func TestServeHTTPRejectsNonPOST(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{})

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusMethodNotAllowed)
	}
	assertGraphQLErrorContains(t, resp.Body.Bytes(), "only POST method is supported")
}

func TestServeHTTPRejectsInvalidJSON(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{})

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader("{"))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	assertGraphQLErrorContains(t, resp.Body.Bytes(), "invalid JSON request body")
}

func TestServeHTTPRejectsMissingQuery(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{})

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"variables":{"number":1}}`))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	assertGraphQLErrorContains(t, resp.Body.Bytes(), "missing query field")
}

func TestServeHTTPRejectsOversizedBody(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{})

	body := `{"query":"` + strings.Repeat("a", maxRequestBodySize) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusRequestEntityTooLarge)
	}
	assertGraphQLErrorContains(t, resp.Body.Bytes(), "request body exceeds 1048576 bytes")
}

func TestServeHTTPReturnsBackendErrorsInGraphQLPayload(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{
		blockFn: func(context.Context, BlockArgs) (*Block, error) {
			return nil, errors.New("boom")
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"query { block { number } }"}`))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	assertGraphQLErrorContains(t, resp.Body.Bytes(), "boom")
}

func TestExecuteQueryRoutesBlockQueryWithVariables(t *testing.T) {
	var called bool
	handler := NewHandler(&stubQueryBackend{
		blockFn: func(_ context.Context, args BlockArgs) (*Block, error) {
			called = true
			if args.Number == nil || uint64(*args.Number) != 123 {
				t.Fatalf("block number = %v, want 123", args.Number)
			}
			return &Block{Number: hexutil.Uint64(123)}, nil
		},
	})

	data, err := handler.executeQuery(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query:     `query { block(number: $number) { number } }`,
		Variables: map[string]interface{}{"number": "0x7b"},
	})
	if err != nil {
		t.Fatalf("executeQuery() error = %v", err)
	}
	if !called {
		t.Fatal("block backend was not called")
	}
	assertTopLevelKeyPresent(t, data, "block")
}

func TestExecuteQueryRoutesTransactionQueryWithInlineHash(t *testing.T) {
	wantHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	var called bool
	handler := NewHandler(&stubQueryBackend{
		transactionFn: func(_ context.Context, hash types.Hash) (*Transaction, error) {
			called = true
			if hash != wantHash {
				t.Fatalf("hash = %s, want %s", hash.Hex(), wantHash.Hex())
			}
			return &Transaction{Hash: hash}, nil
		},
	})

	data, err := handler.executeQuery(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query: `query { transaction(hash: "0x1111111111111111111111111111111111111111111111111111111111111111") { hash } }`,
	})
	if err != nil {
		t.Fatalf("executeQuery() error = %v", err)
	}
	if !called {
		t.Fatal("transaction backend was not called")
	}
	assertTopLevelKeyPresent(t, data, "transaction")
}

func TestExecuteQueryRoutesAccountQueryWithVariables(t *testing.T) {
	wantAddr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	var called bool
	handler := NewHandler(&stubQueryBackend{
		accountFn: func(_ context.Context, addr types.Address, blockNr *uint64) (*Account, error) {
			called = true
			if addr != wantAddr {
				t.Fatalf("address = %s, want %s", addr.Hex(), wantAddr.Hex())
			}
			if blockNr == nil || *blockNr != 7 {
				t.Fatalf("blockNr = %v, want 7", blockNr)
			}
			return &Account{Address: addr}, nil
		},
	})

	data, err := handler.executeQuery(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query: `query { account(address: $address, blockNumber: $blockNumber) { address } }`,
		Variables: map[string]interface{}{
			"address":     wantAddr.Hex(),
			"blockNumber": float64(7),
		},
	})
	if err != nil {
		t.Fatalf("executeQuery() error = %v", err)
	}
	if !called {
		t.Fatal("account backend was not called")
	}
	assertTopLevelKeyPresent(t, data, "account")
}

func TestExecuteQueryRoutesLogsQueryWithVariables(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	topic0 := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	topic1 := types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	var called bool
	handler := NewHandler(&stubQueryBackend{
		logsFn: func(_ context.Context, filter LogFilter) ([]*Log, error) {
			called = true
			if filter.FromBlock == nil || uint64(*filter.FromBlock) != 1 {
				t.Fatalf("fromBlock = %v, want 1", filter.FromBlock)
			}
			if filter.ToBlock == nil || uint64(*filter.ToBlock) != 3 {
				t.Fatalf("toBlock = %v, want 3", filter.ToBlock)
			}
			if len(filter.Addresses) != 1 || filter.Addresses[0] != addr {
				t.Fatalf("addresses = %v, want [%s]", filter.Addresses, addr.Hex())
			}
			if len(filter.Topics) != 2 || len(filter.Topics[0]) != 1 || len(filter.Topics[1]) != 2 {
				t.Fatalf("topics = %#v", filter.Topics)
			}
			if filter.Topics[0][0] != topic0 || filter.Topics[1][0] != topic0 || filter.Topics[1][1] != topic1 {
				t.Fatalf("topics = %#v", filter.Topics)
			}
			return []*Log{}, nil
		},
	})

	data, err := handler.executeQuery(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query: `query { logs(fromBlock: $fromBlock, toBlock: $toBlock, addresses: $addresses, topics: $topics) { blockNumber } }`,
		Variables: map[string]interface{}{
			"fromBlock": float64(1),
			"toBlock":   "0x3",
			"addresses": []interface{}{addr.Hex()},
			"topics": []interface{}{
				topic0.Hex(),
				[]interface{}{topic0.Hex(), topic1.Hex()},
			},
		},
	})
	if err != nil {
		t.Fatalf("executeQuery() error = %v", err)
	}
	if !called {
		t.Fatal("logs backend was not called")
	}
	assertTopLevelKeyPresent(t, data, "logs")
}

func TestExecuteQueryRejectsUnsupportedQuery(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{})

	_, err := handler.executeQuery(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query: `query { chainId }`,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported query") {
		t.Fatalf("executeQuery() error = %v, want unsupported query", err)
	}
}

func TestResolveBlockRejectsMalformedHashVariable(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{
		blockFn: func(context.Context, BlockArgs) (*Block, error) {
			t.Fatal("block backend should not be called")
			return nil, nil
		},
	})

	_, err := handler.resolveBlock(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query:     `query { block(hash: $hash) { number } }`,
		Variables: map[string]interface{}{"hash": "0x1234"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid block hash argument") {
		t.Fatalf("resolveBlock() error = %v, want invalid block hash argument", err)
	}
}

func TestResolveTransactionRejectsMalformedInlineHash(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{
		transactionFn: func(context.Context, types.Hash) (*Transaction, error) {
			t.Fatal("transaction backend should not be called")
			return nil, nil
		},
	})

	_, err := handler.resolveTransaction(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query: `query { transaction(hash: "0x12") { hash } }`,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid transaction hash argument") {
		t.Fatalf("resolveTransaction() error = %v, want invalid transaction hash argument", err)
	}
}

func TestResolveAccountRejectsMalformedAddressVariable(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{
		accountFn: func(context.Context, types.Address, *uint64) (*Account, error) {
			t.Fatal("account backend should not be called")
			return nil, nil
		},
	})

	_, err := handler.resolveAccount(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query:     `query { account(address: $address) { address } }`,
		Variables: map[string]interface{}{"address": "0x1234"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid account address argument") {
		t.Fatalf("resolveAccount() error = %v, want invalid account address argument", err)
	}
}

func TestResolveLogsRejectsMalformedTopicVariable(t *testing.T) {
	handler := NewHandler(&stubQueryBackend{
		logsFn: func(context.Context, LogFilter) ([]*Log, error) {
			t.Fatal("logs backend should not be called")
			return nil, nil
		},
	})

	_, err := handler.resolveLogs(httptest.NewRequest(http.MethodPost, "/graphql", nil), GraphQLRequest{
		Query: `query { logs(topics: $topics) { blockNumber } }`,
		Variables: map[string]interface{}{
			"topics": []interface{}{"0x1234"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid logs topics argument") {
		t.Fatalf("resolveLogs() error = %v, want invalid logs topics argument", err)
	}
}

func TestExtractUint64VariableRejectsFractionalJSONNumber(t *testing.T) {
	_, ok, err := extractUint64Variable(map[string]interface{}{"number": 1.5}, "number")
	if !ok {
		t.Fatal("expected variable to be present")
	}
	if err == nil || !strings.Contains(err.Error(), "non-negative integer") {
		t.Fatalf("extractUint64Variable() error = %v, want non-negative integer", err)
	}
}

func TestExtractInlineNumberRejectsInvalidToken(t *testing.T) {
	_, ok, err := extractInlineNumber(`query { block(number: "abc") { number } }`)
	if !ok {
		t.Fatal("expected inline number to be present")
	}
	if err == nil {
		t.Fatal("expected invalid inline number error")
	}
}

func assertGraphQLErrorContains(t *testing.T, body []byte, substring string) {
	t.Helper()

	var resp GraphQLResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if len(resp.Errors) == 0 {
		t.Fatalf("response errors = 0, want message containing %q", substring)
	}
	if !strings.Contains(resp.Errors[0].Message, substring) {
		t.Fatalf("error message = %q, want substring %q", resp.Errors[0].Message, substring)
	}
}

func assertTopLevelKeyPresent(t *testing.T, data interface{}, key string) {
	t.Helper()

	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("data type = %T, want map[string]interface{}", data)
	}
	if _, ok := m[key]; !ok {
		t.Fatalf("top-level key %q missing from %#v", key, m)
	}
}
