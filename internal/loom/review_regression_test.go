package loom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func syncReviewPair(t *testing.T) (*App, *Node, string) {
	t.Helper()
	key := testNetworkKey(60)
	a := newTestNode(t, "A", t.TempDir(), key)
	b := newTestNode(t, "B", t.TempDir(), key)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := a.Host.Connect(ctx, peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	state := t.TempDir()
	id := "review-folder"
	app := &App{main: a, connected: true, ctx: ctx, statePath: filepath.Join(state, "state.json"), folders: map[string]*folderEntry{id: {Folder: Folder{ID: id, Name: "Review", Path: dir, PeerID: b.Host.ID().String(), PeerName: "B", Mode: modeTwoWay}, manifest: make(map[string]string)}}}
	return app, b, id
}
func reviewWrite(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func reviewRead(t *testing.T, dir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != want {
		t.Fatalf("%s: got %q, err %v; want %q", name, got, err, want)
	}
}
func reviewSync(t *testing.T, a *App, id string) {
	t.Helper()
	if err := a.syncFolder(a.ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestConflictSurvivesRepeatedSyncRestartAndChosenResolution(t *testing.T) {
	for _, resolution := range []string{"local", "remote", "both"} {
		t.Run(resolution, func(t *testing.T) {
			a, b, id := syncReviewPair(t)
			dir := a.folders[id].Folder.Path
			remote := filepath.Join(b.ReceiveDir, id)
			reviewWrite(t, dir, "doc.txt", "base")
			reviewSync(t, a, id)
			reviewWrite(t, dir, "doc.txt", "local edit")
			reviewWrite(t, remote, "doc.txt", "remote edit")
			for range 3 {
				reviewSync(t, a, id)
				reviewRead(t, dir, "doc.txt", "local edit")
				reviewRead(t, remote, "doc.txt", "remote edit")
			}
			if a.folders[id].manifest["doc.txt"] != sha256Hex([]byte("base")) {
				t.Fatal("conflict advanced the shared baseline")
			}
			// An upgrade may load the old implementation's incorrectly advanced baseline.
			a.folders[id].manifest["doc.txt"] = sha256Hex([]byte("local edit"))
			reviewSync(t, a, id)
			reviewRead(t, dir, "doc.txt", "local edit")
			conflicts := a.main.conflicts.List(id)
			if len(conflicts) != 1 {
				t.Fatalf("duplicate conflicts: %d", len(conflicts))
			}
			reviewRead(t, dir, conflicts[0].ConflictPath, "remote edit")
			// Recreate the app's persisted baseline and conflict store as on restart.
			state, err := os.ReadFile(a.statePath)
			if err != nil {
				t.Fatal(err)
			}
			var saved persistedState
			if err := json.Unmarshal(state, &saved); err != nil {
				t.Fatal(err)
			}
			a.folders[id].manifest = saved.Folders[0].Manifest
			a.main.conflicts = NewConflictStore()
			for _, c := range saved.Folders[0].Conflicts {
				a.main.conflicts.Add(c)
			}
			reviewSync(t, a, id)
			reviewRead(t, dir, "doc.txt", "local edit")
			body := fmt.Sprintf(`{"conflictId":%q,"resolution":%q}`, conflicts[0].ID, resolution)
			resp := serveTestRequest(a.Handler("127.0.0.1:8787"), http.MethodPost, "/api/folders/"+id+"/resolve", "application/json", body)
			if resp.Code != 200 {
				t.Fatalf("resolve: %d %s", resp.Code, resp.Body.String())
			}
			for range 3 {
				reviewSync(t, a, id)
			}
			want := "local edit"
			if resolution == "remote" {
				want = "remote edit"
			}
			reviewRead(t, dir, "doc.txt", want)
			reviewRead(t, remote, "doc.txt", want)
			if len(a.main.conflicts.List(id)) != 0 {
				t.Fatal("resolution created another conflict")
			}
			if resolution == "both" {
				reviewRead(t, remote, conflicts[0].ConflictPath, "remote edit")
			}
		})
	}
}

func TestReceiverDeletionPersistsAcrossRestartAndRestore(t *testing.T) {
	a, b, id := syncReviewPair(t)
	dir := a.folders[id].Folder.Path
	remote := filepath.Join(b.ReceiveDir, id)
	reviewWrite(t, dir, "delete.txt", "preserve me")
	reviewSync(t, a, id)
	if err := os.Remove(filepath.Join(remote, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	reviewSync(t, a, id)
	for range 2 {
		reviewSync(t, a, id)
	}
	if _, err := os.Stat(filepath.Join(dir, "delete.txt")); !os.IsNotExist(err) {
		t.Fatal("receiver deletion was resurrected")
	}
	// A new Node loads the receiver catalog and durable tombstones from disk.
	bDir := filepath.Dir(b.receivedPath)
	_ = b.Close()
	restarted := newTestNode(t, "B", bDir, testNetworkKey(60))
	if err := a.main.Host.Connect(a.ctx, peer.AddrInfo{ID: restarted.Host.ID(), Addrs: restarted.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reviewSync(t, a, id)
	}
	if _, err := os.Stat(filepath.Join(remote, "delete.txt")); !os.IsNotExist(err) {
		t.Fatal("restart resurrected deletion")
	}
	entries, err := a.main.trash.ListTrash(id)
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive: %v %v", entries, err)
	}
	body := fmt.Sprintf(`{"id":%q}`, entries[0].ID)
	resp := serveTestRequest(a.Handler("127.0.0.1:8787"), http.MethodPost, "/api/folders/"+id+"/restore", "application/json", body)
	if resp.Code != 200 {
		t.Fatalf("restore: %d %s", resp.Code, resp.Body.String())
	}
	reviewSync(t, a, id)
	reviewRead(t, dir, "delete.txt", "preserve me")
	reviewRead(t, remote, "delete.txt", "preserve me")
}

func TestSendOnlyNeverFetchesOrDeletesSource(t *testing.T) {
	a, b, id := syncReviewPair(t)
	a.folders[id].Folder.Mode = modeSendOnly
	dir := a.folders[id].Folder.Path
	remote := filepath.Join(b.ReceiveDir, id)
	reviewWrite(t, dir, "doc.txt", "source")
	reviewSync(t, a, id)
	reviewWrite(t, remote, "doc.txt", "receiver edit")
	reviewWrite(t, remote, "extra.txt", "private receiver file")
	for range 2 {
		reviewSync(t, a, id)
	}
	reviewRead(t, dir, "doc.txt", "source")
	reviewRead(t, remote, "doc.txt", "source")
	if _, err := os.Stat(filepath.Join(dir, "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("send-only fetched a receiver file")
	}
	if err := os.Remove(filepath.Join(remote, "doc.txt")); err != nil {
		t.Fatal(err)
	}
	reviewSync(t, a, id)
	reviewRead(t, dir, "doc.txt", "source")
	reviewRead(t, remote, "doc.txt", "source")
}

func TestIncomingConsentDestinationPauseDeclineAndSenderOwnership(t *testing.T) {
	a, b, id := syncReviewPair(t)
	b.requireAcceptance = true
	dir := a.folders[id].Folder.Path
	reviewWrite(t, dir, "doc.txt", "source")
	if err := a.syncFolder(a.ctx, id); err == nil {
		t.Fatal("unaccepted folder synced")
	}
	if _, err := os.Stat(filepath.Join(b.ReceiveDir, id)); !os.IsNotExist(err) {
		t.Fatal("offer wrote files before consent")
	}
	receiver := &App{main: b, ctx: a.ctx, statePath: filepath.Join(t.TempDir(), "state.json"), folders: make(map[string]*folderEntry)}
	handler := receiver.Handler("127.0.0.1:8787")
	destination := t.TempDir()
	for _, action := range []string{"accept", "pause", "resume", "remove"} {
		body := fmt.Sprintf(`{"action":%q,"path":%q}`, action, destination)
		resp := serveTestRequest(handler, http.MethodPost, "/api/received/"+id, "application/json", body)
		if resp.Code != 200 {
			t.Fatalf("%s: %d %s", action, resp.Code, resp.Body.String())
		}
		err := a.syncFolder(a.ctx, id)
		if action == "accept" || action == "resume" {
			if err != nil {
				t.Fatal(err)
			}
			reviewRead(t, destination, "doc.txt", "source")
		} else if err == nil {
			t.Fatalf("%s did not prevent sync", action)
		}
	}
	b.mu.Lock()
	folder := b.received[id]
	folder.Status = "accepted"
	folder.PeerID = a.main.Host.ID().String()
	b.received[id] = folder
	_ = b.saveReceivedLocked()
	b.mu.Unlock()
	b.received = make(map[string]ReceivedFolder)
	if err := b.loadReceived(); err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(destination)
	if b.received[id].Path != canonical {
		t.Fatal("custom destination lost on reload")
	}
	intruder := newTestNode(t, "intruder", t.TempDir(), testNetworkKey(60))
	if err := intruder.Host.Connect(a.ctx, peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	stream, err := intruder.Host.NewStream(a.ctx, b.Host.ID(), syncProtocol)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = writeFrame(stream, mustJSON(SyncHello{FolderID: id, Mode: modeTwoWay}))
	var reply SyncHelloReply
	if err := readFrame(stream, &reply, 1<<20); err != nil {
		t.Fatal(err)
	}
	if reply.OK || !strings.Contains(reply.Error, "different sender") {
		t.Fatalf("sender ownership bypass: %+v", reply)
	}
}

func TestSyncRejectsTraversalSymlinksAndUnrequestedDownloads(t *testing.T) {
	a, b, id := syncReviewPair(t)
	for _, hello := range []SyncHello{
		{FolderID: "../outside", Mode: modeTwoWay},
		{FolderID: id, Mode: modeTwoWay, Manifest: map[string]fileDescriptor{"../outside": {Path: "../outside", SHA256: sha256Hex(nil)}}},
		{FolderID: id, Mode: modeTwoWay, Manifest: map[string]fileDescriptor{"file.txt": {Path: "file.txt", Size: -1, SHA256: sha256Hex(nil)}}},
	} {
		stream, err := a.main.Host.NewStream(a.ctx, b.Host.ID(), syncProtocol)
		if err != nil {
			t.Fatal(err)
		}
		_ = writeFrame(stream, mustJSON(hello))
		var reply SyncHelloReply
		err = readFrame(stream, &reply, 1<<20)
		_ = stream.Close()
		if err != nil || reply.OK {
			t.Fatalf("unsafe hello accepted: %+v %v", reply, err)
		}
	}
	dir := a.folders[id].Folder.Path
	reviewWrite(t, dir, "safe.txt", "initial")
	reviewSync(t, a, id)
	outside := t.TempDir()
	reviewWrite(t, outside, "secret.txt", "private")
	remote := filepath.Join(b.ReceiveDir, id)
	if err := os.Symlink(outside, filepath.Join(remote, "nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	reviewWrite(t, filepath.Join(dir, "nested"), "secret.txt", "overwrite")
	if err := a.syncFolder(a.ctx, id); err == nil {
		t.Fatal("sync followed a destination symlink")
	}
	reviewRead(t, outside, "secret.txt", "private")
	// The responder only serves files the reconciliation plan permits fetching.
	stream, err := a.main.Host.NewStream(a.ctx, b.Host.ID(), syncProtocol)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(2 * time.Second))
	_ = writeFrame(stream, mustJSON(SyncHello{FolderID: id, Mode: modeTwoWay}))
	var reply SyncHelloReply
	if err := readFrame(stream, &reply, 1<<20); err != nil || !reply.OK {
		t.Fatalf("hello: %v %+v", err, reply)
	}
	_ = writeFrame(stream, mustJSON(TransferHeader{End: true}))
	_ = writeFrame(stream, mustJSON(map[string]any{"fetchPaths": []string{"../secret.txt"}}))
	var hdr TransferHeader
	if err := readFrame(stream, &hdr, 64<<10); err == nil {
		t.Fatalf("unrequested path served: %+v", hdr)
	}
}

func TestTCPRequiresPublicationProtocolAndPermission(t *testing.T) {
	a, b, _ := syncReviewPair(t)
	b.SetService(ServiceRecord{ID: "http-service", Name: "HTTP only", Target: "http://127.0.0.1:9"})
	other := newTestNode(t, "other", t.TempDir(), testNetworkKey(60))
	b.SetService(ServiceRecord{ID: "private-tcp", Name: "Private", Protocol: "tcp", Target: "127.0.0.1:9", AllowedPeers: []string{other.Host.ID().String()}})
	for _, handshake := range []TCPForwardHandshake{
		{Target: "127.0.0.1:9"},
		{ServiceID: "http-service"},
		{ServiceID: "private-tcp"},
		{ServiceID: "missing-service"},
	} {
		stream, err := a.main.Host.NewStream(a.ctx, b.Host.ID(), tcpForwardProtocol)
		if err != nil {
			t.Fatal(err)
		}
		_ = writeLengthPrefixedJSON(stream, handshake)
		var reply TCPForwardResponse
		err = readLengthPrefixedJSON(stream, &reply, 4<<10)
		_ = stream.Close()
		if err != nil || reply.OK || reply.Error == "" {
			t.Fatalf("TCP authorization bypass: %+v %v", reply, err)
		}
	}
	manager := NewForwardManager(a.main, nil)
	defer manager.Close()
	if _, err := manager.StartForward(b.Host.ID().String(), "", "127.0.0.1:9", 0); err == nil {
		t.Fatal("raw forward created a listener")
	}
	if len(manager.ListForwards()) != 0 {
		t.Fatal("rejected forward left a listener")
	}
}

func TestTransferRejectsCorruptContentAndMalformedChunk(t *testing.T) {
	// Exercise the receiver through a real mesh stream, including malicious chunk lengths.
	a, b, id := syncReviewPair(t)
	for _, delta := range []bool{false, true} {
		stream, err := a.main.Host.NewStream(a.ctx, b.Host.ID(), syncProtocol)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
		size := int64(4)
		if delta {
			size = DefaultChunkSize
		}
		desc := fileDescriptor{Path: "file.txt", Size: size, SHA256: sha256Hex([]byte("good"))}
		_ = writeFrame(stream, mustJSON(SyncHello{FolderID: id, Mode: modeTwoWay, Manifest: map[string]fileDescriptor{desc.Path: desc}}))
		var reply SyncHelloReply
		if err := readFrame(stream, &reply, 1<<20); err != nil || !reply.OK {
			t.Fatalf("hello failed: %v %+v", err, reply)
		}
		_ = writeFrame(stream, mustJSON(TransferHeader{Path: desc.Path, Size: desc.Size, SHA256: desc.SHA256, IsDelta: delta}))
		if delta {
			_ = writeFrame(stream, mustJSON(FileChunkManifest{Path: desc.Path, Size: size, SHA256: desc.SHA256, ChunkSize: DefaultChunkSize, Chunks: []ChunkInfo{{Index: 0, Offset: 0, Size: DefaultChunkSize, SHA256: desc.SHA256}}}))
			var needed NeededChunksReply
			if err := readFrame(stream, &needed, 64<<10); err != nil {
				t.Fatal(err)
			}
			_ = writeFrame(stream, mustJSON(ChunkFrame{Index: 0, Size: 1 << 40}))
		} else {
			_, _ = stream.Write([]byte("evil"))
		}
		var ack TransferAck
		if err := readFrame(stream, &ack, 16<<10); err != nil || ack.OK {
			t.Fatalf("corrupt transfer accepted: %+v %v", ack, err)
		}
		_ = stream.Close()
		if _, err := os.Stat(filepath.Join(b.ReceiveDir, id, "file.txt")); !os.IsNotExist(err) {
			t.Fatal("corrupt file installed")
		}
	}
}

func TestUnresolvedConflictAlsoFreezesSubsequentDeletion(t *testing.T) {
	a, b, id := syncReviewPair(t)
	dir := a.folders[id].Folder.Path
	remote := filepath.Join(b.ReceiveDir, id)
	reviewWrite(t, dir, "doc.txt", "base")
	reviewSync(t, a, id)
	reviewWrite(t, dir, "doc.txt", "local")
	reviewWrite(t, remote, "doc.txt", "remote")
	reviewSync(t, a, id)
	c := a.main.conflicts.List(id)[0]
	if err := os.Remove(filepath.Join(dir, "doc.txt")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reviewSync(t, a, id)
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.txt")); !os.IsNotExist(err) {
		t.Fatal("unresolved conflict fetched over a subsequent local deletion")
	}
	reviewRead(t, remote, "doc.txt", "remote")
	body := fmt.Sprintf(`{"conflictId":%q,"resolution":"local"}`, c.ID)
	resp := serveTestRequest(a.Handler("127.0.0.1:8787"), http.MethodPost, "/api/folders/"+id+"/resolve", "application/json", body)
	if resp.Code != 200 {
		t.Fatalf("resolve: %d %s", resp.Code, resp.Body.String())
	}
	reviewSync(t, a, id)
	if _, err := os.Stat(filepath.Join(remote, "doc.txt")); !os.IsNotExist(err) {
		t.Fatal("chosen deletion did not propagate")
	}
}

func TestPrefilledManagedDestinationAndChangingDestination(t *testing.T) {
	a, b, id := syncReviewPair(t)
	b.requireAcceptance = true
	source := a.folders[id].Folder.Path
	reviewWrite(t, source, "doc.txt", "keep source")
	if err := a.syncFolder(a.ctx, id); err == nil {
		t.Fatal("folder must require acceptance")
	}
	receiver := &App{main: b, ctx: a.ctx, statePath: filepath.Join(t.TempDir(), "state.json"), folders: make(map[string]*folderEntry)}
	handler := receiver.Handler("127.0.0.1:8787")
	resp := serveTestRequest(handler, http.MethodPost, "/api/received/"+id, "application/json", `{"action":"pause"}`)
	if resp.Code != 409 {
		t.Fatal("pending invitation can bypass consent by pause/resume")
	}
	b.mu.RLock()
	managed := b.received[id].Path
	b.mu.RUnlock()
	// The dashboard submits its prefilled path even though it has not been created yet.
	body := fmt.Sprintf(`{"action":"accept","path":%q}`, managed)
	resp = serveTestRequest(handler, http.MethodPost, "/api/received/"+id, "application/json", body)
	if resp.Code != 200 {
		t.Fatalf("prefilled destination rejected: %d %s", resp.Code, resp.Body.String())
	}
	reviewSync(t, a, id)
	reviewRead(t, managed, "doc.txt", "keep source")
	resp = serveTestRequest(handler, http.MethodPost, "/api/received/"+id, "application/json", `{"action":"remove"}`)
	if resp.Code != 200 {
		t.Fatal(resp.Body.String())
	}
	replacement := t.TempDir()
	body = fmt.Sprintf(`{"action":"accept","path":%q}`, replacement)
	resp = serveTestRequest(handler, http.MethodPost, "/api/received/"+id, "application/json", body)
	if resp.Code != 200 {
		t.Fatal(resp.Body.String())
	}
	for range 2 {
		reviewSync(t, a, id)
	}
	reviewRead(t, source, "doc.txt", "keep source")
	reviewRead(t, replacement, "doc.txt", "keep source")
	reviewRead(t, managed, "doc.txt", "keep source")
}
