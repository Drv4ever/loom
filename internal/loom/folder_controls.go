package loom

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) handleIncomingFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isID(id) {
		writeError(w, 400, "invalid folder id")
		return
	}
	var body struct {
		Action string `json:"action"`
		Path   string `json:"path"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	n := a.main
	// Stop an in-flight transfer before changing its destination or permission.
	n.mu.RLock()
	active := n.activeSync[id]
	n.mu.RUnlock()
	if active != nil {
		_ = active.Reset()
	}
	n.receiveMu.Lock()
	defer n.receiveMu.Unlock()
	n.mu.RLock()
	folder, exists := n.received[id]
	n.mu.RUnlock()
	if !exists {
		writeError(w, 404, "incoming folder not found")
		return
	}
	switch body.Action {
	case "accept":
		destination := folder.Path
		if body.Path != "" && filepath.Clean(body.Path) != filepath.Clean(folder.Path) {
			if !filepath.IsAbs(body.Path) {
				writeError(w, 400, "destination must be an absolute path")
				return
			}
			resolved, err := filepath.EvalSymlinks(body.Path)
			if err != nil {
				writeError(w, 400, "choose an existing destination directory")
				return
			}
			info, err := os.Stat(resolved)
			if err != nil || !info.IsDir() {
				writeError(w, 400, "destination must be a directory")
				return
			}
			if resolved != folder.Path {
				entries, err := os.ReadDir(resolved)
				if err != nil || len(entries) != 0 {
					writeError(w, 400, "choose an empty destination directory")
					return
				}
				if pathsOverlap(resolved, a.dataDir) {
					writeError(w, 400, "destination must be outside Loom's data directory")
					return
				}
				a.mu.RLock()
				overlap := false
				for _, f := range a.folders {
					if pathsOverlap(resolved, f.Folder.Path) {
						overlap = true
					}
				}
				a.mu.RUnlock()
				n.mu.RLock()
				for otherID, f := range n.received {
					if otherID != id && pathsOverlap(resolved, f.Path) {
						overlap = true
					}
				}
				n.mu.RUnlock()
				if overlap {
					writeError(w, 400, "destination overlaps another shared folder")
					return
				}
			}
			destination = resolved
		}
		if err := os.MkdirAll(destination, 0700); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		info, err := os.Lstat(destination)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			writeError(w, 400, "destination must be a regular directory")
			return
		}
		if destination != folder.Path {
			folder.Files = []fileDescriptor{}
			folder.Bytes = 0
			folder.Tombstones = nil
		}
		folder.Path = destination
		folder.Status = "accepted"
	case "pause":
		if folder.Status != "accepted" {
			writeError(w, 409, "accept the folder before pausing it")
			return
		}
		folder.Status = "paused"
	case "resume":
		if folder.Status != "paused" {
			writeError(w, 409, "only paused folders can be resumed")
			return
		}
		folder.Status = "accepted"
	case "decline", "remove":
		folder.Status = "declined"
	default:
		writeError(w, 400, "action must be accept, decline, pause, resume, or remove")
		return
	}
	n.mu.Lock()
	old := n.received[id]
	n.received[id] = folder
	err := n.saveReceivedLocked()
	if err != nil {
		n.received[id] = old
	}
	n.mu.Unlock()
	if err != nil {
		writeError(w, 500, "could not save folder settings")
		return
	}
	a.mu.Lock()
	a.addActivityLocked("sync", "Folder sharing updated", folder.Name+": "+folder.Status)
	_ = a.persistLocked()
	a.mu.Unlock()
	folder.Tombstones = nil
	writeJSON(w, http.StatusOK, folder)
}

func pathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func (a *App) localFolderPath(id string) (string, error) {
	if !isID(id) {
		return "", fmt.Errorf("invalid folder id")
	}
	a.mu.RLock()
	entry := a.folders[id]
	if entry != nil {
		p := entry.Folder.Path
		busy := entry.syncing
		a.mu.RUnlock()
		if busy {
			return "", errFolderBusy
		}
		return p, nil
	}
	a.mu.RUnlock()
	a.main.mu.RLock()
	folder, exists := a.main.received[id]
	a.main.mu.RUnlock()
	if !exists {
		return "", os.ErrNotExist
	}
	if folder.Status != "accepted" && folder.Status != "paused" {
		return "", fmt.Errorf("accept the folder before accessing it")
	}
	return folder.Path, nil
}

func (a *App) handleRestoreTrash(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	a.main.receiveMu.Lock()
	defer a.main.receiveMu.Unlock()
	// Serialize against the outgoing sync worker and conflict resolution.
	a.mu.Lock()
	entry := a.folders[id]
	if entry != nil && entry.syncing {
		a.mu.Unlock()
		writeMutationError(w, errFolderBusy)
		return
	}
	path := ""
	if entry != nil {
		path = entry.Folder.Path
	} else {
		a.main.mu.RLock()
		f, ok := a.main.received[id]
		a.main.mu.RUnlock()
		if ok && (f.Status == "accepted" || f.Status == "paused") {
			path = f.Path
		}
	}
	if path == "" {
		a.mu.Unlock()
		writeError(w, 404, "folder not found")
		return
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		a.mu.Unlock()
		writeError(w, 400, err.Error())
		return
	}
	defer root.Close()
	restored, err := a.main.trash.Restore(id, body.ID, root)
	if err == nil {
		if entry != nil {
			delete(entry.tombstones, restored)
		}
		a.addActivityLocked("sync", "Deleted file restored", restored)
		err = a.persistLocked()
	}
	a.mu.Unlock()
	if err != nil {
		writeMutationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": restored})
}
