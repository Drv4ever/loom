package loom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	errFolderPaused  = errors.New("folder sync is paused")
	errNetworkPaused = errors.New("network is paused")
	errFolderBusy    = errors.New("folder sync is already running")
)

func (a *App) syncLoop() {
	defer close(a.watcherDone)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.mu.RLock()
			ids := make([]string, 0, len(a.folders))
			for id, entry := range a.folders {
				peer := a.peers[entry.Folder.PeerID]
				if !entry.Folder.Paused && !entry.syncing && peer != nil && peer.Device.Online {
					ids = append(ids, id)
				}
			}
			a.mu.RUnlock()
			for _, id := range ids {
				go func(id string) { _ = a.syncFolder(context.Background(), id) }(id)
			}
		}
	}
}

func (a *App) addFolder(path, peerID, name string) (Folder, error) {
	return a.addFolderWithOptions(path, peerID, name, nil, false)
}

func (a *App) addFolderWithOptions(path, peerID, name string, allowedPeers []string, readOnly bool) (Folder, error) {
	if !filepath.IsAbs(path) {
		return Folder{}, fmt.Errorf("folder path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Folder{}, fmt.Errorf("folder path is unavailable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return Folder{}, fmt.Errorf("selected path must be a folder")
	}
	if pathsOverlap(resolved, a.dataDir) {
		return Folder{}, fmt.Errorf("shared folder must be outside Loom data directory")
	}
	for _, id := range allowedPeers {
		if _, err := parsePeerID(id); err != nil {
			return Folder{}, err
		}
	}
	if len(allowedPeers) > 0 && !containsPeer(allowedPeers, peerID) {
		return Folder{}, fmt.Errorf("selected device must have folder access")
	}
	if _, err := parsePeerID(peerID); err != nil {
		return Folder{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.connected {
		return Folder{}, errNetworkPaused
	}
	peerInfo, ok := a.peers[peerID]
	if !ok || !peerInfo.Device.Online {
		return Folder{}, fmt.Errorf("selected device is offline")
	}
	if name == "" {
		name = filepath.Base(resolved)
	}
	if len(name) > 120 {
		return Folder{}, fmt.Errorf("folder name must be under 120 characters")
	}
	id, err := randomSuffix()
	if err != nil {
		return Folder{}, err
	}
	folder := Folder{
		ID:           id,
		Name:         name,
		Path:         resolved,
		PeerID:       peerID,
		PeerName:     peerInfo.Device.Name,
		Status:       "pending",
		AllowedPeers: allowedPeers,
		ReadOnly:     readOnly,
		Mode:         modeTwoWay,
		Manifest:     nil,
	}
	if readOnly {
		folder.Mode = modeSendOnly
	}
	a.folders[id] = &folderEntry{Folder: folder, manifest: make(map[string]string)}
	a.addActivityLocked("sync", "Folder added", fmt.Sprintf("%s is ready to sync with %s.", name, peerInfo.Device.Name))
	if err := a.persistLocked(); err != nil {
		delete(a.folders, id)
		return Folder{}, fmt.Errorf("save folder: %w", err)
	}
	return folder, nil
}

func (a *App) removeFolder(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.folders[id]
	if !ok {
		return os.ErrNotExist
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	delete(a.folders, id)
	a.addActivityLocked("sync", "Folder removed", fmt.Sprintf("Stopped tracking %s.", entry.Folder.Name))
	return a.persistLocked()
}

func (a *App) setFolderPaused(id string, paused bool) (Folder, error) {
	a.mu.Lock()
	entry, ok := a.folders[id]
	if !ok {
		a.mu.Unlock()
		return Folder{}, os.ErrNotExist
	}
	entry.Folder.Paused = paused
	if paused {
		entry.Folder.Status = "paused"
		entry.Folder.Error = ""
		if entry.cancel != nil {
			entry.cancel()
		}
	} else if entry.Folder.Status == "paused" {
		peer := a.peers[entry.Folder.PeerID]
		if peer == nil || !peer.Device.Online {
			entry.Folder.Status = "offline"
			entry.Folder.Error = "device is offline"
		} else {
			entry.Folder.Status = "pending"
			entry.Folder.Error = ""
		}
	}
	_ = a.persistLocked()
	folder := entry.Folder
	a.mu.Unlock()
	if !paused {
		go func() { _ = a.syncFolder(context.Background(), id) }()
	}
	return folder, nil
}

func (a *App) syncFolder(requestCtx context.Context, id string) error {
	return a.syncBidirectional(requestCtx, id)
}

func syncContextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (a *App) finishSync(id string, err error, transferred int64, manifest map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.folders[id]
	if !ok {
		return
	}
	entry.syncing = false
	entry.cancel = nil
	if errors.Is(err, context.Canceled) && (entry.Folder.Paused || !a.connected || a.ctx.Err() != nil) {
		if entry.Folder.Paused {
			entry.Folder.Status = "paused"
		} else if !a.connected {
			entry.Folder.Status = "offline"
		} else {
			entry.Folder.Status = "pending"
		}
		entry.Folder.Error = ""
	} else if err != nil {
		var response *syncResponseError
		if errors.As(err, &response) && response.status != "" {
			entry.Folder.Status = response.status
			entry.Folder.Error = err.Error()
			_ = a.persistLocked()
			return
		}
		changed := entry.Folder.Error != err.Error()
		entry.Folder.Status = "error"
		entry.Folder.Error = err.Error()
		if changed {
			a.addActivityLocked("sync", "Folder sync failed", fmt.Sprintf("%s: %s", entry.Folder.Name, err.Error()))
		}
	} else {
		entry.Folder.Status = "synced"
		entry.Folder.Error = ""
	}
	if manifest != nil {
		entry.manifest = manifest
	}
	_ = transferred
	_ = a.persistLocked()
}

func scanFolder(rootPath string) (map[string]fileDescriptor, int64, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, 0, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("shared root is no longer a regular directory")
	}
	files := make(map[string]fileDescriptor)
	var total int64
	matcher := LoadIgnoreMatcher(rootPath)

	err = filepath.WalkDir(rootPath, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fullPath == rootPath {
			return nil
		}
		relative, err := filepath.Rel(rootPath, fullPath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if !validRelativePath(name) || strings.HasPrefix(filepath.Base(name), ".loom-part-") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if matcher.ShouldIgnore(name, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if matcher.ShouldIgnore(name, false) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > maxFileSize {
			return fmt.Errorf("%s exceeds the 100 MiB transfer limit", name)
		}
		if len(files) >= maxSyncFiles {
			return fmt.Errorf("folder exceeds the file count limit")
		}
		file, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		read, readErr := io.Copy(hash, file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if read != info.Size() {
			return fmt.Errorf("%s changed while it was being scanned", name)
		}
		total += read
		if total > 1<<30 {
			return fmt.Errorf("folder exceeds the 1 GiB scan limit")
		}
		files[name] = fileDescriptor{Path: name, Size: read, ModTime: info.ModTime(), SHA256: hex.EncodeToString(hash.Sum(nil))}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return files, total, nil
}
