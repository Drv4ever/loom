package loom

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTrashRetention is 30 days.
const DefaultTrashRetention = 30 * 24 * time.Hour

// TrashEntry records information about an archived deleted file.
type TrashEntry struct {
	OriginalPath string    `json:"originalPath"`
	TrashPath    string    `json:"trashPath"`
	Size         int64     `json:"size"`
	DeletedAt    time.Time `json:"deletedAt"`
}

// TrashManager manages soft-deleted safety archives.
type TrashManager struct {
	baseDir   string
	retention time.Duration
}

// NewTrashManager creates a trash manager rooted at baseDir.
func NewTrashManager(baseDir string, retention time.Duration) *TrashManager {
	if retention <= 0 {
		retention = DefaultTrashRetention
	}
	return &TrashManager{
		baseDir:   baseDir,
		retention: retention,
	}
}

// MoveToTrash moves a file into the safety archive.
func (tm *TrashManager) MoveToTrash(folderID, relPath, sourcePath string) (*TrashEntry, error) {
	stat, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("stat file to trash: %w", err)
	}

	folderTrash := filepath.Join(tm.baseDir, folderID)
	timestamp := time.Now().UTC().Format("20060102-150405")
	trashRel := fmt.Sprintf("%s.%s.deleted", relPath, timestamp)
	trashDest := filepath.Join(folderTrash, filepath.FromSlash(trashRel))

	if err := os.MkdirAll(filepath.Dir(trashDest), 0700); err != nil {
		return nil, fmt.Errorf("create trash directory: %w", err)
	}

	// Try atomic rename first
	err = os.Rename(sourcePath, trashDest)
	if err != nil {
		// Fallback to copy and remove across filesystem boundaries
		if err := copyFile(sourcePath, trashDest); err != nil {
			return nil, fmt.Errorf("copy to trash: %w", err)
		}
		_ = os.Remove(sourcePath)
	}

	return &TrashEntry{
		OriginalPath: filepath.ToSlash(relPath),
		TrashPath:    trashDest,
		Size:         stat.Size(),
		DeletedAt:    time.Now().UTC(),
	}, nil
}

// ListTrash returns all archived files for a folder.
func (tm *TrashManager) ListTrash(folderID string) ([]TrashEntry, error) {
	folderTrash := filepath.Join(tm.baseDir, folderID)
	if _, err := os.Stat(folderTrash); os.IsNotExist(err) {
		return nil, nil
	}

	var entries []TrashEntry
	err := filepath.Walk(folderTrash, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(folderTrash, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		orig := rel
		if idx := strings.LastIndex(rel, "."); idx > 0 && strings.HasSuffix(rel, ".deleted") {
			beforeDeleted := rel[:idx]
			if secondDot := strings.LastIndex(beforeDeleted, "."); secondDot > 0 {
				orig = beforeDeleted[:secondDot]
			}
		}

		entries = append(entries, TrashEntry{
			OriginalPath: orig,
			TrashPath:    path,
			Size:         info.Size(),
			DeletedAt:    info.ModTime().UTC(),
		})
		return nil
	})

	return entries, err
}

// PurgeExpired removes archived files older than the retention window.
func (tm *TrashManager) PurgeExpired(folderID string) (int, error) {
	folderTrash := filepath.Join(tm.baseDir, folderID)
	if _, err := os.Stat(folderTrash); os.IsNotExist(err) {
		return 0, nil
	}

	cutoff := time.Now().Add(-tm.retention)
	purged := 0

	err := filepath.Walk(folderTrash, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if removeErr := os.Remove(path); removeErr == nil {
				purged++
			}
		}
		return nil
	})

	return purged, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
