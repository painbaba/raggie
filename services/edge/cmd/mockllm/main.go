// SPDX-License-Identifier: Apache-2.0

// Command mockllm runs a deterministic OpenAI-compatible chat server for CI
// end-to-end tests and offline demos. Point the edge at it with
// LLMAAS_BASE_URL=http://mockllm:11435/v1. See internal/mockllm for behavior
// and the MOCKLLM_* environment variables.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alicangunduz/raggie/services/edge/internal/mockllm"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz and exit 0/1 (container healthcheck)")
	flag.Parse()

	cfg := mockllm.LoadConfig()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if *healthcheck {
		_, port, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			os.Exit(1)
		}
		if err := mockllm.Probe("http://127.0.0.1:" + port + "/healthz"); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mockllm.New(cfg).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logger.Info("mockllm listening", "addr", cfg.Addr, "models", cfg.Models,
			"token_delay_ms", cfg.TokenDelay.Milliseconds(), "tool_calls", cfg.ToolCalls, "reasoning", cfg.Reasoning,
			"language", cfg.Language)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
