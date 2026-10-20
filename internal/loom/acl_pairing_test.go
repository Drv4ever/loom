package loom

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestServiceACLEnforcement(t *testing.T) {
	key := testNetworkKey(40)
	nodeA := newTestNode(t, "nodeA", t.TempDir(), key)
	nodeB := newTestNode(t, "nodeB", t.TempDir(), key)
	nodeC := newTestNode(t, "nodeC", t.TempDir(), key)
	defer nodeA.Close()
	defer nodeB.Close()
	defer nodeC.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secret backend response"))
	}))
	defer backend.Close()

	// Node B publishes a service that ONLY allows Node A
	serviceID := "acl-protected-service"
	nodeB.mu.Lock()
	nodeB.services[serviceID] = ServiceRecord{
		ID:           serviceID,
		Name:         "ACL Service",
		Target:       backend.URL,
		AllowedPeers: []string{nodeA.Host.ID().String()},
	}
	nodeB.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA to nodeB: %v", err)
	}
	if err := nodeC.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeC to nodeB: %v", err)
	}

	// 1. Node A (allowed) sends HTTP request to Node B's service
	streamA, err := nodeA.Host.NewStream(ctx, nodeB.Host.ID(), httpProtocol)
	if err != nil {
		t.Fatalf("open stream A -> B: %v", err)
	}
	defer streamA.Close()

	reqA, _ := http.NewRequest(http.MethodGet, "http://loom.internal/proxy/"+nodeB.Host.ID().String()+"/"+serviceID+"/", nil)
	reqA.Header.Set("X-Loom-Service-ID", serviceID)
	_ = reqA.Write(streamA)
	respA, err := http.ReadResponse(bufio.NewReader(streamA), reqA)
	if err != nil {
		t.Fatalf("read response A: %v", err)
	}
	defer respA.Body.Close()
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("nodeA expected 200 OK, got %d", respA.StatusCode)
	}
	bodyA, _ := io.ReadAll(respA.Body)
	if !strings.Contains(string(bodyA), "secret backend response") {
		t.Fatalf("unexpected body A: %s", string(bodyA))
	}

	// 2. Node C (forbidden) sends HTTP request to Node B's service
	streamC, err := nodeC.Host.NewStream(ctx, nodeB.Host.ID(), httpProtocol)
	if err != nil {
		t.Fatalf("open stream C -> B: %v", err)
	}
	defer streamC.Close()

	reqC, _ := http.NewRequest(http.MethodGet, "http://loom.internal/proxy/"+nodeB.Host.ID().String()+"/"+serviceID+"/", nil)
	reqC.Header.Set("X-Loom-Service-ID", serviceID)
	_ = reqC.Write(streamC)
	respC, err := http.ReadResponse(bufio.NewReader(streamC), reqC)
	if err != nil {
		t.Fatalf("read response C: %v", err)
	}
	defer respC.Body.Close()
	if respC.StatusCode != http.StatusForbidden {
		t.Fatalf("nodeC expected 403 Forbidden, got %d", respC.StatusCode)
	}
	bodyC, _ := io.ReadAll(respC.Body)
	if !strings.Contains(string(bodyC), "service access denied") {
		t.Fatalf("expected 'service access denied' in body, got: %s", string(bodyC))
	}
}

func TestDeviceBlockingAndConnectionGater(t *testing.T) {
	app, err := New(context.Background(), Config{
		Name:    "App Node",
		Listen:  "127.0.0.1:8787",
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	defer app.Close(context.Background())

	otherNode := newTestNode(t, "Other", t.TempDir(), app.networkKey)
	defer otherNode.Close()
	otherID := otherNode.Host.ID().String()

	// Initial connect
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.main.Host.Connect(ctx, peer.AddrInfo{ID: otherNode.Host.ID(), Addrs: otherNode.Host.Addrs()}); err != nil {
		t.Fatalf("connect app to other: %v", err)
	}

	// Block the peer
	if err := app.BlockPeer(otherID); err != nil {
		t.Fatalf("block peer: %v", err)
	}

	if !app.IsPeerBlocked(otherID) {
		t.Fatalf("expected peer to be blocked")
	}
	blockedList := app.ListBlockedPeers()
	if len(blockedList) != 1 || blockedList[0] != otherID {
		t.Fatalf("unexpected blocked list: %v", blockedList)
	}

	// Verify connecting to blocked peer fails
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	err = app.main.Host.Connect(ctx2, peer.AddrInfo{ID: otherNode.Host.ID(), Addrs: otherNode.Host.Addrs()})
	if err == nil {
		t.Fatalf("expected connect to blocked peer to fail, but it succeeded")
	}

	// Unblock the peer
	if err := app.UnblockPeer(otherID); err != nil {
		t.Fatalf("unblock peer: %v", err)
	}
	if app.IsPeerBlocked(otherID) {
		t.Fatalf("expected peer to no longer be blocked")
	}
	if len(app.ListBlockedPeers()) != 0 {
		t.Fatalf("expected empty blocked list, got %v", app.ListBlockedPeers())
	}
}

func TestFolderAccessControl(t *testing.T) {
	app, err := New(context.Background(), Config{
		Name:    "Sync App",
		Listen:  "127.0.0.1:8787",
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	defer app.Close(context.Background())

	folderDir := filepath.Join(t.TempDir(), "test-folder")
	_ = os.MkdirAll(folderDir, 0700)
	_ = os.WriteFile(filepath.Join(folderDir, "hello.txt"), []byte("hello world"), 0600)

	// Add folder restricted to peerA and read-only
	peerA := "12D3KooWRandomPeerA11111111111111111111111111111111111111"
	peerB := "12D3KooWRandomPeerB22222222222222222222222222222222222222"

	folderID := "folder-123"
	app.mu.Lock()
	app.folders[folderID] = &folderEntry{
		Folder: Folder{
			ID:           folderID,
			Name:         "ACL Folder",
			Path:         folderDir,
			AllowedPeers: []string{peerA},
			ReadOnly:     true,
		},
	}
	app.mu.Unlock()

	// 1. peerA with read access (isWrite = false) -> should pass
	if err := app.checkFolderAccess(folderID, peerA, false); err != nil {
		t.Fatalf("expected peerA read to be allowed, got: %v", err)
	}

	// 2. peerA with write access (isWrite = true) -> should fail because ReadOnly = true
	if err := app.checkFolderAccess(folderID, peerA, true); err == nil {
		t.Fatalf("expected peerA write to fail for read-only folder")
	}

	// 3. peerB (not in AllowedPeers) -> should fail
	if err := app.checkFolderAccess(folderID, peerB, false); err == nil {
		t.Fatalf("expected peerB to be rejected due to AllowedPeers")
	}
}

func TestPairingAndDeviceAPI(t *testing.T) {
	app, err := New(context.Background(), Config{
		Name:    "API App",
		Listen:  "127.0.0.1:8787",
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	defer app.Close(context.Background())

	handler := app.Handler("127.0.0.1:8787")

	// 1. Generate pairing PIN
	resp := serveTestRequest(handler, http.MethodPost, "/api/pairing/generate", "application/json", `{"durationSeconds": 300}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("pairing generate status=%d: %s", resp.Code, resp.Body.String())
	}
	var session PairingSession
	if err := json.Unmarshal(resp.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode pairing session: %v", err)
	}
	if len(session.PIN) != 6 {
		t.Fatalf("expected 6-digit PIN, got %q", session.PIN)
	}
	if !strings.Contains(session.Formatted, "-") {
		t.Fatalf("expected formatted PIN with hyphen, got %q", session.Formatted)
	}

	// 2. Check pairing status
	respStatus := serveTestRequest(handler, http.MethodGet, "/api/pairing", "", "")
	if respStatus.Code != http.StatusOK {
		t.Fatalf("pairing status code=%d", respStatus.Code)
	}
	var statusBody map[string]any
	_ = json.Unmarshal(respStatus.Body.Bytes(), &statusBody)
	if statusBody["active"] != true {
		t.Fatalf("expected active=true in pairing status")
	}

	// 3. Claim with wrong PIN
	claimFail := serveTestRequest(handler, http.MethodPost, "/api/pairing/claim", "application/json", `{"pin": "000-000"}`)
	if claimFail.Code == http.StatusOK {
		t.Fatalf("expected claim with wrong PIN to fail")
	}

	// 4. Claim with valid PIN
	claimSuccess := serveTestRequest(handler, http.MethodPost, "/api/pairing/claim", "application/json", fmt.Sprintf(`{"pin": %q}`, session.Formatted))
	if claimSuccess.Code != http.StatusOK {
		t.Fatalf("expected claim success, got status=%d: %s", claimSuccess.Code, claimSuccess.Body.String())
	}
	var payload PairingPayload
	if err := json.Unmarshal(claimSuccess.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.NetworkKey == "" {
		t.Fatalf("expected non-empty network key in claimed payload")
	}

	// 5. Block and Unblock device via API
	testPeerNode := newTestNode(t, "testPeer", t.TempDir(), app.networkKey)
	defer testPeerNode.Close()
	testPeerID := testPeerNode.Host.ID().String()
	blockResp := serveTestRequest(handler, http.MethodPost, "/api/devices/"+testPeerID+"/block", "application/json", `{}`)
	if blockResp.Code != http.StatusOK {
		t.Fatalf("block device status=%d: %s", blockResp.Code, blockResp.Body.String())
	}

	listBlocked := serveTestRequest(handler, http.MethodGet, "/api/devices/blocked", "", "")
	if listBlocked.Code != http.StatusOK {
		t.Fatalf("list blocked status=%d", listBlocked.Code)
	}
	var blockedResp struct {
		Blocked []string `json:"blocked"`
	}
	_ = json.Unmarshal(listBlocked.Body.Bytes(), &blockedResp)
	if len(blockedResp.Blocked) != 1 || blockedResp.Blocked[0] != testPeerID {
		t.Fatalf("expected testPeerID in blocked list, got: %v", blockedResp.Blocked)
	}

	unblockResp := serveTestRequest(handler, http.MethodPost, "/api/devices/"+testPeerID+"/unblock", "application/json", `{}`)
	if unblockResp.Code != http.StatusOK {
		t.Fatalf("unblock device status=%d: %s", unblockResp.Code, unblockResp.Body.String())
	}
}
