package loom

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The receipt index lives outside the shared files and is updated only after a
// verified file has been installed. Paths always come from our managed root.
func (n *Node) loadReceived() error {
	data, err := os.ReadFile(n.receivedPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read received folders: %w", err)
	}
	var saved map[string]ReceivedFolder
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("decode received folders: %w", err)
	}
	for id, folder := range saved {
		if !isID(id) {
			return fmt.Errorf("invalid received folder id")
		}
		if _, err := parsePeerID(folder.PeerID); err != nil {
			return fmt.Errorf("invalid received folder sender")
		}
		for _, file := range folder.Files {
			if !validRelativePath(file.Path) || file.Size < 0 || file.Size > maxFileSize || !validSHA256(file.SHA256) {
				return fmt.Errorf("invalid received file record")
			}
		}
		if err := validateManifest(nil, folder.Tombstones, nil); err != nil {
			return err
		}
		folder.ID = id
		if folder.Path == "" {
			folder.Path = filepath.Join(n.ReceiveDir, id)
		}
		if !filepath.IsAbs(folder.Path) {
			return fmt.Errorf("invalid received destination")
		}
		if folder.Status == "" {
			folder.Status = "pending"
		}
		if folder.Mode == "" {
			folder.Mode = modeTwoWay
		}
		n.received[id] = folder
	}
	return nil
}

func (n *Node) recordReceived(id, name, sender string, file fileDescriptor) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	folder := n.received[id]
	folder.ID, folder.PeerID = id, sender
	folder.Name = name
	if folder.Name == "" {
		folder.Name = id
	}
	folder.Path = filepath.Join(n.ReceiveDir, id)
	folder.LastReceived = time.Now().UTC()
	files := make([]fileDescriptor, 0, len(folder.Files)+1)
	for _, prior := range folder.Files {
		if prior.Path != file.Path {
			files = append(files, prior)
		}
	}
	folder.Files = append(files, file)
	folder.Bytes = 0
	for _, entry := range folder.Files {
		folder.Bytes += entry.Size
	}
	saved := make(map[string]ReceivedFolder, len(n.received)+1)
	for key, value := range n.received {
		saved[key] = value
	}
	saved[id] = folder
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(n.receivedPath), ".received-*")
	if err != nil {
		return fmt.Errorf("create receipt index: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("save receipt index: %w", err)
	}
	if err := os.Rename(tmp.Name(), n.receivedPath); err != nil {
		return fmt.Errorf("install receipt index: %w", err)
	}
	n.received = saved
	return nil
}

func (n *Node) ReceivedFolders() []ReceivedFolder {
	n.mu.RLock()
	defer n.mu.RUnlock()
	folders := make([]ReceivedFolder, 0, len(n.received))
	for _, folder := range n.received {
		folder.Tombstones = nil
		folder.Files = append([]fileDescriptor{}, folder.Files...)
		sort.Slice(folder.Files, func(i, j int) bool { return folder.Files[i].Path < folder.Files[j].Path })
		folders = append(folders, folder)
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Name < folders[j].Name })
	return folders
}

func (a *App) handleReceivedFile(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("path")
	if !isID(id) || !validRelativePath(name) {
		writeError(w, http.StatusBadRequest, "invalid received file path")
		return
	}
	a.main.mu.RLock()
	folder, exists := a.main.received[id]
	found := false
	for _, file := range folder.Files {
		if file.Path == name {
			found = true
			break
		}
	}
	a.main.mu.RUnlock()
	if !exists || !found || (folder.Status != "" && folder.Status != "accepted" && folder.Status != "paused") {
		writeError(w, http.StatusNotFound, "received file was not found")
		return
	}
	root, err := os.OpenRoot(folder.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "received folder is unavailable")
		return
	}
	defer root.Close()
	file, err := openSyncFile(root, name)
	if err != nil {
		writeError(w, http.StatusNotFound, "received file is unavailable")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "received file is unavailable")
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(name)}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
