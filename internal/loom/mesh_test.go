package loom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func testNetworkKey(value byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = value
	}
	return key
}

func newTestNode(t *testing.T, name, dataDir string, key []byte) *Node {
	t.Helper()
	node, err := NewNode(NodeOptions{
		Name: name, Platform: "test", DataDir: dataDir,
		IdentityPath: filepath.Join(dataDir, "identity.key"),
		ReceiveDir:   filepath.Join(dataDir, "received"), NetworkKey: key,
		ListenAddress: "/ip4/127.0.0.1/tcp/0",
	})
	if err != nil {
		t.Fatalf("create node %q: %v", name, err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

func TestPrivateNetworkRejectsWrongPSK(t *testing.T) {
	left := newTestNode(t, "left", filepath.Join(t.TempDir(), "left"), testNetworkKey(1))
	right := newTestNode(t, "right", filepath.Join(t.TempDir(), "right"), testNetworkKey(2))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	err := left.Host.Connect(ctx, peer.AddrInfo{ID: right.Host.ID(), Addrs: right.Host.Addrs()})
	if err == nil {
		t.Fatal("connect succeeded with a different private-network PSK")
	}
	if left.Host.Network().Connectedness(right.Host.ID()) == network.Connected {
		t.Fatal("wrong-PSK peer became connected")
	}
}

func TestLoadNetworkKeySecuresExistingKeyFile(t *testing.T) {
	key := testNetworkKey(9)
	path := filepath.Join(t.TempDir(), "network.key")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadNetworkKey(path, "")
	if err != nil {
		t.Fatalf("load network key: %v", err)
	}
	if !bytes.Equal(loaded, key) {
		t.Fatal("loaded network key differs from stored key")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("stored network key permissions = %04o, want 0600", mode)
	}
}

func TestDisabledNodeRejectsInboundReconnects(t *testing.T) {
	key := testNetworkKey(4)
	left := newTestNode(t, "left", filepath.Join(t.TempDir(), "left"), key)
	right := newTestNode(t, "right", filepath.Join(t.TempDir(), "right"), key)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	info := peer.AddrInfo{ID: right.Host.ID(), Addrs: right.Host.Addrs()}
	if err := left.Host.Connect(ctx, info); err != nil {
		t.Fatalf("initial same-key connection: %v", err)
	}
	right.SetEnabled(false)
	_ = left.Host.Network().ClosePeer(right.Host.ID())
	_ = right.Host.Network().ClosePeer(left.Host.ID())
	if err := left.Host.Connect(ctx, info); err == nil {
		t.Fatal("disabled peer accepted an inbound reconnect")
	}
}

func TestFolderTransferChecksIntegrityPreservesVersionAndRejectsTraversal(t *testing.T) {
	key := testNetworkKey(3)
	sender := newTestNode(t, "sender", filepath.Join(t.TempDir(), "sender"), key)
	receiver := newTestNode(t, "receiver", filepath.Join(t.TempDir(), "receiver"), key)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sender.Host.Connect(ctx, peer.AddrInfo{ID: receiver.Host.ID(), Addrs: receiver.Host.Addrs()}); err != nil {
		t.Fatalf("connect test nodes: %v", err)
	}

	const folderID = "test-folder"
	transferWithDigest := func(path string, contents []byte, digest string) error {
		_, err := sender.TransferFile(ctx, receiver.Host.ID(), folderID, fileDescriptor{
			Path: path, Size: int64(len(contents)), SHA256: digest,
		}, bytes.NewReader(contents))
		return err
	}
	transfer := func(path string, contents []byte) error {
		digest := sha256.Sum256(contents)
		return transferWithDigest(path, contents, hex.EncodeToString(digest[:]))
	}
	oldContents := []byte("the original version\n")
	if err := transfer("nested/asset.txt", oldContents); err != nil {
		t.Fatalf("initial transfer: %v", err)
	}
	newContents := []byte("the updated version\n")
	if err := transfer("nested/asset.txt", newContents); err != nil {
		t.Fatalf("updated transfer: %v", err)
	}
	receivedPath := filepath.Join(receiver.ReceiveDir, folderID, "nested", "asset.txt")
	actual, err := os.ReadFile(receivedPath)
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if !bytes.Equal(actual, newContents) {
		t.Fatalf("received file = %q, want %q", actual, newContents)
	}
	historyPath := filepath.Join(receiver.HistoryDir, folderID, "nested")
	entries, err := os.ReadDir(historyPath)
	if err != nil {
		t.Fatalf("read version history: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("version history contains %d versions, want 1", len(entries))
	}
	oldSaved, err := os.ReadFile(filepath.Join(historyPath, entries[0].Name()))
	if err != nil || !bytes.Equal(oldSaved, oldContents) {
		t.Fatalf("preserved history = %q, err %v; want original bytes", oldSaved, err)
	}
	if err := transferWithDigest("nested/asset.txt", []byte("invalid replacement"), strings.Repeat("0", 64)); err == nil {
		t.Fatal("file with a mismatched SHA-256 unexpectedly transferred")
	}
	actual, err = os.ReadFile(receivedPath)
	if err != nil || !bytes.Equal(actual, newContents) {
		t.Fatalf("checksum failure changed existing file to %q, err %v", actual, err)
	}

	if err := transfer("../escaped.txt", []byte("must stay inside the managed root")); err == nil {
		t.Fatal("traversal transfer unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(receiver.ReceiveDir, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside folder root: stat error %v", err)
	}
	outside := t.TempDir()
	unsafeParent := filepath.Join(receiver.ReceiveDir, folderID, "unsafe")
	if err := os.Symlink(outside, unsafeParent); err != nil {
		t.Fatalf("create destination symlink: %v", err)
	}
	if err := transfer("unsafe/secret.txt", []byte("must not follow destination symlink")); err == nil {
		t.Fatal("transfer through a destination symlink unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("transfer wrote through a destination symlink: stat error %v", err)
	}
}

func TestFailedTransferDoesNotAdvanceManifestAndCanRetry(t *testing.T) {
	key := testNetworkKey(5)
	sender := newTestNode(t, "sender", filepath.Join(t.TempDir(), "sender"), key)
	receiver := newTestNode(t, "receiver", filepath.Join(t.TempDir(), "receiver"), key)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := sender.Host.Connect(ctx, peer.AddrInfo{ID: receiver.Host.ID(), Addrs: receiver.Host.Addrs()}); err != nil {
		t.Fatalf("connect test nodes: %v", err)
	}

	appDir := t.TempDir()
	source := filepath.Join(appDir, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	contents := []byte("retry after receiver error\n")
	if err := os.WriteFile(filepath.Join(source, "retry.txt"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	const folderID = "retry-folder"
	destination := filepath.Join(receiver.ReceiveDir, folderID, "retry.txt")
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	folder := &folderEntry{Folder: Folder{ID: folderID, Name: "Retry", Path: source, PeerID: receiver.Host.ID().String(), Status: "pending"}, manifest: make(map[string]string)}
	app := &App{
		main: sender, connected: true, dataDir: appDir,
		statePath: filepath.Join(appDir, "state.json"), ctx: ctx,
		folders: map[string]*folderEntry{folderID: folder},
	}
	if err := app.syncFolder(ctx, folderID); err == nil {
		t.Fatal("sync succeeded while the destination path was a directory")
	}
	if _, exists := folder.manifest["retry.txt"]; exists {
		t.Fatal("failed transfer advanced the acknowledged manifest")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := app.syncFolder(ctx, folderID); err != nil {
		t.Fatalf("sync retry failed: %v", err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(actual, contents) {
		t.Fatalf("retried destination = %q, err %v", actual, err)
	}
}

func TestFreshNormalStateUsesEmptyArrays(t *testing.T) {
	app, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("start normal app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	response := serveTestRequest(app.Handler("127.0.0.1:8787"), http.MethodGet, "/api/state", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("state status=%d: %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	for _, field := range []string{"devices", "services", "folders", "receivedFolders", "activity"} {
		if value := string(body[field]); !strings.HasPrefix(value, "[") {
			t.Errorf("state.%s = %s, want a JSON array", field, value)
		}
	}
}

func TestReceivedFileDownloadAndSenderIsolation(t *testing.T) {
	key := testNetworkKey(8)
	sender := newTestNode(t, "sender", t.TempDir(), key)
	receiver := newTestNode(t, "receiver", t.TempDir(), key)
	other := newTestNode(t, "other", t.TempDir(), key)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, node := range []*Node{sender, other} {
		if err := node.Host.Connect(ctx, peer.AddrInfo{ID: receiver.Host.ID(), Addrs: receiver.Host.Addrs()}); err != nil {
			t.Fatal(err)
		}
	}
	contents := []byte("verified receipt\n")
	digest := sha256.Sum256(contents)
	desc := fileDescriptor{Path: "nested/file #1.txt", Size: int64(len(contents)), SHA256: hex.EncodeToString(digest[:])}
	if _, err := sender.TransferFile(ctx, receiver.Host.ID(), "receipt-test", desc, bytes.NewReader(contents), "Incoming demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.TransferFile(ctx, receiver.Host.ID(), "receipt-test", desc, bytes.NewReader(contents)); err == nil {
		t.Fatal("another sender overwrote an owned folder")
	}
	app := &App{main: receiver}
	handler := app.Handler("127.0.0.1:8787")
	url := "/api/received/receipt-test/files/nested/file%20%231.txt"
	response := serveTestRequest(handler, http.MethodGet, url, "", "")
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), contents) {
		t.Fatalf("download status=%d bytes=%q", response.Code, response.Body.Bytes())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("download must be served as an attachment with nosniff")
	}
	response = serveTestRequest(handler, http.MethodGet, "/api/received/receipt-test/files/%2e%2e/identity.key", "", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("traversal status=%d, want 400", response.Code)
	}
	response = serveTestRequest(handler, http.MethodGet, "/api/received/receipt-test/files/unknown.txt", "", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("unregistered file status=%d, want 404", response.Code)
	}
	destination := filepath.Join(receiver.ReceiveDir, "receipt-test", "nested", "file #1.txt")
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(secret, []byte("outside managed root"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, destination); err != nil {
		t.Fatal(err)
	}
	response = serveTestRequest(handler, http.MethodGet, url, "", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("escaping symlink status=%d, want 404", response.Code)
	}
}

func TestNormalNetworkPausePersistsAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	app, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: dataDir})
	if err != nil {
		t.Fatalf("start normal app: %v", err)
	}
	response := serveTestRequest(app.Handler("127.0.0.1:8787"), http.MethodPost, "/api/network", "application/json", `{"connected":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("pause normal network status=%d: %s", response.Code, response.Body.String())
	}
	_ = app.Close(context.Background())

	restarted, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: dataDir})
	if err != nil {
		t.Fatalf("restart paused normal app: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	if restarted.State().Network.Connected || restarted.main.enabled.Load() {
		t.Fatal("network pause was not preserved across restart")
	}
}

func TestDemoRestartPreservesNetworkAndFolderPause(t *testing.T) {
	dataDir := t.TempDir()
	app, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: dataDir, Demo: true})
	if err != nil {
		t.Fatalf("start demo app: %v", err)
	}
	networkResponse := serveTestRequest(app.Handler("127.0.0.1:8787"), http.MethodPost, "/api/network", "application/json", `{"connected":false}`)
	if networkResponse.Code != http.StatusOK {
		t.Fatalf("pause demo network status=%d: %s", networkResponse.Code, networkResponse.Body.String())
	}
	folderResponse := serveTestRequest(app.Handler("127.0.0.1:8787"), http.MethodPost, "/api/folders/demo-shared-assets/pause", "application/json", `{"paused":true}`)
	if folderResponse.Code != http.StatusOK {
		t.Fatalf("pause demo folder status=%d: %s", folderResponse.Code, folderResponse.Body.String())
	}
	pausedFile := filepath.Join(dataDir, "demo-source", "paused-only.txt")
	pausedContents := []byte("this must remain unsent during restart\n")
	if err := os.WriteFile(pausedFile, pausedContents, 0600); err != nil {
		t.Fatal(err)
	}
	transferredBefore := app.State().Stats.TransferredBytes
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("close first demo app: %v", err)
	}

	restarted, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: dataDir, Demo: true})
	if err != nil {
		t.Fatalf("restart paused demo app: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	state := restarted.State()
	if state.Network.Connected {
		t.Fatal("demo network pause was not preserved across restart")
	}
	if state.Stats.TransferredBytes != transferredBefore {
		t.Fatalf("restart transferred bytes increased from %d to %d while paused", transferredBefore, state.Stats.TransferredBytes)
	}
	var folder Folder
	for _, value := range state.Folders {
		if value.ID == "demo-shared-assets" {
			folder = value
		}
	}
	if !folder.Paused || folder.Status != "paused" {
		t.Fatalf("demo folder pause was not preserved: %+v", folder)
	}
	received := filepath.Join(dataDir, "nodes", "linux", "received", folder.ID, "paused-only.txt")
	if _, err := os.Stat(received); !os.IsNotExist(err) {
		t.Fatalf("paused file was transferred on restart: stat error %v", err)
	}
	resumeResponse := serveTestRequest(restarted.Handler("127.0.0.1:8787"), http.MethodPost, "/api/network", "application/json", `{"connected":true}`)
	if resumeResponse.Code != http.StatusOK {
		t.Fatalf("resume restarted demo network status=%d: %s", resumeResponse.Code, resumeResponse.Body.String())
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, device := range restarted.State().Devices {
			if device.Platform == "linux" {
				return device.Online && device.Connection == "relay"
			}
		}
		return false
	})
	if restarted.State().Stats.TransferredBytes != transferredBefore {
		t.Fatal("resuming network transferred files from a paused folder")
	}
}

func TestDemoUsesRelayForProxyAndFolderTransfersAndReconnects(t *testing.T) {
	dataDir := t.TempDir()
	app, err := New(context.Background(), Config{Listen: "127.0.0.1:8787", DataDir: dataDir, Demo: true})
	if err != nil {
		t.Fatalf("start demo app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	handler := app.Handler("127.0.0.1:8787")
	state := app.State()
	if len(state.Devices) != 3 || !state.Devices[0].Self || state.Devices[1].Name > state.Devices[2].Name {
		t.Fatalf("devices are not ordered self-first then by name: %+v", state.Devices)
	}
	for i := 0; i < 5; i++ {
		observed := app.State().Devices
		for j := range state.Devices {
			if state.Devices[j].ID != observed[j].ID {
				t.Fatalf("device order changed between state reads: %v then %v", deviceIDs(state.Devices), deviceIDs(observed))
			}
		}
	}
	var linux Device
	var statusService Service
	for _, device := range state.Devices {
		if device.Platform == "linux" {
			linux = device
		}
	}
	for _, service := range state.Services {
		if service.ID == "linux-status" {
			statusService = service
		}
	}
	if linux.ID == "" || !linux.Online || linux.Connection != "relay" {
		t.Fatalf("Linux demo peer is not online through relay: %+v", linux)
	}
	if statusService.ID == "" || statusService.DeviceID != linux.ID {
		t.Fatalf("Linux sample service was not published: %+v", statusService)
	}

	proxyPath := "/proxy/" + linux.ID + "/" + statusService.ID + "/status/check"
	proxyResponse := serveTestRequest(handler, http.MethodGet, proxyPath, "", "")
	if proxyResponse.Code != http.StatusOK {
		t.Fatalf("relay proxy status = %d: %s", proxyResponse.Code, proxyResponse.Body.String())
	}
	if !strings.Contains(proxyResponse.Body.String(), statusService.Name) || !strings.Contains(proxyResponse.Body.String(), linux.Name) || !strings.Contains(proxyResponse.Body.String(), "/status/check") {
		t.Fatalf("unexpected proxied sample page: %s", proxyResponse.Body.String())
	}
	if got := proxyResponse.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") || !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("proxy sandbox policy = %q", got)
	}
	if proxyResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("proxy nosniff header = %q", proxyResponse.Header().Get("X-Content-Type-Options"))
	}
	wrongOrigin := httptest.NewRequest(http.MethodPost, "/api/network", strings.NewReader(`{"connected":false}`))
	wrongOrigin.Host = "127.0.0.1:8787"
	wrongOrigin.Header.Set("Content-Type", "application/json")
	wrongOrigin.Header.Set("Origin", "http://attacker.example")
	wrongOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongOriginResponse, wrongOrigin)
	if wrongOriginResponse.Code != http.StatusForbidden || !app.State().Network.Connected {
		t.Fatalf("wrong-origin mutation status=%d connected=%t", wrongOriginResponse.Code, app.State().Network.Connected)
	}

	var demoFolder Folder
	for _, folder := range state.Folders {
		if folder.ID == "demo-shared-assets" {
			demoFolder = folder
		}
	}
	if demoFolder.ID == "" || demoFolder.Status != "synced" {
		t.Fatalf("demo folder was not initially synced: %+v", demoFolder)
	}
	checkDemoFolderBytes(t, dataDir, demoFolder.Path, demoFolder.ID)
	changeResponse := serveTestRequest(handler, http.MethodPost, "/api/demo/change", "application/json", `{}`)
	if changeResponse.Code != http.StatusOK {
		t.Fatalf("demo change status = %d: %s", changeResponse.Code, changeResponse.Body.String())
	}
	checkDemoFolderBytes(t, dataDir, demoFolder.Path, demoFolder.ID)

	paused := serveTestRequest(handler, http.MethodPost, "/api/folders/"+demoFolder.ID+"/pause", "application/json", `{"paused":true}`)
	if paused.Code != http.StatusOK {
		t.Fatalf("pause folder status = %d: %s", paused.Code, paused.Body.String())
	}
	syncResponse := serveTestRequest(handler, http.MethodPost, "/api/folders/"+demoFolder.ID+"/sync", "application/json", `{}`)
	if syncResponse.Code != http.StatusConflict {
		t.Fatalf("sync while paused status = %d, want 409: %s", syncResponse.Code, syncResponse.Body.String())
	}
	unpaused := serveTestRequest(handler, http.MethodPost, "/api/folders/"+demoFolder.ID+"/pause", "application/json", `{"paused":false}`)
	if unpaused.Code != http.StatusOK {
		t.Fatalf("unpause folder status = %d: %s", unpaused.Code, unpaused.Body.String())
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, folder := range app.State().Folders {
			if folder.ID == demoFolder.ID {
				return !folder.Paused && !strings.Contains(folder.Status, "error")
			}
		}
		return false
	})
	networkPaused := serveTestRequest(handler, http.MethodPost, "/api/network", "application/json", `{"connected":false}`)
	if networkPaused.Code != http.StatusOK || app.State().Network.Connected {
		t.Fatalf("network pause status=%d connected=%t: %s", networkPaused.Code, app.State().Network.Connected, networkPaused.Body.String())
	}
	pausedProxy := serveTestRequest(handler, http.MethodGet, proxyPath, "", "")
	if pausedProxy.Code != http.StatusServiceUnavailable {
		t.Fatalf("proxy while network paused status=%d: %s", pausedProxy.Code, pausedProxy.Body.String())
	}
	networkResumed := serveTestRequest(handler, http.MethodPost, "/api/network", "application/json", `{"connected":true}`)
	if networkResumed.Code != http.StatusOK || !app.State().Network.Connected {
		t.Fatalf("network resume status=%d connected=%t: %s", networkResumed.Code, app.State().Network.Connected, networkResumed.Body.String())
	}

	linuxPeerID, err := peer.Decode(linux.ID)
	if err != nil {
		t.Fatalf("decode Linux peer ID: %v", err)
	}
	if err := app.main.Host.Network().ClosePeer(linuxPeerID); err != nil {
		t.Fatalf("close Linux peer connection: %v", err)
	}
	if len(app.main.Host.Network().ConnsToPeer(linuxPeerID)) != 0 {
		t.Fatal("ClosePeer left an active Linux connection")
	}
	app.refreshPeers()
	waitFor(t, 20*time.Second, func() bool {
		for _, device := range app.State().Devices {
			if device.ID == linux.ID {
				return device.Online && device.Connection == "relay"
			}
		}
		return false
	})
}

func serveTestRequest(handler http.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "127.0.0.1:8787"
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
		request.Header.Set("Origin", "http://127.0.0.1:3000")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func checkDemoFolderBytes(t *testing.T, dataDir, source, folderID string) {
	t.Helper()
	destination := filepath.Join(dataDir, "nodes", "linux", "received", folderID)
	err := filepath.WalkDir(source, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, sourcePath)
		if err != nil {
			return err
		}
		sourceBytes, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		destinationBytes, err := os.ReadFile(filepath.Join(destination, relative))
		if err != nil {
			return err
		}
		if !bytes.Equal(sourceBytes, destinationBytes) {
			return fmt.Errorf("received file %s does not match source bytes", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("compare demo folder bytes: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

func deviceIDs(devices []Device) []string {
	ids := make([]string, len(devices))
	for i, device := range devices {
		ids[i] = device.ID
	}
	return ids
}
