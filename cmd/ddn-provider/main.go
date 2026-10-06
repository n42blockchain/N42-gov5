package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/internal/ddn/native"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	"github.com/n42blockchain/N42/internal/ddn/serve"
	"github.com/n42blockchain/N42/internal/ddn/transformer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	backend := flag.String("backend", "native-rules", "native-rules, native-system1, native-bayes or native-transformer")
	file := flag.String("model", "", "local artifact file")
	did := flag.String("did", "", "provider DID")
	chain := flag.Uint64("chain-id", 0, "required chain ID")
	workers := flag.Int("workers", 2, "max active requests")
	latency := flag.Duration("max-latency", 500*time.Millisecond, "decision deadline ceiling")
	tokenEnv := flag.String("token-env", "N42_DDN_PROVIDER_TOKEN", "bearer token environment variable")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	token := os.Getenv(*tokenEnv)
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if token == "" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("non-loopback provider listener requires a bearer token")
	}
	var p provider.DecisionProvider
	switch *backend {
	case "native-system1":
		p, err = provider.NewNativeSystem1(*did)
	case "native-rules":
		p, err = provider.NewNativeRules(*did)
	case "native-bayes":
		var a native.Artifact
		a, err = native.Load(*file)
		if err == nil {
			p, err = provider.NewNativeModel(*did, a)
		}
	case "native-transformer":
		var a transformer.Artifact
		a, err = transformer.Load(*file)
		if err == nil {
			p, err = provider.NewTransformer(*did, a)
		}
	default:
		return errors.New("provider daemon supports native backends only")
	}
	if err != nil {
		return err
	}
	handler, err := serve.New(serve.Config{ChainID: *chain, MaxConcurrency: *workers, MaxInputBytes: 65536, MaxLatency: *latency, Token: token}, p)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: *latency + 3*time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	fmt.Fprintln(os.Stdout, "native DDN provider listening", listener.Addr(), "model_hash", p.Identity().ModelHash.Hex())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = srv.Shutdown(shutdown); err != nil {
			srv.Close()
			return err
		}
		err = <-done
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
