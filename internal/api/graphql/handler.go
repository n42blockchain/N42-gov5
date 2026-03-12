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
	"encoding/json"
	"fmt"
	"io"
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
	backend *Resolver
}

// NewHandler creates a new GraphQL HTTP handler.
func NewHandler(resolver *Resolver) *Handler {
	return &Handler{backend: resolver}
}

// ServeHTTP implements the http.Handler interface.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST method is supported")
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
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
		if num, ok := extractUint64Variable(req.Variables, "number"); ok {
			n := hexutil.Uint64(num)
			args.Number = &n
		}
		if hashStr, ok := req.Variables["hash"].(string); ok {
			hash := types.HexToHash(hashStr)
			args.Hash = &hash
		}
	}

	// Also try to parse inline arguments from the query string.
	if args.Number == nil && args.Hash == nil {
		if num, ok := extractInlineNumber(req.Query); ok {
			n := hexutil.Uint64(num)
			args.Number = &n
		}
		if hash, ok := extractInlineHash(req.Query); ok {
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
		if hashStr, ok := req.Variables["hash"].(string); ok {
			hash = types.HexToHash(hashStr)
			found = true
		}
	}

	if !found {
		if h, ok := extractInlineHash(req.Query); ok {
			hash = h
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
		if addrStr, ok := req.Variables["address"].(string); ok {
			addr = types.HexToAddress(addrStr)
			addrFound = true
		}
		if num, ok := extractUint64Variable(req.Variables, "blockNumber"); ok {
			blockNr = &num
		}
	}

	if !addrFound {
		if a, ok := extractInlineAddress(req.Query); ok {
			addr = a
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
		if fb, ok := extractUint64Variable(req.Variables, "fromBlock"); ok {
			n := hexutil.Uint64(fb)
			filter.FromBlock = &n
		}
		if tb, ok := extractUint64Variable(req.Variables, "toBlock"); ok {
			n := hexutil.Uint64(tb)
			filter.ToBlock = &n
		}
		if addrs, ok := req.Variables["addresses"].([]interface{}); ok {
			for _, a := range addrs {
				if s, ok := a.(string); ok {
					filter.Addresses = append(filter.Addresses, types.HexToAddress(s))
				}
			}
		}
		if topics, ok := req.Variables["topics"].([]interface{}); ok {
			for _, topicGroup := range topics {
				var group []types.Hash
				switch tg := topicGroup.(type) {
				case []interface{}:
					for _, t := range tg {
						if s, ok := t.(string); ok {
							group = append(group, types.HexToHash(s))
						}
					}
				case string:
					group = append(group, types.HexToHash(tg))
				}
				filter.Topics = append(filter.Topics, group)
			}
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
func extractUint64Variable(vars map[string]interface{}, key string) (uint64, bool) {
	v, ok := vars[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return uint64(val), true
	case string:
		// Support hex (0x...) and decimal strings.
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, "0x") || strings.HasPrefix(val, "0X") {
			n, err := strconv.ParseUint(val[2:], 16, 64)
			if err != nil {
				return 0, false
			}
			return n, true
		}
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// extractInlineNumber attempts to find a number argument inside a GraphQL
// query string, e.g. block(number: 123) or block(number: "0x7b").
func extractInlineNumber(query string) (uint64, bool) {
	idx := strings.Index(strings.ToLower(query), "number:")
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(query[idx+len("number:"):])
	// Remove leading quote if present.
	rest = strings.TrimLeft(rest, " \"")
	// Extract the value token.
	end := strings.IndexAny(rest, " ,)\"\n\t}")
	if end < 0 {
		end = len(rest)
	}
	token := strings.TrimSpace(rest[:end])
	if strings.HasPrefix(token, "0x") || strings.HasPrefix(token, "0X") {
		n, err := strconv.ParseUint(token[2:], 16, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	n, err := strconv.ParseUint(token, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// extractInlineHash attempts to find a hash argument inside a GraphQL query
// string, e.g. block(hash: "0xabc...").
func extractInlineHash(query string) (types.Hash, bool) {
	idx := strings.Index(strings.ToLower(query), "hash:")
	if idx < 0 {
		return types.Hash{}, false
	}
	rest := strings.TrimSpace(query[idx+len("hash:"):])
	rest = strings.TrimLeft(rest, " \"")
	end := strings.IndexAny(rest, " ,)\"\n\t}")
	if end < 0 {
		end = len(rest)
	}
	token := strings.TrimSpace(rest[:end])
	if !strings.HasPrefix(token, "0x") && !strings.HasPrefix(token, "0X") {
		return types.Hash{}, false
	}
	if len(token) < 10 { // minimum reasonable hash length
		return types.Hash{}, false
	}
	hash := types.HexToHash(token)
	return hash, true
}

// extractInlineAddress attempts to find an address argument inside a GraphQL
// query string, e.g. account(address: "0xabc...").
func extractInlineAddress(query string) (types.Address, bool) {
	idx := strings.Index(strings.ToLower(query), "address:")
	if idx < 0 {
		return types.Address{}, false
	}
	rest := strings.TrimSpace(query[idx+len("address:"):])
	rest = strings.TrimLeft(rest, " \"")
	end := strings.IndexAny(rest, " ,)\"\n\t}")
	if end < 0 {
		end = len(rest)
	}
	token := strings.TrimSpace(rest[:end])
	if !strings.HasPrefix(token, "0x") && !strings.HasPrefix(token, "0X") {
		return types.Address{}, false
	}
	addr := types.HexToAddress(token)
	return addr, true
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
