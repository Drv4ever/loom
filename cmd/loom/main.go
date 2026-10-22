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
	"strings"
	"syscall"
	"time"

	"github.com/ayush/loom/internal/cli"
	"github.com/ayush/loom/internal/loom"
)

func main() {
	args := os.Args[1:]

	// Extract global --api flag if provided anywhere in arguments
	var apiURL string
	var filteredArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--api" && i+1 < len(args) {
			apiURL = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--api=") {
			apiURL = strings.TrimPrefix(arg, "--api=")
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}

	// 1. Explicit daemon command
	if len(filteredArgs) > 0 && (filteredArgs[0] == "daemon" || filteredArgs[0] == "up") {
		runDaemon(filteredArgs[1:])
		return
	}

	// 2. Client subcommands
	if len(filteredArgs) > 0 {
		cmd := filteredArgs[0]
		switch cmd {
		case "status", "devices", "services", "publish", "unpublish", "sync", "network", "forwards", "forward", "relays", "relay", "doctor", "pair", "acl", "help":
			runner := cli.NewRunner(apiURL)
			if err := runner.Execute(context.Background(), filteredArgs); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "-h", "--help":
			runner := cli.NewRunner(apiURL)
			_ = runner.RunHelp(nil)
			return
		}
	}

	// 3. Backward-compatible default: if args start with '-' (e.g. --name) or no args given, run daemon
	runDaemon(filteredArgs)
}

func runDaemon(args []string) {
	var config loom.Config
	var keyFile string
	var relayAddrs string

	fs := flag.NewFlagSet("loom", flag.ExitOnError)
	fs.StringVar(&config.Name, "name", "", "device name")
	fs.StringVar(&config.Listen, "listen", "127.0.0.1:8787", "loopback HTTP API address")
	fs.StringVar(&config.DataDir, "data-dir", ".loom", "directory for identity, network key, state, and received files")
	fs.StringVar(&config.NetworkKey, "network-key", "", "64-character hex private-network key (created and persisted when omitted)")
	fs.StringVar(&keyFile, "network-key-file", "", "read the shared private-network key from a file")
	fs.StringVar(&config.PeerListen, "peer-listen", "/ip4/0.0.0.0/tcp/0", "libp2p listen multiaddress; use /ip4/0.0.0.0/tcp/4001 for a fixed LAN port")
	fs.BoolVar(&config.Relay, "relay", false, "offer this device as a libp2p circuit relay")
	fs.StringVar(&relayAddrs, "relay-address", "", "comma-separated relay multiaddresses")

	_ = fs.Parse(args)

	if relayAddrs != "" {
		for _, r := range strings.Split(relayAddrs, ",") {
			r = strings.TrimSpace(r)
			if r != "" {
				config.Relays = append(config.Relays, r)
			}
		}
	}

	if keyFile != "" {
		if config.NetworkKey != "" {
			log.Fatal("use either --network-key or --network-key-file")
		}
		key, err := os.ReadFile(keyFile)
		if err != nil {
			log.Fatalf("read network key file: %v", err)
		}
		config.NetworkKey = strings.TrimSpace(string(key))
		if config.NetworkKey == "" {
			log.Fatal("network key file is empty")
		}
	}

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
