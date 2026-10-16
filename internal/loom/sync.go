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
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
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
				if !entry.Folder.Paused && !entry.syncing {
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
	id, err := randomSuffix()
	if err != nil {
		return Folder{}, err
	}
	folder := Folder{ID: id, Name: name, Path: resolved, PeerID: peerID, PeerName: peerInfo.Device.Name, Status: "pending", Manifest: nil}
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
		entry.Folder.Status = "pending"
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
	a.mu.Lock()
	entry, ok := a.folders[id]
	if !ok {
		a.mu.Unlock()
		return os.ErrNotExist
	}
	if !a.connected {
		a.mu.Unlock()
		return errNetworkPaused
	}
	if entry.Folder.Paused {
		a.mu.Unlock()
		return errFolderPaused
	}
	if entry.syncing {
		a.mu.Unlock()
		return errFolderBusy
	}
	peerID, err := parsePeerID(entry.Folder.PeerID)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	connectedness := a.main.Host.Network().Connectedness(peerID)
	if connectedness != network.Connected && connectedness != network.Limited {
		a.mu.Unlock()
		return fmt.Errorf("selected device is offline")
	}
	entry.syncing = true
	entry.Folder.Status = "syncing"
	entry.Folder.Error = ""
	entry.Folder.Progress = 0
	syncCtx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	entry.cancel = cancel
	previous := make(map[string]string, len(entry.manifest))
	for path, digest := range entry.manifest {
		previous[path] = digest
	}
	folder := entry.Folder
	_ = a.persistLocked()
	a.mu.Unlock()
	if requestCtx != nil {
		stop := context.AfterFunc(requestCtx, cancel)
		defer stop()
	}

	files, totalBytes, err := scanFolder(folder.Path)
	if err != nil {
		a.finishSync(id, err, 0, nil)
		return err
	}
	a.mu.Lock()
	if current := a.folders[id]; current == nil {
		a.mu.Unlock()
		return os.ErrNotExist
	} else {
		current.Folder.Files = len(files)
		current.Folder.Bytes = totalBytes
		current.Folder.Progress = 0
		_ = a.persistLocked()
	}
	a.mu.Unlock()

	sourceRoot, err := os.OpenRoot(folder.Path)
	if err != nil {
		a.finishSync(id, err, 0, nil)
		return err
	}
	defer sourceRoot.Close()
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	transferred := int64(0)
	changed := 0
	newManifest := make(map[string]string, len(files))
	for i, name := range paths {
		if err := syncContextError(syncCtx); err != nil {
			a.finishSync(id, err, transferred, newManifest)
			return err
		}
		desc := files[name]
		if previous[name] == desc.SHA256 {
			newManifest[name] = desc.SHA256
			continue
		}
		input, err := sourceRoot.Open(filepath.FromSlash(name))
		if err != nil {
			a.finishSync(id, err, transferred, newManifest)
			return fmt.Errorf("open source file %s: %w", name, err)
		}
		n, transferErr := a.main.TransferFile(network.WithAllowLimitedConn(syncCtx, "loom folder sync"), peerID, id, desc, input)
		closeErr := input.Close()
		transferred += n
		if transferErr != nil {
			a.finishSync(id, transferErr, transferred, newManifest)
			return fmt.Errorf("transfer %s: %w", name, transferErr)
		}
		if closeErr != nil {
			a.finishSync(id, closeErr, transferred, newManifest)
			return closeErr
		}
		changed++
		newManifest[name] = desc.SHA256
		a.mu.Lock()
		a.transferredBytes += n
		if current := a.folders[id]; current != nil {
			current.Folder.Progress = (i + 1) * 100 / max(1, len(files))
			current.manifest[name] = desc.SHA256
		}
		_ = a.persistLocked()
		a.mu.Unlock()
	}
	if err := syncContextError(syncCtx); err != nil {
		a.finishSync(id, err, transferred, newManifest)
		return err
	}
	a.mu.Lock()
	if current := a.folders[id]; current != nil {
		current.syncing = false
		current.cancel = nil
		current.manifest = newManifest
		current.Folder.Files = len(files)
		current.Folder.Bytes = totalBytes
		current.Folder.Progress = 100
		current.Folder.Status = "synced"
		current.Folder.LastSync = time.Now().UTC()
		current.Folder.Error = ""
		if changed > 0 {
			a.addActivityLocked("sync", "Folder synced", fmt.Sprintf("Transferred %d file(s) to %s.", changed, current.Folder.PeerName))
		}
	}
	_ = a.persistLocked()
	a.mu.Unlock()
	return nil
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
		entry.Folder.Status = "error"
		entry.Folder.Error = err.Error()
		a.addActivityLocked("sync", "Folder sync failed", fmt.Sprintf("%s: %s", entry.Folder.Name, err.Error()))
	} else {
		entry.Folder.Status = "synced"
		entry.Folder.Error = ""
	}
	for name, digest := range manifest {
		entry.manifest[name] = digest
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
	files := make(map[string]fileDescriptor)
	var total int64
	err = filepath.WalkDir(rootPath, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fullPath == rootPath {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(rootPath, fullPath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if !validRelativePath(name) || strings.HasPrefix(name, ".loom-part-") {
			return nil
		}
		if info.Size() > maxFileSize {
			return fmt.Errorf("%s exceeds the %d MB per-file limit", name, maxFileSize>>20)
		}
		file, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		read, readErr := io.Copy(hash, io.LimitReader(file, maxFileSize+1))
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
			return fmt.Errorf("folder exceeds the 1 GB sync limit")
		}
		files[name] = fileDescriptor{Path: name, Size: read, ModTime: info.ModTime(), SHA256: hex.EncodeToString(hash.Sum(nil))}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return files, total, nil
}
