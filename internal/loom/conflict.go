package loom

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Conflict describes a detected concurrent edit collision.
type Conflict struct {
	ID            string    `json:"id"`
	FolderID      string    `json:"folderId"`
	Path          string    `json:"path"`
	ConflictPath  string    `json:"conflictPath"`
	LocalModTime  time.Time `json:"localModTime"`
	RemoteModTime time.Time `json:"remoteModTime"`
	LocalSize     int64     `json:"localSize"`
	RemoteSize    int64     `json:"remoteSize"`
	LocalSHA256   string    `json:"localSha256"`
	RemoteSHA256  string    `json:"remoteSha256"`
	DeviceName    string    `json:"deviceName"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ConflictResolution represents the user's resolution decision.
type ConflictResolution string

const (
	ResolutionKeepLocal  ConflictResolution = "local"
	ResolutionKeepRemote ConflictResolution = "remote"
	ResolutionKeepBoth   ConflictResolution = "both"
)

var filenameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_\-]+`)

func sanitizeForFilename(s string) string {
	s = strings.TrimSpace(s)
	s = filenameSanitizer.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// GenerateConflictPath constructs a conflict copy relative path following:
// <filename>.sync-conflict-<timestamp>-<device>.<ext>
func GenerateConflictPath(relPath string, timestamp time.Time, deviceName string) string {
	clean := filepath.ToSlash(relPath)
	dir := filepath.Dir(clean)
	base := filepath.Base(clean)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	safeDevice := sanitizeForFilename(deviceName)
	if safeDevice == "" {
		safeDevice = "peer"
	}
	timeStr := timestamp.Format("20060102-150405")
	conflictBase := fmt.Sprintf("%s.sync-conflict-%s-%s%s", stem, timeStr, safeDevice, ext)

	if dir == "." || dir == "" {
		return conflictBase
	}
	return filepath.ToSlash(filepath.Join(dir, conflictBase))
}

// ConflictStore manages active conflicts.
type ConflictStore struct {
	mu        sync.RWMutex
	conflicts map[string]map[string]Conflict // folderID -> conflictID -> Conflict
}

// NewConflictStore creates an in-memory conflict store.
func NewConflictStore() *ConflictStore {
	return &ConflictStore{
		conflicts: make(map[string]map[string]Conflict),
	}
}

// Add adds a conflict record.
func (cs *ConflictStore) Add(c Conflict) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.conflicts[c.FolderID] == nil {
		cs.conflicts[c.FolderID] = make(map[string]Conflict)
	}
	cs.conflicts[c.FolderID][c.ID] = c
}

// List returns all active conflicts for a folder.
func (cs *ConflictStore) List(folderID string) []Conflict {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	folderMap := cs.conflicts[folderID]
	if len(folderMap) == 0 {
		return nil
	}
	list := make([]Conflict, 0, len(folderMap))
	for _, c := range folderMap {
		list = append(list, c)
	}
	return list
}

// Get finds a conflict by folder and ID.
func (cs *ConflictStore) Get(folderID, conflictID string) (Conflict, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	c, ok := cs.conflicts[folderID][conflictID]
	return c, ok
}

// Remove deletes a conflict record.
func (cs *ConflictStore) Remove(folderID, conflictID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.conflicts[folderID] != nil {
		delete(cs.conflicts[folderID], conflictID)
	}
}

// Resolve applies the chosen resolution strategy on disk and removes the conflict record.
func (cs *ConflictStore) Resolve(folderRoot string, folderID, conflictID string, res ConflictResolution) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, ok := cs.conflicts[folderID][conflictID]
	if !ok {
		return os.ErrNotExist
	}

	if !validRelativePath(c.Path) || !validRelativePath(c.ConflictPath) {
		return fmt.Errorf("invalid conflict path")
	}
	root, err := os.OpenRoot(folderRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	if res == ResolutionKeepRemote && c.LocalSHA256 != "" {
		if err := checkSyncDestination(root, c.Path, &fileDescriptor{SHA256: c.LocalSHA256}); err != nil {
			return err
		}
	}
	if c.RemoteSHA256 != "" {
		if err := checkSyncDestination(root, c.ConflictPath, &fileDescriptor{SHA256: c.RemoteSHA256}); err != nil {
			return err
		}
	}

	switch res {
	case ResolutionKeepLocal:
		// Delete the conflict copy
		if err := root.Remove(c.ConflictPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove conflict copy: %w", err)
		}
	case ResolutionKeepRemote:
		// Replace original file with the conflict copy
		f, err := openSyncFile(root, c.ConflictPath)
		if err != nil {
			return err
		}
		_ = f.Close()
		if err := root.Rename(c.ConflictPath, c.Path); err != nil {
			return fmt.Errorf("replace original with conflict copy: %w", err)
		}
	case ResolutionKeepBoth:
		// Keep both files on disk; just remove from conflict tracking
	default:
		return fmt.Errorf("invalid resolution: %s (must be 'local', 'remote', or 'both')", res)
	}

	delete(cs.conflicts[folderID], conflictID)
	return nil
}
