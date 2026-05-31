package serve

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// Handler maps the stateless Service onto HTTP and (optionally) wraps it with the
// per-IP request-rate limiter (modules/rpc/jsonrpc.RateLimiter → HTTP 429). The
// Service's per-request caps + per-IP bandwidth limiter run inside the methods.
// Routes (read-only GET; n = block number):
//
//	GET /head                 → {"number","hash","anchor"} JSON
//	GET /header?n=N           → header bytes (block.Header.Marshal)
//	GET /anchor?n=N           → MPT anchor proof bytes (encoded BlockProof)
//	GET /witness?n=N          → witness stream bytes
//	GET /block?n=N            → 4-byte-LE-len header || body
//
// Artifacts are immutable + content-verifiable, so responses are cacheable
// (a CDN can front everything but the live /head).
func Handler(svc *Service, rl *jsonrpc.RateLimiter) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/head", func(w http.ResponseWriter, r *http.Request) {
		num, hash, anchor, err := svc.Head()
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"number": num, "hash": hash.Hex(), "anchor": anchor})
	})

	mux.HandleFunc("/header", func(w http.ResponseWriter, r *http.Request) {
		n, err := qn(r)
		if err != nil {
			http.Error(w, "bad n", http.StatusBadRequest)
			return
		}
		hs, err := svc.GetHeaders(clientIP(r), n, 1)
		if err != nil {
			writeErr(w, err)
			return
		}
		if len(hs) == 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeBytes(w, hs[0])
	})

	mux.HandleFunc("/anchor", func(w http.ResponseWriter, r *http.Request) {
		n, err := qn(r)
		if err != nil {
			http.Error(w, "bad n", http.StatusBadRequest)
			return
		}
		b, err := svc.GetAnchor(clientIP(r), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeBytes(w, b)
	})

	mux.HandleFunc("/witness", func(w http.ResponseWriter, r *http.Request) {
		n, err := qn(r)
		if err != nil {
			http.Error(w, "bad n", http.StatusBadRequest)
			return
		}
		b, err := svc.GetWitness(clientIP(r), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeBytes(w, b)
	})

	var h http.Handler = mux
	if rl != nil {
		h = jsonrpc.RateLimitMiddleware(rl, h)
	}
	return h
}

func writeBytes(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(b)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCapExceeded):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrRateLimited):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	default:
		http.Error(w, err.Error(), http.StatusNotFound) // backend gap/absent
	}
}

func qn(r *http.Request) (uint64, error) {
	return strconv.ParseUint(r.URL.Query().Get("n"), 10, 64)
}

// clientIP mirrors jsonrpc.getClientIP (unexported there): X-Forwarded-For,
// then X-Real-IP, then RemoteAddr — for the per-IP bandwidth limiter.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip.String()
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
