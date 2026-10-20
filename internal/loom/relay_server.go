package loom

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

// RelayServerOptions holds the configuration for a deployable Circuit Relay v2 + Rendezvous server.
type RelayServerOptions struct {
	ListenAddresses []string
	DataDir         string
	IdentityPath    string
	NetworkKey      []byte // Optional: 32-byte PSK for private-mesh relays
	AuthToken       string // Optional: shared authorization token
	AllowLoopback   bool   // For testing/local validation
	DurationLimit   time.Duration
	DataLimit       int64
}

// RelayServer is a standalone libp2p Circuit Relay v2 host with built-in Rendezvous discovery.
type RelayServer struct {
	Host       host.Host
	Rendezvous *RendezvousStore
	Options    RelayServerOptions
}

// NewRelayServer initializes and starts a new RelayServer instance.
func NewRelayServer(opts RelayServerOptions) (*RelayServer, error) {
	if len(opts.ListenAddresses) == 0 {
		opts.ListenAddresses = []string{
			"/ip4/0.0.0.0/tcp/4001",
			"/ip4/0.0.0.0/udp/4001/quic-v1",
		}
	}
	if opts.DataDir == "" {
		opts.DataDir = ".loom-relay"
	}
	if err := os.MkdirAll(opts.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("create relay data directory: %w", err)
	}

	idPath := opts.IdentityPath
	if idPath == "" {
		idPath = filepath.Join(opts.DataDir, "relay_identity.key")
	}
	identity, err := loadOrCreateIdentity(idPath)
	if err != nil {
		return nil, fmt.Errorf("load relay identity: %w", err)
	}

	libp2pOpts := []libp2p.Option{
		libp2p.Identity(identity),
		libp2p.ListenAddrStrings(opts.ListenAddresses...),
		libp2p.UserAgent("loom-relay/" + Version),
		libp2p.ForceReachabilityPublic(),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
	}

	if len(opts.NetworkKey) == 32 {
		libp2pOpts = append(libp2pOpts, libp2p.PrivateNetwork(pnet.PSK(opts.NetworkKey)))
	}

	durLimit := opts.DurationLimit
	if durLimit <= 0 {
		durLimit = 60 * time.Minute
	}
	dataLimit := opts.DataLimit
	if dataLimit <= 0 {
		dataLimit = 2 << 30 // 2 GiB per circuit reservation
	}

	relayConfig := relay.WithLimit(&relay.RelayLimit{
		Duration: durLimit,
		Data:     dataLimit,
	})

	if opts.AllowLoopback {
		libp2pOpts = append(libp2pOpts, libp2p.EnableRelayService(
			relayConfig,
			relay.WithReservationAddressFilter(func(ma.Multiaddr) bool { return true }),
		))
	} else {
		libp2pOpts = append(libp2pOpts, libp2p.EnableRelayService(relayConfig))
	}

	h, err := libp2p.New(libp2pOpts...)
	if err != nil {
		return nil, fmt.Errorf("start relay libp2p host: %w", err)
	}

	rendezvous := NewRendezvousStore()
	rendezvous.Attach(h)

	return &RelayServer{
		Host:       h,
		Rendezvous: rendezvous,
		Options:    opts,
	}, nil
}

// Addrs returns all public listening addresses formatted with the /p2p/<peer-id> suffix.
func (r *RelayServer) Addrs() []string {
	if r == nil || r.Host == nil {
		return nil
	}
	peerID := r.Host.ID().String()
	res := make([]string, 0, len(r.Host.Addrs()))
	for _, a := range r.Host.Addrs() {
		res = append(res, fmt.Sprintf("%s/p2p/%s", a.String(), peerID))
	}
	return res
}

// Close gracefully terminates the relay service, rendezvous store, and underlying libp2p host.
func (r *RelayServer) Close() error {
	if r == nil {
		return nil
	}
	if r.Rendezvous != nil {
		r.Rendezvous.Close()
	}
	if r.Host != nil {
		return r.Host.Close()
	}
	return nil
}
