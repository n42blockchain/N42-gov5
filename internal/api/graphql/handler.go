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

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// rootFieldPatterns match root-level GraphQL field names with word boundaries
// to prevent ambiguous routing (e.g., "blockTransaction" matching "block").
var (
	blockFieldRe       = regexp.MustCompile(`\bblock\b\s*[({]`)
	transactionFieldRe = regexp.MustCompile(`\btransaction\b\s*[({]`)
	accountFieldRe     = regexp.MustCompile(`\baccount\b\s*[({]`)
	logsFieldRe        = regexp.MustCompile(`\blogs\b\s*[({]`)
)

// maxRequestBodySize limits the size of incoming GraphQL request bodies (1 MB).
const maxRequestBodySize = 1 << 20

// Handler is the HTTP handler for the GraphQL endpoint. It accepts POST
// requests with a JSON body conforming to the standard GraphQL over HTTP
// specification and delegates query resolution to the embedded Resolver.
type Handler struct {
	backend queryBackend
}

type queryBackend interface {
	Block(context.Context, BlockArgs) (*Block, error)
	Transaction(context.Context, types.Hash) (*Transaction, error)
	Account(context.Context, types.Address, *uint64) (*Account, error)
	Logs(context.Context, LogFilter) ([]*Log, error)
}

// NewHandler creates a new GraphQL HTTP handler.
func NewHandler(backend queryBackend) *Handler {
	return &Handler{backend: backend}
}

// ServeHTTP implements the http.Handler interface.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST method is supported")
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodySize))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1048576 bytes")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req GraphQLRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}

	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "missing query field")
		return
	}

	data, err := h.executeQuery(r, req)
	if err != nil {
		resp := GraphQLResponse{
			Errors: []*GraphQLError{{Message: err.Error()}},
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp := GraphQLResponse{Data: data}
	writeJSON(w, http.StatusOK, resp)
}

// executeQuery parses the simplified GraphQL query and routes it to the
// appropriate resolver method. It supports a practical subset of GraphQL:
// block, transaction, account, and logs queries.
func (h *Handler) executeQuery(r *http.Request, req GraphQLRequest) (interface{}, error) {
	query := normalizeQuery(req.Query)

	switch {
	case transactionFieldRe.MatchString(query):
		return h.resolveTransaction(r, req)
	case blockFieldRe.MatchString(query):
		return h.resolveBlock(r, req)
	case accountFieldRe.MatchString(query):
		return h.resolveAccount(r, req)
	case logsFieldRe.MatchString(query):
		return h.resolveLogs(r, req)
	default:
		return nil, fmt.Errorf("unsupported query: only block, transaction, account, and logs queries are supported")
	}
}

// resolveBlock handles block queries. It supports queries with optional
// number or hash arguments.
func (h *Handler) resolveBlock(r *http.Request, req GraphQLRequest) (interface{}, error) {
	args := BlockArgs{}

	if req.Variables != nil {
		if num, ok, err := extractUint64Variable(req.Variables, "number"); err != nil {
			return nil, fmt.Errorf("invalid block number argument: %w", err)
		} else if ok {
			n := hexutil.Uint64(num)
			args.Number = &n
		}
		if hash, ok, err := extractHashVariable(req.Variables, "hash"); err != nil {
			return nil, fmt.Errorf("invalid block hash argument: %w", err)
		} else if ok {
			args.Hash = &hash
		}
	}

	// Also try to parse inline arguments from the query string.
	if args.Number == nil && args.Hash == nil {
		if num, ok, err := extractInlineNumber(req.Query); err != nil {
			return nil, fmt.Errorf("invalid block number argument: %w", err)
		} else if ok {
			n := hexutil.Uint64(num)
			args.Number = &n
		}
		if hash, ok, err := extractInlineHash(req.Query); err != nil {
			return nil, fmt.Errorf("invalid block hash argument: %w", err)
		} else if ok {
			args.Hash = &hash
		}
	}

	result, err := h.backend.Block(r.Context(), args)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{"block": result}, nil
}

// resolveTransaction handles transaction queries. It requires a hash argument.
func (h *Handler) resolveTransaction(r *http.Request, req GraphQLRequest) (interface{}, error) {
	var hash types.Hash
	found := false

	if req.Variables != nil {
		if parsedHash, ok, err := extractHashVariable(req.Variables, "hash"); err != nil {
			return nil, fmt.Errorf("invalid transaction hash argument: %w", err)
		} else if ok {
			hash = parsedHash
			found = true
		}
	}

	if !found {
		if parsedHash, ok, err := extractInlineHash(req.Query); err != nil {
			return nil, fmt.Errorf("invalid transaction hash argument: %w", err)
		} else if ok {
			hash = parsedHash
			found = true
		}
	}

	if !found {
		return nil, fmt.Errorf("transaction query requires a hash argument")
	}

	result, err := h.backend.Transaction(r.Context(), hash)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{"transaction": result}, nil
}

// resolveAccount handles account queries. It requires an address argument
// and supports an optional block argument.
func (h *Handler) resolveAccount(r *http.Request, req GraphQLRequest) (interface{}, error) {
	var addr types.Address
	addrFound := false
	var blockNr *uint64

	if req.Variables != nil {
		if parsedAddr, ok, err := extractAddressVariable(req.Variables, "address"); err != nil {
			return nil, fmt.Errorf("invalid account address argument: %w", err)
		} else if ok {
			addr = parsedAddr
			addrFound = true
		}
		if num, ok, err := extractUint64Variable(req.Variables, "blockNumber"); err != nil {
			return nil, fmt.Errorf("invalid account blockNumber argument: %w", err)
		} else if ok {
			blockNr = &num
		}
	}

	if !addrFound {
		if parsedAddr, ok, err := extractInlineAddress(req.Query); err != nil {
			return nil, fmt.Errorf("invalid account address argument: %w", err)
		} else if ok {
			addr = parsedAddr
			addrFound = true
		}
	}

	if !addrFound {
		return nil, fmt.Errorf("account query requires an address argument")
	}

	result, err := h.backend.Account(r.Context(), addr, blockNr)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{"account": result}, nil
}

// resolveLogs handles log filter queries.
func (h *Handler) resolveLogs(r *http.Request, req GraphQLRequest) (interface{}, error) {
	filter := LogFilter{}

	if req.Variables != nil {
		if fb, ok, err := extractUint64Variable(req.Variables, "fromBlock"); err != nil {
			return nil, fmt.Errorf("invalid logs fromBlock argument: %w", err)
		} else if ok {
			n := hexutil.Uint64(fb)
			filter.FromBlock = &n
		}
		if tb, ok, err := extractUint64Variable(req.Variables, "toBlock"); err != nil {
			return nil, fmt.Errorf("invalid logs toBlock argument: %w", err)
		} else if ok {
			n := hexutil.Uint64(tb)
			filter.ToBlock = &n
		}
		if addresses, ok, err := extractAddressSliceVariable(req.Variables, "addresses"); err != nil {
			return nil, fmt.Errorf("invalid logs addresses argument: %w", err)
		} else if ok {
			filter.Addresses = addresses
		}
		if topics, ok, err := extractTopicGroupsVariable(req.Variables, "topics"); err != nil {
			return nil, fmt.Errorf("invalid logs topics argument: %w", err)
		} else if ok {
			filter.Topics = topics
		}
	}

	result, err := h.backend.Logs(r.Context(), filter)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{"logs": result}, nil
}

// normalizeQuery lowercases and trims whitespace from the query string to
// simplify root field detection.
func normalizeQuery(q string) string {
	return strings.TrimSpace(strings.ToLower(q))
}

// extractUint64Variable extracts a uint64 value from a variables map,
// supporting both JSON number and hex-string representations.
func extractUint64Variable(vars map[string]interface{}, key string) (uint64, bool, error) {
	v, ok := vars[key]
	if !ok {
		return 0, false, nil
	}
	switch val := v.(type) {
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) || val < 0 || val != math.Trunc(val) || val > math.MaxUint64 {
			return 0, true, fmt.Errorf("must be a non-negative integer")
		}
		return uint64(val), true, nil
	case string:
		// Support hex (0x...) and decimal strings.
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, "0x") || strings.HasPrefix(val, "0X") {
			n, err := strconv.ParseUint(val[2:], 16, 64)
			if err != nil {
				return 0, true, err
			}
			return n, true, nil
		}
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return 0, true, err
		}
		return n, true, nil
	default:
		return 0, true, fmt.Errorf("must be a JSON number or string")
	}
}

// extractInlineNumber attempts to find a number argument inside a GraphQL
// query string, e.g. block(number: 123) or block(number: "0x7b").
func extractInlineNumber(query string) (uint64, bool, error) {
	token, ok := extractInlineToken(query, "number")
	if !ok {
		return 0, false, nil
	}
	if strings.HasPrefix(token, "0x") || strings.HasPrefix(token, "0X") {
		n, err := strconv.ParseUint(token[2:], 16, 64)
		if err != nil {
			return 0, true, err
		}
		return n, true, nil
	}
	n, err := strconv.ParseUint(token, 10, 64)
	if err != nil {
		return 0, true, err
	}
	return n, true, nil
}

// extractInlineHash attempts to find a hash argument inside a GraphQL query
// string, e.g. block(hash: "0xabc...").
func extractInlineHash(query string) (types.Hash, bool, error) {
	token, ok := extractInlineToken(query, "hash")
	if !ok {
		return types.Hash{}, false, nil
	}
	hash, err := parseHash(token)
	if err != nil {
		return types.Hash{}, true, err
	}
	return hash, true, nil
}

// extractInlineAddress attempts to find an address argument inside a GraphQL
// query string, e.g. account(address: "0xabc...").
func extractInlineAddress(query string) (types.Address, bool, error) {
	token, ok := extractInlineToken(query, "address")
	if !ok {
		return types.Address{}, false, nil
	}
	addr, err := parseAddress(token)
	if err != nil {
		return types.Address{}, true, err
	}
	return addr, true, nil
}

func extractHashVariable(vars map[string]interface{}, key string) (types.Hash, bool, error) {
	raw, ok := vars[key]
	if !ok {
		return types.Hash{}, false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return types.Hash{}, true, fmt.Errorf("must be a hex string")
	}
	hash, err := parseHash(value)
	if err != nil {
		return types.Hash{}, true, err
	}
	return hash, true, nil
}

func extractAddressVariable(vars map[string]interface{}, key string) (types.Address, bool, error) {
	raw, ok := vars[key]
	if !ok {
		return types.Address{}, false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return types.Address{}, true, fmt.Errorf("must be a hex string")
	}
	addr, err := parseAddress(value)
	if err != nil {
		return types.Address{}, true, err
	}
	return addr, true, nil
}

func extractAddressSliceVariable(vars map[string]interface{}, key string) ([]types.Address, bool, error) {
	raw, ok := vars[key]
	if !ok {
		return nil, false, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, true, fmt.Errorf("must be an array of addresses")
	}
	addresses := make([]types.Address, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, true, fmt.Errorf("must contain only address strings")
		}
		addr, err := parseAddress(value)
		if err != nil {
			return nil, true, err
		}
		addresses = append(addresses, addr)
	}
	return addresses, true, nil
}

func extractTopicGroupsVariable(vars map[string]interface{}, key string) ([][]types.Hash, bool, error) {
	raw, ok := vars[key]
	if !ok {
		return nil, false, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, true, fmt.Errorf("must be an array")
	}

	groups := make([][]types.Hash, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case []interface{}:
			group := make([]types.Hash, 0, len(value))
			for _, nested := range value {
				hashValue, ok := nested.(string)
				if !ok {
					return nil, true, fmt.Errorf("must contain only hash strings")
				}
				hash, err := parseHash(hashValue)
				if err != nil {
					return nil, true, err
				}
				group = append(group, hash)
			}
			groups = append(groups, group)
		case string:
			hash, err := parseHash(value)
			if err != nil {
				return nil, true, err
			}
			groups = append(groups, []types.Hash{hash})
		default:
			return nil, true, fmt.Errorf("must contain only hash strings or arrays of hash strings")
		}
	}
	return groups, true, nil
}

func extractInlineToken(query, key string) (string, bool) {
	idx := strings.Index(strings.ToLower(query), key+":")
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(query[idx+len(key)+1:])
	rest = strings.TrimLeft(rest, " \"")
	end := strings.IndexAny(rest, " ,)\"\n\t}")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end]), true
}

func parseHash(input string) (types.Hash, error) {
	decoded, err := hexutil.Decode(strings.TrimSpace(input))
	if err != nil {
		return types.Hash{}, err
	}
	if len(decoded) != types.HashLength {
		return types.Hash{}, fmt.Errorf("must be %d bytes", types.HashLength)
	}
	return types.BytesToHash(decoded), nil
}

func parseAddress(input string) (types.Address, error) {
	value := strings.TrimSpace(input)
	if !types.IsHexAddress(value) {
		return types.Address{}, fmt.Errorf("must be a 20-byte hex address")
	}
	return types.HexToAddress(value), nil
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	resp := GraphQLResponse{
		Errors: []*GraphQLError{{Message: msg}},
	}
	writeJSON(w, status, resp)
}

// writeJSON marshals v as JSON and writes it to the response writer.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn("graphql: failed to encode response", "err", err)
	}
}
