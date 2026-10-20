package loom

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConflictPathGeneration(t *testing.T) {
	ts := time.Date(2026, 10, 19, 15, 30, 0, 0, time.UTC)
	path := GenerateConflictPath("nested/document.txt", ts, "MacBook Pro")
	expected := "nested/document.sync-conflict-20261019-153000-MacBook-Pro.txt"
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestConflictStoreResolutions(t *testing.T) {
	dir := t.TempDir()
	origPath := filepath.Join(dir, "doc.txt")
	conflictPath := filepath.Join(dir, "doc.sync-conflict-20261019-153000-peer.txt")

	if err := os.WriteFile(origPath, []byte("local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictPath, []byte("remote edit"), 0600); err != nil {
		t.Fatal(err)
	}

	store := NewConflictStore()
	c := Conflict{
		ID:           "conf-1",
		FolderID:     "folder-a",
		Path:         "doc.txt",
		ConflictPath: "doc.sync-conflict-20261019-153000-peer.txt",
	}
	store.Add(c)

	// Test Resolve Keep Remote
	if err := store.Resolve(dir, "folder-a", "conf-1", ResolutionKeepRemote); err != nil {
		t.Fatalf("Resolve remote failed: %v", err)
	}

	data, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "remote edit" {
		t.Fatalf("expected 'remote edit', got %q", string(data))
	}
	if _, err := os.Stat(conflictPath); !os.IsNotExist(err) {
		t.Fatalf("conflict copy was not removed")
	}

	if list := store.List("folder-a"); len(list) != 0 {
		t.Fatalf("expected 0 conflicts, got %d", len(list))
	}
}
