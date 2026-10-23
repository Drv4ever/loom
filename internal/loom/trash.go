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
	ID           string    `json:"id"`
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
	if !isID(folderID) || !validRelativePath(relPath) {
		return nil, fmt.Errorf("invalid archive path")
	}
	rootPath := sourcePath
	for range strings.Split(relPath, "/") {
		rootPath = filepath.Dir(rootPath)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	hash, err := syncFileHash(root, relPath)
	if err != nil {
		return nil, err
	}
	if err := tm.archiveFromRoot(folderID, relPath, root, fileDescriptor{SHA256: hash}); err != nil {
		return nil, err
	}
	entries, err := tm.ListTrash(folderID)
	if err != nil {
		return nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].OriginalPath == relPath {
			return &entries[i], nil
		}
	}
	return nil, fmt.Errorf("archive receipt missing")
}

// ListTrash returns all archived files for a folder.
func (tm *TrashManager) ListTrash(folderID string) ([]TrashEntry, error) {
	if !isID(folderID) {
		return nil, fmt.Errorf("invalid folder id")
	}
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
		if !info.Mode().IsRegular() || !validRelativePath(rel) {
			return nil
		}
		orig := rel
		deletedAt := info.ModTime().UTC()
		if strings.HasSuffix(rel, ".deleted") {
			before := strings.TrimSuffix(rel, ".deleted")
			// New archives use nanosecond timestamps; retain older second-resolution entries.
			for _, layout := range []string{"20060102-150405.000000000", "20060102-150405"} {
				length := len(time.Now().Format(layout))
				if len(before) > length && before[len(before)-length-1] == '.' {
					if stamp, err := time.Parse(layout, before[len(before)-length:]); err == nil {
						orig = before[:len(before)-length-1]
						deletedAt = stamp
						break
					}
				}
			}
		}
		if !validRelativePath(orig) {
			return nil
		}

		entries = append(entries, TrashEntry{
			ID:           rel,
			OriginalPath: orig,
			TrashPath:    path,
			Size:         info.Size(),
			DeletedAt:    deletedAt,
		})
		return nil
	})

	return entries, err
}

// PurgeExpired removes archived files older than the retention window.
func (tm *TrashManager) PurgeExpired(folderID string) (int, error) {
	if !isID(folderID) {
		return 0, fmt.Errorf("invalid folder id")
	}
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

// archiveFromRoot preserves a verified version before deleting the replica.
func (tm *TrashManager) archiveFromRoot(folderID, rel string, source *os.Root, expected fileDescriptor) error {
	if !isID(folderID) || !validRelativePath(rel) {
		return fmt.Errorf("invalid archive path")
	}
	if err := checkSyncDestination(source, rel, &expected); err != nil {
		return err
	}
	if err := os.MkdirAll(tm.baseDir, 0700); err != nil {
		return err
	}
	base, err := os.OpenRoot(tm.baseDir)
	if err != nil {
		return err
	}
	defer base.Close()
	if err := ensureSafeParent(base, folderID); err != nil {
		return err
	}
	archive, err := base.OpenRoot(folderID)
	if err != nil {
		return err
	}
	defer archive.Close()
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	name := rel + "." + stamp + ".deleted"
	input, err := openSyncFile(source, rel)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := copyIntoRoot(archive, name, input, maxFileSize); err != nil {
		return err
	}
	hash, err := syncFileHash(archive, name)
	if err != nil || hash != expected.SHA256 {
		_ = archive.Remove(name)
		return fmt.Errorf("file changed while archiving")
	}
	if err := checkSyncDestination(source, rel, &expected); err != nil {
		return err
	}
	return source.Remove(rel)
}

// Restore takes an archive-relative ID, never a client-supplied absolute path.
func (tm *TrashManager) Restore(folderID, archiveID string, destination *os.Root) (string, error) {
	if !isID(folderID) || !validRelativePath(archiveID) {
		return "", fmt.Errorf("invalid archive id")
	}
	entries, err := tm.ListTrash(folderID)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.ID != archiveID {
			continue
		}
		if err := checkSyncDestination(destination, entry.OriginalPath, nil); err != nil {
			return "", err
		}
		archive, err := os.OpenRoot(filepath.Join(tm.baseDir, folderID))
		if err != nil {
			return "", err
		}
		defer archive.Close()
		input, err := openSyncFile(archive, archiveID)
		if err != nil {
			return "", err
		}
		defer input.Close()
		if err := copyIntoRoot(destination, entry.OriginalPath, input, maxFileSize); err != nil {
			return "", err
		}
		if err := archive.Remove(archiveID); err != nil {
			return "", err
		}
		return entry.OriginalPath, nil
	}
	return "", os.ErrNotExist
}
