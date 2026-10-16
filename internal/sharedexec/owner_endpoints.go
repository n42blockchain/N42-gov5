package sharedexec

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// ServePrivateUnix starts both owner endpoints only after all private listeners
// exist. It is terminal: failure or shutdown closes the server and drains owner
// work before the caller may close its BlockChain/database.
func (server *Server) ServePrivateUnix(ctx context.Context, dir string, strict bool) error {
	defer server.Close()
	httpServer, listener, err := server.ListenUnix(filepath.Join(dir, "execution.sock"))
	if err != nil {
		return err
	}
	defer httpServer.Close()
	defer listener.Close()
	var ingress net.Listener
	var strictDone chan error
	if strict {
		strictListener, err := server.ListenStrictIngressUnix(filepath.Join(dir, "ingress.sock"))
		if err != nil {
			return err
		}
		ingress = strictListener
		defer strictListener.Close()
		strictDone = make(chan error, 1)
		go func() { strictDone <- server.ServeStrictIngress(strictListener) }()
	}
	httpDone := make(chan error, 1)
	go func() { httpDone <- httpServer.Serve(listener) }()
	var result error
	graceful := false
	select {
	case <-ctx.Done():
		graceful = true
	case err := <-httpDone:
		httpDone = nil
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = err
		} else {
			result = errors.New("execution HTTP endpoint stopped unexpectedly")
		}
	case err := <-strictDone:
		strictDone = nil
		if err != nil {
			result = err
		} else {
			result = errors.New("strict ingress endpoint stopped unexpectedly")
		}
	}
	if ingress != nil {
		_ = ingress.Close()
	}
	if graceful {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result = httpServer.Shutdown(shutdownCtx)
		cancel()
	}
	_ = httpServer.Close()
	server.Close() // closes live ingress IO, cancels and drains all owner workers
	if httpDone != nil {
		<-httpDone
	}
	if strictDone != nil {
		<-strictDone
	}
	return result
}
