package serve

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
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

	// /headers?from=F&count=C → concatenated [4-byte-LE len || header] records
	// (≤ Caps.MaxHeaders; stops early at a gap/tip). Batches the per-block /header
	// fetch so a client can catch up over a long range efficiently.
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		from, err1 := strconv.ParseUint(r.URL.Query().Get("from"), 10, 64)
		count, err2 := strconv.ParseUint(r.URL.Query().Get("count"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "bad from/count", http.StatusBadRequest)
			return
		}
		hs, err := svc.GetHeaders(clientIP(r), from, count)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		for _, h := range hs {
			var lp [4]byte
			binary.LittleEndian.PutUint32(lp[:], uint32(len(h)))
			_, _ = w.Write(lp[:])
			_, _ = w.Write(h)
		}
	})

	// /block?n=N → 4-byte-LE header length || header || body (one round-trip).
	mux.HandleFunc("/block", func(w http.ResponseWriter, r *http.Request) {
		n, err := qn(r)
		if err != nil {
			http.Error(w, "bad n", http.StatusBadRequest)
			return
		}
		hb, bb, err := svc.GetBlock(clientIP(r), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		var lp [4]byte
		binary.LittleEndian.PutUint32(lp[:], uint32(len(hb)))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(lp[:])
		_, _ = w.Write(hb)
		_, _ = w.Write(bb)
	})

	// /code?h=0x..&h=0x.. → concatenated [hash(32) || 4-byte-LE len || ZSTD(code)]
	// records (the client decompresses); missing hashes omitted. zstd halves the
	// wire (~45% of raw) and, when the backend has the codes-freezer, ships its
	// already-compressed blob directly (no decompress+recompress).
	mux.HandleFunc("/code", func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query()["h"]
		hashes := make([]types.Hash, 0, len(raw))
		for _, s := range raw {
			b, err := hexutil.Decode(s)
			if err != nil || len(b) != 32 {
				http.Error(w, "bad code hash", http.StatusBadRequest)
				return
			}
			hashes = append(hashes, types.BytesToHash(b))
		}
		codes, err := svc.GetCodeZ(clientIP(r), hashes)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		for _, h := range hashes {
			c, ok := codes[h]
			if !ok {
				continue
			}
			var lp [4]byte
			binary.LittleEndian.PutUint32(lp[:], uint32(len(c)))
			_, _ = w.Write(h[:])
			_, _ = w.Write(lp[:])
			_, _ = w.Write(c)
		}
	})

	// /account-multiproof?addrs=0x..,0x.. → JSON serve.AccountMultiproofResponse:
	// one merged, deduped account multiproof for all addrs (per-block layer-③).
	mux.HandleFunc("/account-multiproof", func(w http.ResponseWriter, r *http.Request) {
		var addrs []types.Address
		for _, p := range strings.Split(r.URL.Query().Get("addrs"), ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			ab, err := hexutil.Decode(p)
			if err != nil || len(ab) != 20 {
				http.Error(w, "bad addr", http.StatusBadRequest)
				return
			}
			var a types.Address
			copy(a[:], ab)
			addrs = append(addrs, a)
		}
		b, err := svc.GetAccountMultiproof(clientIP(r), addrs)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})

	// /full-header?n=N → fork-aware canonical RLP header (all exec fields) for ②.
	mux.HandleFunc("/full-header", func(w http.ResponseWriter, r *http.Request) {
		n, err := qn(r)
		if err != nil {
			http.Error(w, "bad n", http.StatusBadRequest)
			return
		}
		b, err := svc.GetFullHeader(clientIP(r), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeBytes(w, b)
	})

	// /account-proof?addr=0x..&slots=0x..,0x.. → JSON account.AccProofResult
	// (EIP-1186) at head. The mobile/minimal layer-③: bounded (~KB), verified by
	// the client via stateless.VerifyAccountInclusion against the trusted stateRoot.
	mux.HandleFunc("/account-proof", func(w http.ResponseWriter, r *http.Request) {
		ab, err := hexutil.Decode(r.URL.Query().Get("addr"))
		if err != nil || len(ab) != 20 {
			http.Error(w, "bad addr", http.StatusBadRequest)
			return
		}
		var addr types.Address
		copy(addr[:], ab)
		var slots []types.Hash
		if s := strings.TrimSpace(r.URL.Query().Get("slots")); s != "" {
			for _, p := range strings.Split(s, ",") {
				sb, err := hexutil.Decode(strings.TrimSpace(p))
				if err != nil || len(sb) != 32 {
					http.Error(w, "bad slot", http.StatusBadRequest)
					return
				}
				slots = append(slots, types.BytesToHash(sb))
			}
		}
		b, err := svc.GetAccountProof(clientIP(r), addr, slots)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		num, hash, anchor, err := svc.Head()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "head": num, "hash": hash.Hex(), "anchor": anchor})
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
