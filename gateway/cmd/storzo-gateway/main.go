package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/imthi16/storzo/gateway/internal/audit"
	"github.com/imthi16/storzo/gateway/internal/proxy"
)

func run(ctx context.Context) error {
	cfg, err := proxy.ConfigFromEnv()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("gateway listener failed")
	}
	defer listener.Close()
	return serve(ctx, cfg, listener)
}

// serve owns the emitter until the HTTP server has finished draining.
func serve(ctx context.Context, cfg proxy.Config, listener net.Listener) (result error) {
	revocations, err := audit.NewRevocations(cfg.AuditURL, cfg.CredSigningKey, cfg.RevocationMaxStale)
	if err != nil {
		return err
	}
	cfg.Revocations = revocations
	interval := cfg.RevocationPollInterval
	if interval <= 0 {
		interval = proxy.DefaultRevocationPollInterval
	}
	pollCtx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()
	revocations.Refresh(pollCtx)
	go revocations.Poll(pollCtx, interval)
	emitter := audit.New(audit.Config{URL: cfg.AuditURL, SigningKey: cfg.CredSigningKey})
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := emitter.Close(flush); err != nil {
			result = errors.Join(result, errors.New("gateway audit flush timed out"))
		}
	}()
	handler, err := proxy.New(cfg, emitter)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("gateway listener failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("gateway shutdown timed out")
		}
		return nil
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
