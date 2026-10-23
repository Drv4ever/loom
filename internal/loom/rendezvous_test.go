package loom

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func TestRendezvousTopicDerivation(t *testing.T) {
	key1 := testNetworkKey(1)
	key2 := testNetworkKey(2)

	topic1 := RendezvousTopic(key1)
	topic1Again := RendezvousTopic(key1)
	topic2 := RendezvousTopic(key2)

	if topic1 == "" || len(topic1) != 32 {
		t.Fatalf("expected 32-character hex topic, got %q", topic1)
	}
	if topic1 != topic1Again {
		t.Fatalf("expected deterministic topic derivation")
	}
	if topic1 == topic2 {
		t.Fatalf("expected different topics for different network keys")
	}
}

func TestRendezvousStoreInMem(t *testing.T) {
	store := NewRendezvousStore()
	defer store.Close()

	peerA, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqnjvq3qEzKnQVNocXJwUkEP")
	peerB, _ := peer.Decode("12D3KooWR4nmG6Y5Rk3a1L3V7H8pP9m1Q2s3T4u5V6w7X8y9Z1a2")
	addrA, _ := ma.NewMultiaddr("/ip4/192.168.1.50/tcp/4001")

	topic := "loom-test-topic"

	// Initial empty discovery
	discovered := store.Discover(topic, 10, peerB)
	if len(discovered) != 0 {
		t.Fatalf("expected 0 discovered peers initially, got %d", len(discovered))
	}

	// Register Peer A
	store.Register(topic, peerA, []ma.Multiaddr{addrA}, 200*time.Millisecond, "")

	// Peer B discovers Peer A
	discovered = store.Discover(topic, 10, peerB)
	if len(discovered) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(discovered))
	}
	if discovered[0].ID != peerA {
		t.Fatalf("expected peer %s, got %s", peerA, discovered[0].ID)
	}

	// Peer A discovers itself -> should be excluded
	discoveredSelf := store.Discover(topic, 10, peerA)
	if len(discoveredSelf) != 0 {
		t.Fatalf("expected peer to be excluded from self-discovery")
	}

	// Wait for TTL expiration
	time.Sleep(250 * time.Millisecond)
	store.PruneExpired()

	discoveredAfterExpiry := store.Discover(topic, 10, peerB)
	if len(discoveredAfterExpiry) != 0 {
		t.Fatalf("expected peer to expire after TTL, got %d", len(discoveredAfterExpiry))
	}
}

func TestRendezvousProtocolNetwork(t *testing.T) {
	key := testNetworkKey(10)
	topic := RendezvousTopic(key)

	// Create Relay/Rendezvous Node
	relayDir := filepath.Join(t.TempDir(), "relay")
	relayNode := newTestNode(t, "relay", relayDir, key)
	defer relayNode.Close()

	store := NewRendezvousStore()
	defer store.Close()
	store.Attach(relayNode.Host)

	// Create Node A
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	defer nodeA.Close()

	// Create Node B
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)
	defer nodeB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	relayInfo := peer.AddrInfo{ID: relayNode.Host.ID(), Addrs: relayNode.Host.Addrs()}

	// Connect Node A and Node B to Relay
	if err := nodeA.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("nodeA connect to relay: %v", err)
	}
	if err := nodeB.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("nodeB connect to relay: %v", err)
	}

	// Node A registers with Relay
	if err := RegisterPeer(ctx, nodeA.Host, relayNode.Host.ID(), topic, 10*time.Second); err != nil {
		t.Fatalf("nodeA register peer: %v", err)
	}

	// Node B discovers peers from Relay
	discovered, err := DiscoverPeers(ctx, nodeB.Host, relayNode.Host.ID(), topic, 10)
	if err != nil {
		t.Fatalf("nodeB discover peers: %v", err)
	}

	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered peer, got %d", len(discovered))
	}
	if discovered[0].ID != nodeA.Host.ID() {
		t.Fatalf("expected discovered peer %s, got %s", nodeA.Host.ID(), discovered[0].ID)
	}

	// Node B queries a non-existent topic
	emptyDiscovery, err := DiscoverPeers(ctx, nodeB.Host, relayNode.Host.ID(), "non-existent-topic", 10)
	if err != nil {
		t.Fatalf("discover non-existent topic: %v", err)
	}
	if len(emptyDiscovery) != 0 {
		t.Fatalf("expected 0 peers for non-existent topic, got %d", len(emptyDiscovery))
	}
}

func TestRendezvousBlindedTopicIsolation(t *testing.T) {
	keyAlpha := testNetworkKey(101)
	keyBeta := testNetworkKey(102)

	topicAlpha := RendezvousTopic(keyAlpha)
	topicBeta := RendezvousTopic(keyBeta)

	if topicAlpha == topicBeta {
		t.Fatalf("expected distinct topics for different network keys")
	}

	// Relay host running rendezvous store
	relayDir := filepath.Join(t.TempDir(), "relay")
	relayNode := newTestNode(t, "relay", relayDir, keyAlpha)
	defer relayNode.Close()

	store := NewRendezvousStore()
	defer store.Close()
	store.Attach(relayNode.Host)

	// Node Alpha on network Alpha
	nodeAlpha := newTestNode(t, "nodeAlpha", filepath.Join(t.TempDir(), "nodeAlpha"), keyAlpha)
	defer nodeAlpha.Close()

	// Node Beta on network Beta (different key)
	nodeBeta := newTestNode(t, "nodeBeta", filepath.Join(t.TempDir(), "nodeBeta"), keyAlpha)
	defer nodeBeta.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	relayInfo := peer.AddrInfo{ID: relayNode.Host.ID(), Addrs: relayNode.Host.Addrs()}
	if err := nodeAlpha.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeAlpha: %v", err)
	}
	if err := nodeBeta.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeBeta: %v", err)
	}

	// Register nodeAlpha under topicAlpha
	if err := RegisterPeer(ctx, nodeAlpha.Host, relayNode.Host.ID(), topicAlpha, 30*time.Second); err != nil {
		t.Fatalf("register nodeAlpha: %v", err)
	}

	// nodeBeta queries for topicBeta -> must receive 0 peers (complete isolation)
	discoveredBeta, err := DiscoverPeers(ctx, nodeBeta.Host, relayNode.Host.ID(), topicBeta, 10)
	if err != nil {
		t.Fatalf("discover topicBeta: %v", err)
	}
	if len(discoveredBeta) != 0 {
		t.Fatalf("expected 0 peers discovered on topicBeta, got %d", len(discoveredBeta))
	}

	// nodeBeta queries for topicAlpha -> finds nodeAlpha
	discoveredAlpha, err := DiscoverPeers(ctx, nodeBeta.Host, relayNode.Host.ID(), topicAlpha, 10)
	if err != nil {
		t.Fatalf("discover topicAlpha: %v", err)
	}
	if len(discoveredAlpha) != 1 || discoveredAlpha[0].ID != nodeAlpha.Host.ID() {
		t.Fatalf("expected 1 discovered peer matching nodeAlpha, got %v", discoveredAlpha)
	}
}

func TestRendezvousMultiPeerDiscoveryAndLimits(t *testing.T) {
	store := NewRendezvousStore()
	defer store.Close()

	peer1, _ := peer.Decode("12D3KooWAcXjZ9Wq2tYV5m3K8P1nL4jR7sB6cV9uX3yZ5wA1bC2d")
	peer2, _ := peer.Decode("12D3KooWBdXjZ9Wq2tYV5m3K8P1nL4jR7sB6cV9uX3yZ5wA1bC2e")
	peer3, _ := peer.Decode("12D3KooWCeXjZ9Wq2tYV5m3K8P1nL4jR7sB6cV9uX3yZ5wA1bC2f")
	peer4, _ := peer.Decode("12D3KooWDfXjZ9Wq2tYV5m3K8P1nL4jR7sB6cV9uX3yZ5wA1bC2g")
	caller, _ := peer.Decode("12D3KooWEgXjZ9Wq2tYV5m3K8P1nL4jR7sB6cV9uX3yZ5wA1bC2h")

	dummyAddr, _ := ma.NewMultiaddr("/ip4/10.0.0.1/tcp/4001")
	topic := "multi-peer-topic"

	store.Register(topic, peer1, []ma.Multiaddr{dummyAddr}, time.Minute, "")
	store.Register(topic, peer2, []ma.Multiaddr{dummyAddr}, time.Minute, "")
	store.Register(topic, peer3, []ma.Multiaddr{dummyAddr}, time.Minute, "")
	store.Register(topic, peer4, []ma.Multiaddr{dummyAddr}, time.Minute, "")

	// Query with limit=2
	disc2 := store.Discover(topic, 2, caller)
	if len(disc2) != 2 {
		t.Fatalf("expected exactly 2 peers with limit=2, got %d", len(disc2))
	}

	// Query with limit=10
	discAll := store.Discover(topic, 10, caller)
	if len(discAll) != 4 {
		t.Fatalf("expected all 4 peers with limit=10, got %d", len(discAll))
	}

	// Verify caller self-exclusion when caller was registered
	store.Register(topic, caller, []ma.Multiaddr{dummyAddr}, time.Minute, "")
	discCaller := store.Discover(topic, 10, caller)
	if len(discCaller) != 4 {
		t.Fatalf("expected caller to be excluded, returning remaining 4 peers, got %d", len(discCaller))
	}
	for _, p := range discCaller {
		if p.ID == caller {
			t.Fatalf("caller was not excluded from discovery results")
		}
	}
}

func TestRendezvousDataPayloadExchange(t *testing.T) {
	key := testNetworkKey(15)
	topic := RendezvousTopic(key)

	relayDir := filepath.Join(t.TempDir(), "relay")
	relayNode := newTestNode(t, "relay", relayDir, key)
	defer relayNode.Close()

	store := NewRendezvousStore()
	defer store.Close()
	store.Attach(relayNode.Host)

	nodeA := newTestNode(t, "nodeA", filepath.Join(t.TempDir(), "nodeA"), key)
	defer nodeA.Close()
	nodeB := newTestNode(t, "nodeB", filepath.Join(t.TempDir(), "nodeB"), key)
	defer nodeB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	relayInfo := peer.AddrInfo{ID: relayNode.Host.ID(), Addrs: relayNode.Host.Addrs()}
	if err := nodeA.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeA: %v", err)
	}
	if err := nodeB.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeB: %v", err)
	}

	customData := "opaque-encrypted-pairing-voucher-payload-12345"
	if err := RegisterPeerWithData(ctx, nodeA.Host, relayNode.Host.ID(), topic, 15*time.Second, customData); err != nil {
		t.Fatalf("register with data: %v", err)
	}

	discovered, err := DiscoverPeersWithData(ctx, nodeB.Host, relayNode.Host.ID(), topic, 10)
	if err != nil {
		t.Fatalf("discover with data: %v", err)
	}

	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered peer, got %d", len(discovered))
	}
	if discovered[0].ID != nodeA.Host.ID().String() {
		t.Fatalf("expected peer %s, got %s", nodeA.Host.ID(), discovered[0].ID)
	}
	if discovered[0].Data != customData {
		t.Fatalf("data mismatch! got %q, want %q", discovered[0].Data, customData)
	}
}
