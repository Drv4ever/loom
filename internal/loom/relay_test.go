package loom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	client "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	ma "github.com/multiformats/go-multiaddr"
)

func TestRelayServerReservationAndRendezvous(t *testing.T) {
	key := testNetworkKey(20)
	topic := RendezvousTopic(key)

	relayDir := filepath.Join(t.TempDir(), "relay_server")
	relaySrv, err := NewRelayServer(RelayServerOptions{
		ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
		DataDir:         relayDir,
		NetworkKey:      key,
		AllowLoopback:   true,
	})
	if err != nil {
		t.Fatalf("start relay server: %v", err)
	}
	defer relaySrv.Close()

	if len(relaySrv.Addrs()) == 0 {
		t.Fatalf("expected relay server to have listening addresses")
	}

	relayInfo := peer.AddrInfo{
		ID:    relaySrv.Host.ID(),
		Addrs: relaySrv.Host.Addrs(),
	}

	// Start Node A
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	defer nodeA.Close()

	// Start Node B
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)
	defer nodeB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect both nodes to the relay
	if err := nodeA.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("nodeA connect to relay: %v", err)
	}
	if err := nodeB.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("nodeB connect to relay: %v", err)
	}

	// Make circuit reservations on the relay
	resA, err := client.Reserve(ctx, nodeA.Host, relayInfo)
	if err != nil {
		t.Fatalf("nodeA reserve slot on relay: %v", err)
	}
	if resA == nil || resA.Voucher == nil {
		t.Fatalf("expected valid reservation voucher for nodeA")
	}

	resB, err := client.Reserve(ctx, nodeB.Host, relayInfo)
	if err != nil {
		t.Fatalf("nodeB reserve slot on relay: %v", err)
	}
	if resB == nil || resB.Voucher == nil {
		t.Fatalf("expected valid reservation voucher for nodeB")
	}

	// Register Node A on the Relay's Rendezvous service
	if err := RegisterPeer(ctx, nodeA.Host, relaySrv.Host.ID(), topic, 30*time.Second); err != nil {
		t.Fatalf("nodeA register on relay rendezvous: %v", err)
	}

	// Node B discovers Node A via the Relay
	discovered, err := DiscoverPeers(ctx, nodeB.Host, relaySrv.Host.ID(), topic, 10)
	if err != nil {
		t.Fatalf("nodeB discover from relay: %v", err)
	}
	if len(discovered) != 1 || discovered[0].ID != nodeA.Host.ID() {
		t.Fatalf("expected Node B to discover Node A, got %v", discovered)
	}

	// Verify circuit relay multiaddress connection:
	// Construct relayed address: /ip4/127.0.0.1/tcp/<relay-port>/p2p/<relay-id>/p2p-circuit/p2p/<nodeA-id>
	relayAddr := relaySrv.Host.Addrs()[0]
	circuitAddrStr := relayAddr.String() + "/p2p/" + relaySrv.Host.ID().String() + "/p2p-circuit"
	circuitMA, err := ma.NewMultiaddr(circuitAddrStr)
	if err != nil {
		t.Fatalf("construct circuit multiaddr: %v", err)
	}

	relayedNodeAInfo := peer.AddrInfo{
		ID:    nodeA.Host.ID(),
		Addrs: []ma.Multiaddr{circuitMA},
	}

	// Connect Node B to Node A over the circuit relay!
	if err := nodeB.Host.Connect(ctx, relayedNodeAInfo); err != nil {
		t.Fatalf("connect Node B to Node A via circuit relay: %v", err)
	}

	// Verify stream communication over circuit relay using FetchInfo
	summaryCtx, summaryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer summaryCancel()
	summary, err := nodeB.FetchInfo(summaryCtx, nodeA.Host.ID())
	if err != nil {
		t.Fatalf("fetch peer summary over circuit relay: %v", err)
	}
	if summary.Name != "nodeA" {
		t.Fatalf("expected nodeA name %q, got %q", "nodeA", summary.Name)
	}
}

func TestRelayRESTAPI(t *testing.T) {
	app, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })

	handler := app.Handler("127.0.0.1:8787")

	// 1. Initial GET /api/relays should return empty list
	resp := serveTestRequest(handler, http.MethodGet, "/api/relays", "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/relays status=%d: %s", resp.Code, resp.Body.String())
	}
	var relays []RelayInfo
	if err := json.Unmarshal(resp.Body.Bytes(), &relays); err != nil {
		t.Fatalf("unmarshal relays: %v", err)
	}
	if len(relays) != 0 {
		t.Fatalf("expected 0 relays initially, got %d", len(relays))
	}

	// 2. Start a test RelayServer
	relayDir := filepath.Join(t.TempDir(), "relay_srv")
	relaySrv, err := NewRelayServer(RelayServerOptions{
		ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
		DataDir:         relayDir,
		AllowLoopback:   true,
	})
	if err != nil {
		t.Fatalf("start relay server: %v", err)
	}
	defer relaySrv.Close()

	relayAddr := relaySrv.Addrs()[0]
	relayPeerID := relaySrv.Host.ID().String()

	// 3. POST /api/relays with valid relay multiaddress
	body, _ := json.Marshal(map[string]string{"address": relayAddr})
	resp = serveTestRequest(handler, http.MethodPost, "/api/relays", "application/json", string(body))
	if resp.Code != http.StatusCreated {
		t.Fatalf("POST /api/relays status=%d: %s", resp.Code, resp.Body.String())
	}
	var created RelayInfo
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created relay: %v", err)
	}
	if created.Address != relayAddr || created.PeerID != relayPeerID {
		t.Fatalf("unexpected created relay: %+v", created)
	}

	// 4. GET /api/relays returns 1 relay
	resp = serveTestRequest(handler, http.MethodGet, "/api/relays", "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/relays status=%d: %s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &relays); err != nil {
		t.Fatalf("unmarshal relays: %v", err)
	}
	if len(relays) != 1 || relays[0].Address != relayAddr {
		t.Fatalf("expected 1 relay with address %s, got %+v", relayAddr, relays)
	}

	// 5. Test invalid multiaddress inputs
	resp = serveTestRequest(handler, http.MethodPost, "/api/relays", "application/json", `{"address":"invalid-addr"}`)
	if resp.Code != http.StatusBadRequest && resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected client error on invalid address, got %d", resp.Code)
	}

	resp = serveTestRequest(handler, http.MethodPost, "/api/relays", "application/json", `{"address":"/ip4/127.0.0.1/tcp/4001"}`)
	if resp.Code != http.StatusBadRequest && resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected error on missing /p2p/, got %d", resp.Code)
	}

	// 6. DELETE /api/relays/{id} by Peer ID
	resp = serveTestRequest(handler, http.MethodDelete, "/api/relays/"+relayPeerID, "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("DELETE /api/relays/%s status=%d: %s", relayPeerID, resp.Code, resp.Body.String())
	}

	// 7. Verify relay was removed
	resp = serveTestRequest(handler, http.MethodGet, "/api/relays", "", "")
	if err := json.Unmarshal(resp.Body.Bytes(), &relays); err != nil {
		t.Fatalf("unmarshal relays: %v", err)
	}
	if len(relays) != 0 {
		t.Fatalf("expected 0 relays after deletion, got %d", len(relays))
	}

	// 8. DELETE on non-existent relay returns 404
	resp = serveTestRequest(handler, http.MethodDelete, "/api/relays/non-existent-relay", "", "")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on deleting non-existent relay, got %d", resp.Code)
	}
}

func TestRelayPersistence(t *testing.T) {
	dataDir := t.TempDir()
	key := testNetworkKey(22)

	app1, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: dataDir, NetworkKey: hex.EncodeToString(key)})
	if err != nil {
		t.Fatalf("start app1: %v", err)
	}

	testRelayAddr := "/ip4/198.51.100.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqnjvq3qEzKnQVNocXJwUkEP"
	if err := app1.AddRelay(testRelayAddr); err != nil {
		t.Fatalf("app1.AddRelay: %v", err)
	}

	relays1 := app1.ListRelays()
	if len(relays1) != 1 || relays1[0].Address != testRelayAddr {
		t.Fatalf("unexpected relays in app1: %+v", relays1)
	}

	// Read state.json on disk to verify persistence
	stateBytes, err := os.ReadFile(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if !strings.Contains(string(stateBytes), testRelayAddr) {
		t.Fatalf("state.json does not contain relay address: %s", string(stateBytes))
	}

	_ = app1.Close(context.Background())

	// Start a second app instance from the same directory
	app2, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: dataDir, NetworkKey: hex.EncodeToString(key)})
	if err != nil {
		t.Fatalf("start app2: %v", err)
	}
	defer app2.Close(context.Background())

	relays2 := app2.ListRelays()
	if len(relays2) != 1 || relays2[0].Address != testRelayAddr {
		t.Fatalf("expected persisted relay in app2, got: %+v", relays2)
	}
}

func TestRelayNodeIsRelayedAndPathSwitching(t *testing.T) {
	key := testNetworkKey(25)

	relayDir := filepath.Join(t.TempDir(), "relay_server")
	relaySrv, err := NewRelayServer(RelayServerOptions{
		ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
		DataDir:         relayDir,
		NetworkKey:      key,
		AllowLoopback:   true,
	})
	if err != nil {
		t.Fatalf("start relay server: %v", err)
	}
	defer relaySrv.Close()

	relayInfo := peer.AddrInfo{
		ID:    relaySrv.Host.ID(),
		Addrs: relaySrv.Host.Addrs(),
	}

	// Direct Node pair: Node A and Node B
	nodeA := newTestNode(t, "nodeA", filepath.Join(t.TempDir(), "nodeA"), key)
	defer nodeA.Close()
	nodeB := newTestNode(t, "nodeB", filepath.Join(t.TempDir(), "nodeB"), key)
	defer nodeB.Close()

	// Relayed Node: Node C
	nodeC := newTestNode(t, "nodeC", filepath.Join(t.TempDir(), "nodeC"), key)
	defer nodeC.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Connect Node A directly to Node B
	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA to nodeB: %v", err)
	}

	// Check IsRelayed for direct connection -> should be false
	if nodeA.IsRelayed(nodeB.Host.ID()) {
		t.Fatalf("expected direct connection between A and B, but IsRelayed was true")
	}
	if nodeB.IsRelayed(nodeA.Host.ID()) {
		t.Fatalf("expected direct connection between B and A, but IsRelayed was true")
	}

	// 2. Connect Node A and Node C to the Relay
	if err := nodeA.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeA to relay: %v", err)
	}
	if err := nodeC.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeC to relay: %v", err)
	}

	// Reserve slots on relay
	if _, err := client.Reserve(ctx, nodeA.Host, relayInfo); err != nil {
		t.Fatalf("reserve nodeA: %v", err)
	}
	if _, err := client.Reserve(ctx, nodeC.Host, relayInfo); err != nil {
		t.Fatalf("reserve nodeC: %v", err)
	}

	// Construct circuit relay address for Node C
	relayAddr := relaySrv.Host.Addrs()[0]
	circuitAddrStr := relayAddr.String() + "/p2p/" + relaySrv.Host.ID().String() + "/p2p-circuit"
	circuitMA, err := ma.NewMultiaddr(circuitAddrStr)
	if err != nil {
		t.Fatalf("new multiaddr: %v", err)
	}

	// Connect Node A to Node C ONLY over the circuit address
	relayedNodeC := peer.AddrInfo{
		ID:    nodeC.Host.ID(),
		Addrs: []ma.Multiaddr{circuitMA},
	}
	if err := nodeA.Host.Connect(ctx, relayedNodeC); err != nil {
		t.Fatalf("connect nodeA to nodeC over circuit: %v", err)
	}

	// Check IsRelayed for circuit connection -> should be true
	if !nodeA.IsRelayed(nodeC.Host.ID()) {
		t.Fatalf("expected IsRelayed to be true for circuit connection A->C")
	}
	if !nodeC.IsRelayed(nodeA.Host.ID()) {
		t.Fatalf("expected IsRelayed to be true for circuit connection C->A")
	}
}

func TestRelayEndToEndStreaming(t *testing.T) {
	key := testNetworkKey(30)

	relayDir := filepath.Join(t.TempDir(), "relay_server")
	relaySrv, err := NewRelayServer(RelayServerOptions{
		ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
		DataDir:         relayDir,
		NetworkKey:      key,
		AllowLoopback:   true,
	})
	if err != nil {
		t.Fatalf("start relay server: %v", err)
	}
	defer relaySrv.Close()

	relayInfo := peer.AddrInfo{
		ID:    relaySrv.Host.ID(),
		Addrs: relaySrv.Host.Addrs(),
	}

	nodeX := newTestNode(t, "nodeX", filepath.Join(t.TempDir(), "nodeX"), key)
	defer nodeX.Close()
	nodeY := newTestNode(t, "nodeY", filepath.Join(t.TempDir(), "nodeY"), key)
	defer nodeY.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Both connect to relay and reserve slots
	if err := nodeX.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeX: %v", err)
	}
	if err := nodeY.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("connect nodeY: %v", err)
	}
	if _, err := client.Reserve(ctx, nodeX.Host, relayInfo); err != nil {
		t.Fatalf("reserve nodeX: %v", err)
	}
	if _, err := client.Reserve(ctx, nodeY.Host, relayInfo); err != nil {
		t.Fatalf("reserve nodeY: %v", err)
	}

	// Set up custom test stream handler on nodeY
	const testStreamProto = "/loom/test-relay-stream/1.0.0"
	doneChan := make(chan []byte, 1)
	nodeY.Host.SetStreamHandler(testStreamProto, func(s network.Stream) {
		defer s.Close()
		buf := make([]byte, 64<<10) // 64 KiB
		n, err := io.ReadFull(s, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			t.Errorf("read from relay stream: %v", err)
			return
		}
		hash := sha256.Sum256(buf[:n])
		_, _ = s.Write(hash[:])
		doneChan <- hash[:]
	})

	// Connect nodeX to nodeY via circuit relay
	circuitAddrStr := relaySrv.Host.Addrs()[0].String() + "/p2p/" + relaySrv.Host.ID().String() + "/p2p-circuit"
	circuitMA, err := ma.NewMultiaddr(circuitAddrStr)
	if err != nil {
		t.Fatalf("new circuit multiaddr: %v", err)
	}
	relayedY := peer.AddrInfo{ID: nodeY.Host.ID(), Addrs: []ma.Multiaddr{circuitMA}}
	if err := nodeX.Host.Connect(ctx, relayedY); err != nil {
		t.Fatalf("connect nodeX to nodeY over relay: %v", err)
	}

	// Open stream over relay using WithAllowLimitedConn
	streamCtx := network.WithAllowLimitedConn(ctx, "relay test stream")
	stream, err := nodeX.Host.NewStream(streamCtx, nodeY.Host.ID(), testStreamProto)
	if err != nil {
		t.Fatalf("open stream over relay: %v", err)
	}
	defer stream.Close()

	payload := make([]byte, 64<<10)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	expectedHash := sha256.Sum256(payload)

	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("write payload over relay: %v", err)
	}

	respHash := make([]byte, 32)
	if _, err := io.ReadFull(stream, respHash); err != nil {
		t.Fatalf("read response hash: %v", err)
	}

	if !bytes.Equal(respHash, expectedHash[:]) {
		t.Fatalf("hash mismatch over relay stream! got %x, want %x", respHash, expectedHash)
	}

	select {
	case h := <-doneChan:
		if !bytes.Equal(h, expectedHash[:]) {
			t.Fatalf("server received corrupted data over relay")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for server completion")
	}
}

func TestRelayServerNetworkKeyIsolation(t *testing.T) {
	key1 := testNetworkKey(40)
	key2 := testNetworkKey(41) // Different private network key

	relayDir := filepath.Join(t.TempDir(), "isolated_relay")
	relaySrv, err := NewRelayServer(RelayServerOptions{
		ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
		DataDir:         relayDir,
		NetworkKey:      key1,
		AllowLoopback:   true,
	})
	if err != nil {
		t.Fatalf("start isolated relay: %v", err)
	}
	defer relaySrv.Close()

	relayInfo := peer.AddrInfo{
		ID:    relaySrv.Host.ID(),
		Addrs: relaySrv.Host.Addrs(),
	}

	// Node 1 with matching key
	node1 := newTestNode(t, "node1", filepath.Join(t.TempDir(), "node1"), key1)
	defer node1.Close()

	// Node 2 with different key
	node2 := newTestNode(t, "node2", filepath.Join(t.TempDir(), "node2"), key2)
	defer node2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Node 1 should connect successfully
	if err := node1.Host.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("node1 with matching key failed to connect: %v", err)
	}

	// Node 2 should fail to connect due to PSK / private network mismatch
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := node2.Host.Connect(ctx2, relayInfo); err == nil {
		t.Fatalf("node2 with wrong key unexpectedly connected to private relay")
	}
}
