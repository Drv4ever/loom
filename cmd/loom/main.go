package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ayush/loom/internal/loom"
)

func main() {
	var config loom.Config
	flag.BoolVar(&config.Demo, "demo", false, "start three real local demo peers, a relay, HTTP services, and sample folders")
	flag.StringVar(&config.Name, "name", "", "device name (normal mode)")
	flag.StringVar(&config.Listen, "listen", "127.0.0.1:8787", "loopback HTTP API address")
	flag.StringVar(&config.DataDir, "data-dir", ".loom", "directory for identity, network key, state, and received files")
	flag.StringVar(&config.NetworkKey, "network-key", "", "64-character hex private-network key (created and persisted when omitted)")
	flag.BoolVar(&config.Relay, "relay", false, "offer this device as a libp2p circuit relay")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := loom.New(ctx, config)
	if err != nil {
		log.Fatalf("start Loom: %v", err)
	}
	server := &http.Server{
		Addr:              config.Listen,
		Handler:           app.Handler(config.Listen),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("Loom API listening on http://%s", config.Listen)
		serveErr <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = app.Close(shutdownCtx)
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, err)
			_ = app.Close(context.Background())
			os.Exit(1)
		}
	}
}
