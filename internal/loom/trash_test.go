package loom

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrashManager(t *testing.T) {
	dir := t.TempDir()
	trashBase := filepath.Join(dir, "trash")
	tm := NewTrashManager(trashBase, 30*24*time.Hour)

	folderID := "test-folder"
	folderDir := filepath.Join(dir, "folder")
	if err := os.MkdirAll(folderDir, 0700); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(folderDir, "important.doc")
	if err := os.WriteFile(testFile, []byte("vital document content"), 0600); err != nil {
		t.Fatal(err)
	}

	// 1. Move to trash
	entry, err := tm.MoveToTrash(folderID, "important.doc", testFile)
	if err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}

	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Fatalf("original file still exists after move to trash")
	}

	if _, err := os.Stat(entry.TrashPath); err != nil {
		t.Fatalf("trash file does not exist at %s: %v", entry.TrashPath, err)
	}

	// 2. List trash
	list, err := tm.ListTrash(folderID)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 trash entry, got %d", len(list))
	}
	if list[0].OriginalPath != "important.doc" {
		t.Fatalf("expected original path 'important.doc', got %s", list[0].OriginalPath)
	}
}
