package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ayush/loom/internal/loom"
)

func mockState() loom.State {
	return loom.State{
		Network: loom.NetworkState{
			ID:        "loom-test-net",
			Name:      "Test Mesh",
			Connected: true,
			Demo:      false,
			PeerID:    "12D3KooWPrimaryPeerID",
			Addresses: []string{"/ip4/192.168.1.10/tcp/4001/p2p/12D3KooWPrimaryPeerID"},
			Version:   "0.1.0",
		},
		Devices: []loom.Device{
			{
				ID:         "12D3KooWPrimaryPeerID",
				Name:       "Primary Laptop",
				Platform:   "darwin",
				Online:     true,
				Self:       true,
				Connection: "local",
				Address:    "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWPrimaryPeerID",
				LastSeen:   time.Now().UTC(),
			},
			{
				ID:         "12D3KooWStudioPeerID",
				Name:       "Studio Workstation",
				Platform:   "linux",
				Online:     true,
				Self:       false,
				Connection: "direct",
				Address:    "/ip4/192.168.1.50/tcp/4001/p2p/12D3KooWStudioPeerID",
				LastSeen:   time.Now().UTC().Add(-1 * time.Minute),
			},
		},
		Services: []loom.Service{
			{
				ID:         "svc-1234",
				Name:       "Project Preview",
				DeviceID:   "12D3KooWPrimaryPeerID",
				DeviceName: "Primary Laptop",
				Protocol:   "http",
				Target:     "http://127.0.0.1:3000",
				Port:       3000,
				Status:     "published",
				URL:        "/proxy/12D3KooWPrimaryPeerID/svc-1234/",
			},
		},
		Folders: []loom.Folder{
			{
				ID:       "folder-5678",
				Name:     "Design Assets",
				Path:     "/home/user/Design",
				PeerID:   "12D3KooWStudioPeerID",
				PeerName: "Studio Workstation",
				Status:   "synced",
				Files:    12,
				Bytes:    10485760, // 10 MiB
				Progress: 100,
				LastSync: time.Now().UTC().Add(-5 * time.Minute),
				Paused:   false,
			},
		},
		ReceivedFolders: []loom.ReceivedFolder{
			{
				ID:           "folder-recv-1",
				Name:         "Code Repo",
				Path:         "/home/user/.loom/received/folder-recv-1",
				PeerID:       "12D3KooWStudioPeerID",
				PeerName:     "Studio Workstation",
				Bytes:        5242880,
				LastReceived: time.Now().UTC().Add(-2 * time.Minute),
			},
		},
		Stats: loom.Stats{
			OnlineDevices:    2,
			Services:         1,
			SyncedFiles:      12,
			TransferredBytes: 10485760,
		},
	}
}

func newMockServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockState())
	})
	mux.HandleFunc("GET /api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"version":       "0.1.0",
			"uptimeSeconds": 120,
			"limits": map[string]any{
				"fileBytes":         104857600,
				"proxyRequestBytes": 10485760,
			},
		})
	})
	mux.HandleFunc("POST /api/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loom.Device{
			ID:     "12D3KooWNewPeer",
			Name:   "New Device",
			Online: true,
		})
	})
	mux.HandleFunc("POST /api/services", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		name, _ := req["name"].(string)
		target, _ := req["target"].(string)
		trusted, _ := req["trusted"].(bool)
		_ = json.NewEncoder(w).Encode(loom.Service{
			ID:      "new-svc-id",
			Name:    name,
			Target:  target,
			Trusted: trusted,
			URL:     "/proxy/self/new-svc-id/",
		})
	})
	mux.HandleFunc("DELETE /api/services/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/forwards", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*loom.Forward{
			{
				ID:        "fwd-101",
				LocalPort: 5432,
				PeerID:    "12D3KooWStudioPeerID",
				PeerName:  "Studio Workstation",
				Target:    "127.0.0.1:5432",
				Status:    "active",
			},
		})
	})
	mux.HandleFunc("POST /api/forwards", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		port := 0
		if p, ok := req["localPort"].(float64); ok {
			port = int(p)
		}
		target, _ := req["target"].(string)
		peerID, _ := req["peerId"].(string)
		serviceID, _ := req["serviceId"].(string)
		_ = json.NewEncoder(w).Encode(&loom.Forward{
			ID:        "fwd-new",
			LocalPort: port,
			PeerID:    peerID,
			PeerName:  "Studio Workstation",
			ServiceID: serviceID,
			Target:    target,
			Status:    "active",
		})
	})
	mux.HandleFunc("DELETE /api/forwards/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/folders", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loom.Folder{
			ID:       "new-folder-id",
			Name:     req["name"],
			Path:     req["path"],
			PeerID:   req["peerId"],
			PeerName: "Studio Workstation",
			Status:   "pending",
		})
	})
	mux.HandleFunc("POST /api/folders/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loom.Folder{
			ID:     r.PathValue("id"),
			Name:   "Design Assets",
			Status: "syncing",
		})
	})
	mux.HandleFunc("POST /api/folders/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]bool
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loom.Folder{
			ID:     r.PathValue("id"),
			Name:   "Design Assets",
			Paused: req["paused"],
		})
	})
	mux.HandleFunc("DELETE /api/folders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/folders/{id}/conflicts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]loom.Conflict{
			{
				ID:           "conf-101",
				FolderID:     r.PathValue("id"),
				Path:         "notes.txt",
				ConflictPath: "notes.sync-conflict-20261019-153000-studio.txt",
				DeviceName:   "Studio Workstation",
				CreatedAt:    time.Now().UTC(),
			},
		})
	})
	mux.HandleFunc("POST /api/folders/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/folders/{id}/trash", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]loom.TrashEntry{
			{
				OriginalPath: "old.txt",
				TrashPath:    "/trash/old.txt.deleted",
				Size:         1024,
				DeletedAt:    time.Now().UTC(),
			},
		})
	})
	mux.HandleFunc("POST /api/network", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]bool
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loom.NetworkState{
			Connected: req["connected"],
		})
	})
	mux.HandleFunc("GET /api/relays", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]loom.RelayInfo{
			{
				Address: "/ip4/198.51.100.1/tcp/4001/p2p/12D3KooWRelayServer1",
				PeerID:  "12D3KooWRelayServer1",
				Status:  "connected",
			},
		})
	})
	mux.HandleFunc("POST /api/relays", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loom.RelayInfo{
			Address: req["address"],
			PeerID:  "12D3KooWNewRelay",
			Status:  "connected",
		})
	})
	mux.HandleFunc("DELETE /api/relays/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })
	return server
}

func TestRunStatus(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"status"})
	if err != nil {
		t.Fatalf("run status: %v", err)
	}

	out := outBuf.String()
	if !strings.Contains(out, "Test Mesh") {
		t.Errorf("output missing network name: %s", out)
	}
	if !strings.Contains(out, "Studio Workstation") {
		t.Errorf("output missing device name: %s", out)
	}
	if !strings.Contains(out, "Project Preview") {
		t.Errorf("output missing service name: %s", out)
	}
	if !strings.Contains(out, "Design Assets") {
		t.Errorf("output missing folder name: %s", out)
	}
}

func TestRunStatusJSON(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"status", "--json"})
	if err != nil {
		t.Fatalf("run status --json: %v", err)
	}

	var state loom.State
	if err := json.Unmarshal(outBuf.Bytes(), &state); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if state.Network.Name != "Test Mesh" {
		t.Errorf("got %q, want 'Test Mesh'", state.Network.Name)
	}
}

func TestRunDevices(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"devices"})
	if err != nil {
		t.Fatalf("run devices: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Primary Laptop") {
		t.Errorf("output missing device: %s", outBuf.String())
	}

	// Connect
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"devices", "connect", "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWNewPeer"})
	if err != nil {
		t.Fatalf("run devices connect: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Connected to device") {
		t.Errorf("output missing success confirmation: %s", outBuf.String())
	}
}

func TestRunServices(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	// List
	err := runner.Execute(context.Background(), []string{"services"})
	if err != nil {
		t.Fatalf("run services: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Project Preview") {
		t.Errorf("output missing service: %s", outBuf.String())
	}

	// Publish
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"publish", "http://127.0.0.1:8080", "--name", "Web API"})
	if err != nil {
		t.Fatalf("run publish: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Published service \"Web API\"") {
		t.Errorf("output missing confirmation: %s", outBuf.String())
	}

	// Unpublish
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"unpublish", "svc-1234"})
	if err != nil {
		t.Fatalf("run unpublish: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Unpublished service \"svc-1234\"") {
		t.Errorf("output missing confirmation: %s", outBuf.String())
	}
}

func TestRunSync(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	// List
	err := runner.Execute(context.Background(), []string{"sync"})
	if err != nil {
		t.Fatalf("run sync: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Design Assets") {
		t.Errorf("output missing sending folder: %s", outBuf.String())
	}
	if !strings.Contains(outBuf.String(), "Code Repo") {
		t.Errorf("output missing received folder: %s", outBuf.String())
	}

	// Add
	tempDir := t.TempDir()
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"sync", "add", tempDir, "--to", "Studio Workstation", "--name", "Docs"})
	if err != nil {
		t.Fatalf("run sync add: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Added sync folder \"Docs\"") {
		t.Errorf("output missing add confirmation: %s", outBuf.String())
	}

	// Now
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"sync", "now", "folder-5678"})
	if err != nil {
		t.Fatalf("run sync now: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Sync triggered") {
		t.Errorf("output missing sync triggered: %s", outBuf.String())
	}

	// Pause
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"sync", "pause", "folder-5678"})
	if err != nil {
		t.Fatalf("run sync pause: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Paused sync") {
		t.Errorf("output missing pause confirmation: %s", outBuf.String())
	}

	// Resume
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"sync", "resume", "folder-5678"})
	if err != nil {
		t.Fatalf("run sync resume: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Resumed sync") {
		t.Errorf("output missing resume confirmation: %s", outBuf.String())
	}

	// Remove
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"sync", "remove", "folder-5678"})
	if err != nil {
		t.Fatalf("run sync remove: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Stopped tracking folder") {
		t.Errorf("output missing remove confirmation: %s", outBuf.String())
	}
}

func TestRunNetwork(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	// Pause
	err := runner.Execute(context.Background(), []string{"network", "pause"})
	if err != nil {
		t.Fatalf("run network pause: %v", err)
	}
	if !strings.Contains(outBuf.String(), "paused") {
		t.Errorf("output missing paused confirmation: %s", outBuf.String())
	}

	// Resume
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"network", "resume"})
	if err != nil {
		t.Fatalf("run network resume: %v", err)
	}
	if !strings.Contains(outBuf.String(), "resumed") {
		t.Errorf("output missing resumed confirmation: %s", outBuf.String())
	}
}

func TestRunDoctor(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"doctor"})
	if err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "Running & Healthy") {
		t.Errorf("doctor missing daemon health: %s", out)
	}
	if !strings.Contains(out, "Mesh Network ID") {
		t.Errorf("doctor missing mesh network id: %s", out)
	}
}

func TestFormatHelpers(t *testing.T) {
	if FormatBytes(500) != "500 B" {
		t.Errorf("FormatBytes(500) = %q", FormatBytes(500))
	}
	if FormatBytes(1024) != "1.0 KiB" {
		t.Errorf("FormatBytes(1024) = %q", FormatBytes(1024))
	}
	if FormatBytes(1048576) != "1.0 MiB" {
		t.Errorf("FormatBytes(1048576) = %q", FormatBytes(1048576))
	}

	now := time.Now()
	if FormatTimeRelative(now) != "just now" {
		t.Errorf("FormatTimeRelative(now) = %q", FormatTimeRelative(now))
	}
	if FormatTimeRelative(time.Time{}) != "never" {
		t.Errorf("FormatTimeRelative(zero) = %q", FormatTimeRelative(time.Time{}))
	}
}

func TestRunSyncConflicts(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"sync", "conflicts", "folder-5678"})
	if err != nil {
		t.Fatalf("run sync conflicts: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "conf-101") || !strings.Contains(out, "notes.txt") {
		t.Errorf("conflicts output missing details: %s", out)
	}
}

func TestRunSyncResolve(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"sync", "resolve", "folder-5678", "conf-101", "--keep", "remote"})
	if err != nil {
		t.Fatalf("run sync resolve: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "Resolved conflict") || !strings.Contains(out, "remote") {
		t.Errorf("resolve output missing confirmation: %s", out)
	}
}

func TestRunSyncTrash(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"sync", "trash", "folder-5678"})
	if err != nil {
		t.Fatalf("run sync trash: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "old.txt") {
		t.Errorf("trash output missing file: %s", out)
	}
}

func TestRunForwards(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"forwards"})
	if err != nil {
		t.Fatalf("run forwards: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "fwd-101") || !strings.Contains(out, "5432") || !strings.Contains(out, "Studio Workstation") {
		t.Errorf("forwards output missing forward info: %s", out)
	}
}

func TestRunForwardCreateAndStop(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	// Create forward
	err := runner.Execute(context.Background(), []string{"forward", "Studio Workstation:5432", "--port", "5433"})
	if err != nil {
		t.Fatalf("run forward: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "Port forward active") || !strings.Contains(out, "5433") {
		t.Errorf("forward creation missing confirmation: %s", out)
	}

	// Stop forward
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"forward", "stop", "fwd-101"})
	if err != nil {
		t.Fatalf("run forward stop: %v", err)
	}
	out = outBuf.String()
	if !strings.Contains(out, "Stopped port forward \"fwd-101\"") {
		t.Errorf("forward stop missing confirmation: %s", out)
	}
}

func TestRunServicesPublishTrusted(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"publish", "http://127.0.0.1:4000", "--name", "Dashboard App", "--trusted"})
	if err != nil {
		t.Fatalf("run publish trusted: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "Published service \"Dashboard App\"") {
		t.Errorf("trusted publish missing confirmation: %s", out)
	}
	if !strings.Contains(out, "Trusted") {
		t.Errorf("trusted publish missing Trusted indicator: %s", out)
	}
}

func TestRunRelaysListAddRemove(t *testing.T) {
	server := newMockServer(t)
	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	// List relays
	err := runner.Execute(context.Background(), []string{"relays"})
	if err != nil {
		t.Fatalf("run relays: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "Configured Relays") || !strings.Contains(out, "12D3KooWRelayServer1") {
		t.Errorf("relays list missing expected output: %s", out)
	}

	// Add relay
	outBuf.Reset()
	relayAddr := "/ip4/203.0.113.10/tcp/4001/p2p/12D3KooWNewRelay"
	err = runner.Execute(context.Background(), []string{"relays", "add", relayAddr})
	if err != nil {
		t.Fatalf("run relays add: %v", err)
	}
	out = outBuf.String()
	if !strings.Contains(out, "Added relay") || !strings.Contains(out, "12D3KooWNewRelay") {
		t.Errorf("relays add missing confirmation: %s", out)
	}

	// Remove relay
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"relays", "remove", "12D3KooWNewRelay"})
	if err != nil {
		t.Fatalf("run relays remove: %v", err)
	}
	out = outBuf.String()
	if !strings.Contains(out, "Removed relay") {
		t.Errorf("relays remove missing confirmation: %s", out)
	}
}

func TestRunPairCommands(t *testing.T) {
	mockSession := loom.PairingSession{
		PIN:       "583921",
		Formatted: "583-921",
		Topic:     "aabbccddeeff11223344",
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		Payload: loom.PairingPayload{
			NetworkKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			NetworkID:  "loom-test-net",
			IssuedBy:   "12D3KooWPrimaryPeerID",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/pairing/generate":
			_ = json.NewEncoder(w).Encode(mockSession)
		case r.Method == http.MethodGet && r.URL.Path == "/api/pairing":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "session": mockSession})
		case r.Method == http.MethodPost && r.URL.Path == "/api/pairing/claim":
			_ = json.NewEncoder(w).Encode(mockSession.Payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	// 1. loom pair
	err := runner.Execute(context.Background(), []string{"pair"})
	if err != nil {
		t.Fatalf("run pair: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "583-921") || !strings.Contains(out, "Ephemeral Device Pairing") {
		t.Errorf("pair missing PIN or header: %s", out)
	}

	// 2. loom pair status
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"pair", "status"})
	if err != nil {
		t.Fatalf("run pair status: %v", err)
	}
	out = outBuf.String()
	if !strings.Contains(out, "583-921") {
		t.Errorf("pair status missing PIN: %s", out)
	}

	// 3. loom pair join
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"pair", "join", "583-921"})
	if err != nil {
		t.Fatalf("run pair join: %v", err)
	}
	out = outBuf.String()
	if !strings.Contains(out, "Successfully paired") || !strings.Contains(out, "loom-test-net") {
		t.Errorf("pair join missing success output: %s", out)
	}
}

func TestRunDevicesBlockUnblockBlocked(t *testing.T) {
	blockedPeers := []string{"12D3KooWBlockedPeer1"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/devices/blocked":
			_ = json.NewEncoder(w).Encode(map[string]any{"blocked": blockedPeers})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/devices/") && strings.HasSuffix(r.URL.Path, "/block"):
			peerID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/devices/"), "/block")
			blockedPeers = append(blockedPeers, peerID)
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/devices/") && strings.HasSuffix(r.URL.Path, "/unblock"):
			blockedPeers = nil
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	// 1. loom devices blocked
	err := runner.Execute(context.Background(), []string{"devices", "blocked"})
	if err != nil {
		t.Fatalf("devices blocked: %v", err)
	}
	if !strings.Contains(outBuf.String(), "12D3KooWBlockedPeer1") {
		t.Errorf("expected 12D3KooWBlockedPeer1 in blocked output: %s", outBuf.String())
	}

	// 2. loom devices block
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"devices", "block", "12D3KooWNewBlockedPeer"})
	if err != nil {
		t.Fatalf("devices block: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Blocked device") {
		t.Errorf("expected Blocked device confirmation: %s", outBuf.String())
	}

	// 3. loom devices unblock
	outBuf.Reset()
	err = runner.Execute(context.Background(), []string{"devices", "unblock", "12D3KooWBlockedPeer1"})
	if err != nil {
		t.Fatalf("devices unblock: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Unblocked device") {
		t.Errorf("expected Unblocked device confirmation: %s", outBuf.String())
	}
}

func TestRunACLCommand(t *testing.T) {
	state := mockState()
	state.BlockedDevices = []string{"12D3KooWBlocked1"}
	state.Services[0].AllowedPeers = []string{"12D3KooWStudioPeerID"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/state" {
			_ = json.NewEncoder(w).Encode(state)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	var outBuf, errBuf bytes.Buffer
	runner := &Runner{
		Client: NewClient(server.URL),
		Out:    &outBuf,
		Err:    &errBuf,
	}

	err := runner.Execute(context.Background(), []string{"acl"})
	if err != nil {
		t.Fatalf("run acl: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "Access Control Overview") {
		t.Errorf("missing ACL header: %s", out)
	}
	if !strings.Contains(out, "12D3KooWBlocked1") {
		t.Errorf("missing blocked peer: %s", out)
	}
	if !strings.Contains(out, "12D3KooWStudioPeerID") {
		t.Errorf("missing allowed peer in service grants: %s", out)
	}
}
