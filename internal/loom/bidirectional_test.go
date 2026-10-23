package loom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestBidirectionalSyncTwoWay(t *testing.T) {
	key := testNetworkKey(10)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA and nodeB: %v", err)
	}

	folderID := "bidir-folder"
	folderADir := filepath.Join(t.TempDir(), "folderA")
	folderBDir := filepath.Join(t.TempDir(), "folderB")
	_ = os.MkdirAll(folderADir, 0700)
	_ = os.MkdirAll(folderBDir, 0700)

	// Create a file in folder A
	if err := os.WriteFile(filepath.Join(folderADir, "fileA.txt"), []byte("hello from A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Create a file in folder B
	if err := os.WriteFile(filepath.Join(folderBDir, "fileB.txt"), []byte("hello from B\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Register custom path on node B for folderID
	nodeB.getCustomPath = func(id string) string {
		if id == folderID {
			return folderBDir
		}
		return ""
	}

	appA := &App{
		main: nodeA, connected: true, dataDir: nodeADir,
		statePath: filepath.Join(nodeADir, "state.json"), ctx: ctx,
		folders: map[string]*folderEntry{
			folderID: {
				Folder: Folder{
					ID:       folderID,
					Name:     "Bidir Test",
					Path:     folderADir,
					PeerID:   nodeB.Host.ID().String(),
					PeerName: "nodeB",
					Status:   "pending",
				},
				manifest: make(map[string]string),
			},
		},
	}

	// Run sync
	if err := appA.syncFolder(ctx, folderID); err != nil {
		t.Fatalf("syncFolder failed: %v", err)
	}

	// Verify fileA was transferred to B
	dataB, err := os.ReadFile(filepath.Join(folderBDir, "fileA.txt"))
	if err != nil || string(dataB) != "hello from A\n" {
		t.Fatalf("fileA was not transferred to folder B: data=%q err=%v", string(dataB), err)
	}

	// Verify fileB was transferred to A (two-way!)
	dataA, err := os.ReadFile(filepath.Join(folderADir, "fileB.txt"))
	if err != nil || string(dataA) != "hello from B\n" {
		t.Fatalf("fileB was not transferred back to folder A: data=%q err=%v", string(dataA), err)
	}
}

func TestConcurrentEditsResolveToConflictCopy(t *testing.T) {
	key := testNetworkKey(11)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA and nodeB: %v", err)
	}

	folderID := "conflict-folder"
	folderADir := filepath.Join(t.TempDir(), "folderA")
	folderBDir := filepath.Join(t.TempDir(), "folderB")
	_ = os.MkdirAll(folderADir, 0700)
	_ = os.MkdirAll(folderBDir, 0700)

	nodeB.getCustomPath = func(id string) string {
		if id == folderID {
			return folderBDir
		}
		return ""
	}

	// Initial common file
	commonFile := "report.txt"
	initialContent := []byte("Initial baseline content\n")
	_ = os.WriteFile(filepath.Join(folderADir, commonFile), initialContent, 0600)
	_ = os.WriteFile(filepath.Join(folderBDir, commonFile), initialContent, 0600)

	initialHash := sha256Hex(initialContent)
	appA := &App{
		main: nodeA, connected: true, dataDir: nodeADir,
		statePath: filepath.Join(nodeADir, "state.json"), ctx: ctx,
		folders: map[string]*folderEntry{
			folderID: {
				Folder: Folder{
					ID:       folderID,
					Name:     "Conflict Test",
					Path:     folderADir,
					PeerID:   nodeB.Host.ID().String(),
					PeerName: "nodeB",
					Status:   "pending",
				},
				manifest: map[string]string{commonFile: initialHash},
			},
		},
	}

	// Concurrent edits on both offline devices
	aEdit := []byte("Device A modified this document\n")
	bEdit := []byte("Device B modified this document concurrently\n")
	_ = os.WriteFile(filepath.Join(folderADir, commonFile), aEdit, 0600)
	_ = os.WriteFile(filepath.Join(folderBDir, commonFile), bEdit, 0600)

	// Reconnect / trigger sync
	if err := appA.syncFolder(ctx, folderID); err != nil {
		t.Fatalf("syncFolder failed on conflict: %v", err)
	}

	// Verify local original file on A is preserved with A's edit
	gotA, _ := os.ReadFile(filepath.Join(folderADir, commonFile))
	if !bytes.Equal(gotA, aEdit) {
		t.Fatalf("Device A edit was overwritten! got=%q", string(gotA))
	}

	// Verify conflict copy was created on A for B's edit
	conflicts := nodeA.conflicts.List(folderID)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}

	conflictCopyPath := filepath.Join(folderADir, filepath.FromSlash(conflicts[0].ConflictPath))
	conflictContent, err := os.ReadFile(conflictCopyPath)
	if err != nil {
		t.Fatalf("failed to read conflict copy at %s: %v", conflictCopyPath, err)
	}
	if !bytes.Equal(conflictContent, bEdit) {
		t.Fatalf("conflict copy content mismatch: got %q, want %q", string(conflictContent), string(bEdit))
	}

	// Test API Resolution: Keep Remote
	handler := appA.Handler("127.0.0.1:8787")
	resolveBody := `{"conflictId":"` + conflicts[0].ID + `","resolution":"remote"}`
	rec := serveTestRequest(handler, http.MethodPost, "/api/folders/"+folderID+"/resolve", "application/json", resolveBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve failed code=%d body=%s", rec.Code, rec.Body.String())
	}

	// After resolving as remote, original file should have B's content
	resolvedContent, _ := os.ReadFile(filepath.Join(folderADir, commonFile))
	if !bytes.Equal(resolvedContent, bEdit) {
		t.Fatalf("after remote resolution, content got=%q, want=%q", string(resolvedContent), string(bEdit))
	}
	if _, err := os.Stat(conflictCopyPath); !os.IsNotExist(err) {
		t.Fatalf("conflict copy file was not removed after resolution")
	}
}

func TestDeletionPropagationMovesToTrashArchive(t *testing.T) {
	key := testNetworkKey(12)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA and nodeB: %v", err)
	}

	folderID := "trash-test-folder"
	folderADir := filepath.Join(t.TempDir(), "folderA")
	folderBDir := filepath.Join(t.TempDir(), "folderB")
	_ = os.MkdirAll(folderADir, 0700)
	_ = os.MkdirAll(folderBDir, 0700)

	nodeB.getCustomPath = func(id string) string {
		if id == folderID {
			return folderBDir
		}
		return ""
	}

	// Create file on both sides
	testFile := "delete_me.txt"
	testContent := []byte("content to be deleted\n")
	_ = os.WriteFile(filepath.Join(folderADir, testFile), testContent, 0600)
	_ = os.WriteFile(filepath.Join(folderBDir, testFile), testContent, 0600)

	fileHash := sha256Hex(testContent)
	appA := &App{
		main: nodeA, connected: true, dataDir: nodeADir,
		statePath: filepath.Join(nodeADir, "state.json"), ctx: ctx,
		folders: map[string]*folderEntry{
			folderID: {
				Folder: Folder{
					ID:       folderID,
					Name:     "Trash Test",
					Path:     folderADir,
					PeerID:   nodeB.Host.ID().String(),
					PeerName: "nodeB",
					Status:   "synced",
				},
				manifest: map[string]string{testFile: fileHash},
			},
		},
	}

	// Delete file on Device A
	if err := os.Remove(filepath.Join(folderADir, testFile)); err != nil {
		t.Fatal(err)
	}

	// Trigger sync
	if err := appA.syncFolder(ctx, folderID); err != nil {
		t.Fatalf("syncFolder failed: %v", err)
	}

	// File on Device B should be removed from active folder
	if _, err := os.Stat(filepath.Join(folderBDir, testFile)); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists in Device B active folder")
	}

	// File on Device B should be preserved in Device B's safety archive (trash)!
	trashList, err := nodeB.trash.ListTrash(folderID)
	if err != nil {
		t.Fatalf("nodeB.trash.ListTrash: %v", err)
	}
	if len(trashList) != 1 {
		t.Fatalf("expected 1 item in nodeB trash archive, got %d", len(trashList))
	}
	if trashList[0].OriginalPath != testFile {
		t.Fatalf("expected trash originalPath=%q, got %q", testFile, trashList[0].OriginalPath)
	}
	archivedData, err := os.ReadFile(trashList[0].TrashPath)
	if err != nil || !bytes.Equal(archivedData, testContent) {
		t.Fatalf("archived file content mismatch: data=%q err=%v", string(archivedData), err)
	}
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestDeltaChunkSyncOnlyTransfersModifiedChunk(t *testing.T) {
	key := testNetworkKey(13)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA and nodeB: %v", err)
	}

	folderID := "delta-folder"
	folderADir := filepath.Join(t.TempDir(), "folderA")
	folderBDir := filepath.Join(t.TempDir(), "folderB")
	_ = os.MkdirAll(folderADir, 0700)
	_ = os.MkdirAll(folderBDir, 0700)

	nodeB.getCustomPath = func(id string) string {
		if id == folderID {
			return folderBDir
		}
		return ""
	}

	// Create a 2.5 MiB file (3 chunks: 1MB, 1MB, 0.5MB)
	fileName := "largefile.dat"
	chunkSize := int64(1 << 20)
	fileSize := int64(2.5 * float64(chunkSize))
	initialContent := make([]byte, fileSize)
	for i := range initialContent {
		initialContent[i] = byte(i % 251)
	}

	_ = os.WriteFile(filepath.Join(folderADir, fileName), initialContent, 0600)
	_ = os.WriteFile(filepath.Join(folderBDir, fileName), initialContent, 0600)
	initialHash := sha256Hex(initialContent)

	appA := &App{
		main: nodeA, connected: true, dataDir: nodeADir,
		statePath: filepath.Join(nodeADir, "state.json"), ctx: ctx,
		folders: map[string]*folderEntry{
			folderID: {
				Folder: Folder{
					ID:       folderID,
					Name:     "Delta Test",
					Path:     folderADir,
					PeerID:   nodeB.Host.ID().String(),
					PeerName: "nodeB",
					Status:   "synced",
				},
				manifest: map[string]string{fileName: initialHash},
			},
		},
	}

	// Modify 10 bytes in chunk 1 (offset 1.5 MiB) on Device A
	editOffset := int64(1.5 * float64(chunkSize))
	for i := int64(0); i < 10; i++ {
		initialContent[editOffset+i] = 0xAA
	}
	_ = os.WriteFile(filepath.Join(folderADir, fileName), initialContent, 0600)

	// Measure transferred bytes
	beforeBytes := appA.transferredBytes
	if err := appA.syncFolder(ctx, folderID); err != nil {
		t.Fatalf("syncFolder failed on delta sync: %v", err)
	}
	transferredNow := appA.transferredBytes - beforeBytes

	// Only 1 chunk (1 MiB = 1048576 bytes) should have been transferred, NOT 2.5 MiB (2621440 bytes)!
	if transferredNow > int64(1.2*float64(chunkSize)) {
		t.Fatalf("delta sync transferred %d bytes, expected ~1 MiB (one chunk)", transferredNow)
	}

	// Verify Device B now has the exact updated 2.5 MiB file with the edit
	updatedOnB, err := os.ReadFile(filepath.Join(folderBDir, fileName))
	if err != nil {
		t.Fatalf("read updated file on B: %v", err)
	}
	if !bytes.Equal(updatedOnB, initialContent) {
		t.Fatalf("content on Device B does not match modified file")
	}
}
