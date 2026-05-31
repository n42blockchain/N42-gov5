package serve

import (
	"context"
	"net/http"
	"time"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// ServerConfig is the attack-hardened HTTP server posture (architecture doc
// §2.3): request/response timeouts, a header-size ceiling, and a cap on
// concurrent in-flight requests (a cheap connection/concurrency limit on top of
// the per-IP request-rate and bandwidth limiters that live in the Service +
// middleware). Zero fields take the defaults below.
type ServerConfig struct {
	Addr           string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	MaxHeaderBytes int
	MaxConcurrent  int // 0 = unlimited
}

// DefaultServerConfig: 10 s read / 30 s write / 60 s idle, 1 MiB headers, 1024
// concurrent requests. Tuned for many cheap reads fronted by a CDN.
func DefaultServerConfig(addr string) ServerConfig {
	return ServerConfig{
		Addr:           addr,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
		MaxConcurrent:  1024,
	}
}

// NewServer builds an *http.Server serving svc with the full protection stack:
// per-IP request-rate limiter (rl, optional → 429), per-IP bandwidth + per-request
// caps (inside the Service), a max-concurrent-requests gate (→ 503), and hardened
// timeouts/header limits. Call srv.ListenAndServe(); stop with ShutdownServer.
func NewServer(cfg ServerConfig, svc *Service, rl *jsonrpc.RateLimiter) *http.Server {
	h := Handler(svc, rl)
	if cfg.MaxConcurrent > 0 {
		h = limitConcurrent(cfg.MaxConcurrent, h)
	}
	return &http.Server{
		Addr:           cfg.Addr,
		Handler:        h,
		ReadTimeout:    cfg.ReadTimeout,
		WriteTimeout:   cfg.WriteTimeout,
		IdleTimeout:    cfg.IdleTimeout,
		MaxHeaderBytes: cfg.MaxHeaderBytes,
	}
}

// ShutdownServer gracefully drains srv within the timeout.
func ShutdownServer(srv *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return srv.Shutdown(ctx)
}

// limitConcurrent caps in-flight requests with a buffered-channel semaphore,
// rejecting overflow with 503 (so a request flood can't exhaust goroutines/RoTx).
func limitConcurrent(max int, next http.Handler) http.Handler {
	sem := make(chan struct{}, max)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "server busy", http.StatusServiceUnavailable)
		}
	})
}
