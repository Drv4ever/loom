package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ayush/loom/internal/loom"
)

func main() {
	var (
		listenAddrs string
		dataDir     string
		idPath      string
		networkKey  string
		keyFile     string
		authToken   string
		showVersion bool
	)

	flag.StringVar(&listenAddrs, "listen", "/ip4/0.0.0.0/tcp/4001,/ip4/0.0.0.0/udp/4001/quic-v1", "comma-separated libp2p listen multiaddresses")
	flag.StringVar(&dataDir, "data-dir", ".loom-relay", "directory for identity and state")
	flag.StringVar(&idPath, "identity-path", "", "path to identity private key file")
	flag.StringVar(&networkKey, "network-key", "", "64-character hex private network key (optional, restricts relay to private mesh)")
	flag.StringVar(&keyFile, "network-key-file", "", "path to file containing private network key")
	flag.StringVar(&authToken, "token", "", "optional shared authentication token")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("loom-relay %s\n", loom.Version)
		return
	}

	var rawKey []byte
	if keyFile != "" {
		if networkKey != "" {
			log.Fatal("use either --network-key or --network-key-file")
		}
		data, err := os.ReadFile(keyFile)
		if err != nil {
			log.Fatalf("read network key file: %v", err)
		}
		networkKey = strings.TrimSpace(string(data))
	}
	if networkKey != "" {
		k, err := hex.DecodeString(networkKey)
		if err != nil || len(k) != 32 {
			log.Fatal("network key must be a 64-character hex string (32 bytes)")
		}
		rawKey = k
	}

	var addrs []string
	for _, a := range strings.Split(listenAddrs, ",") {
		trimmed := strings.TrimSpace(a)
		if trimmed != "" {
			addrs = append(addrs, trimmed)
		}
	}

	server, err := loom.NewRelayServer(loom.RelayServerOptions{
		ListenAddresses: addrs,
		DataDir:         dataDir,
		IdentityPath:    idPath,
		NetworkKey:      rawKey,
		AuthToken:       authToken,
		AllowLoopback:   false,
	})
	if err != nil {
		log.Fatalf("failed to start relay server: %v", err)
	}
	defer server.Close()

	fmt.Printf("════════════════════════════════════════════════════════════════\n")
	fmt.Printf("  Loom Relay & Rendezvous Server v%s\n", loom.Version)
	fmt.Printf("  Peer ID: %s\n", server.Host.ID().String())
	if len(rawKey) == 32 {
		fmt.Printf("  Network: Private mesh (PSK enabled)\n")
	} else {
		fmt.Printf("  Network: Open public relay\n")
	}
	fmt.Printf("════════════════════════════════════════════════════════════════\n")
	fmt.Printf("Listening Addresses:\n")
	for _, a := range server.Addrs() {
		fmt.Printf("  • %s\n", a)
	}
	fmt.Printf("\nServices Active:\n")
	fmt.Printf("  ✓ libp2p Circuit Relay v2 (hop & reservation enabled)\n")
	fmt.Printf("  ✓ Rendezvous Discovery (/loom/rendezvous/1.0.0)\n")
	fmt.Printf("  ✓ AutoNAT Reachability Prober\n")
	fmt.Printf("\nPress Ctrl+C to terminate gracefully.\n")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down Loom relay...")
}
