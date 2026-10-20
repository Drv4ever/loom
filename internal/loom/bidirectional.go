package loom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const syncProtocol = protocol.ID("/loom/sync/2.0.0")

// SyncHello is the opening handshake from the initiator.
type SyncHello struct {
	FolderID   string                    `json:"folderId"`
	FolderName string                    `json:"folderName"`
	SenderName string                    `json:"senderName"`
	Manifest   map[string]fileDescriptor `json:"manifest"`
	Tombstones map[string]time.Time      `json:"tombstones"`
	Baseline   map[string]string         `json:"baseline"`
}

// SyncHelloReply is returned by the responder.
type SyncHelloReply struct {
	OK           bool                      `json:"ok"`
	Error        string                    `json:"error,omitempty"`
	ReceiverName string                    `json:"receiverName"`
	Manifest     map[string]fileDescriptor `json:"manifest"`
	Tombstones   map[string]time.Time      `json:"tombstones"`
}

// SyncPlanItem specifies an operation for a specific file.
type SyncPlanItem struct {
	Path         string
	Action       string // "send", "fetch", "conflict", "delete_local", "delete_remote"
	SenderDesc   fileDescriptor
	ReceiverDesc fileDescriptor
}

// TransferHeader announces a file transfer within the sync stream.
type TransferHeader struct {
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"modTime"`
	SHA256      string    `json:"sha256"`
	IsConflict  bool      `json:"isConflict"`
	ConflictPath string   `json:"conflictPath,omitempty"`
	IsDelta     bool      `json:"isDelta"`
	End         bool      `json:"end"` // true when batch of transfers is complete
}

// TransferAck confirms receipt of a file or chunk batch.
type TransferAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// NeededChunksReply returns the list of chunks required for a delta sync.
type NeededChunksReply struct {
	Needed []int `json:"needed"`
}

// ChunkFrame transports one chunk of delta data.
type ChunkFrame struct {
	Index int   `json:"index"`
	Size  int64 `json:"size"`
	Last  bool  `json:"last"`
}

// computeSyncPlan calculates what needs to be transferred or resolved.
func computeSyncPlan(
	localManifest map[string]fileDescriptor,
	localTombstones map[string]time.Time,
	remoteManifest map[string]fileDescriptor,
	remoteTombstones map[string]time.Time,
	baseline map[string]string,
) []SyncPlanItem {
	allPaths := make(map[string]bool)
	for p := range localManifest {
		allPaths[p] = true
	}
	for p := range remoteManifest {
		allPaths[p] = true
	}
	for p := range localTombstones {
		allPaths[p] = true
	}
	for p := range remoteTombstones {
		allPaths[p] = true
	}

	var plan []SyncPlanItem
	for p := range allPaths {
		locDesc, hasLoc := localManifest[p]
		remDesc, hasRem := remoteManifest[p]
		locDeleted := localTombstones[p]
		remDeleted := remoteTombstones[p]
		baseHash := baseline[p]

		// Deletion handling
		if !locDeleted.IsZero() && hasRem {
			if remDesc.SHA256 == baseHash || baseHash == "" {
				plan = append(plan, SyncPlanItem{Path: p, Action: "delete_remote", ReceiverDesc: remDesc})
			} else {
				plan = append(plan, SyncPlanItem{Path: p, Action: "fetch", ReceiverDesc: remDesc})
			}
			continue
		}
		if !remDeleted.IsZero() && hasLoc {
			if locDesc.SHA256 == baseHash || baseHash == "" {
				plan = append(plan, SyncPlanItem{Path: p, Action: "delete_local", SenderDesc: locDesc})
			} else {
				plan = append(plan, SyncPlanItem{Path: p, Action: "send", SenderDesc: locDesc})
			}
			continue
		}

		if !hasLoc && !hasRem {
			continue
		}

		if hasLoc && !hasRem {
			plan = append(plan, SyncPlanItem{Path: p, Action: "send", SenderDesc: locDesc})
			continue
		}

		if !hasLoc && hasRem {
			plan = append(plan, SyncPlanItem{Path: p, Action: "fetch", ReceiverDesc: remDesc})
			continue
		}

		// Both present
		if locDesc.SHA256 == remDesc.SHA256 {
			continue
		}

		if baseHash != "" {
			if locDesc.SHA256 == baseHash {
				plan = append(plan, SyncPlanItem{Path: p, Action: "fetch", ReceiverDesc: remDesc})
			} else if remDesc.SHA256 == baseHash {
				plan = append(plan, SyncPlanItem{Path: p, Action: "send", SenderDesc: locDesc})
			} else {
				plan = append(plan, SyncPlanItem{
					Path:         p,
					Action:       "conflict",
					SenderDesc:   locDesc,
					ReceiverDesc: remDesc,
				})
			}
		} else {
			plan = append(plan, SyncPlanItem{
				Path:         p,
				Action:       "conflict",
				SenderDesc:   locDesc,
				ReceiverDesc: remDesc,
			})
		}
	}

	return plan
}

// handleBidirectionalSync handles incoming /loom/sync/2.0.0 streams on the responder.
func (n *Node) handleBidirectionalSync(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Minute))

	if !n.enabled.Load() {
		_ = writeFrame(stream, mustJSON(SyncHelloReply{OK: false, Error: "network is paused"}))
		return
	}

	var hello SyncHello
	if err := readFrame(stream, &hello, 2<<20); err != nil {
		return
	}

	remotePeerID := stream.Conn().RemotePeer().String()
	if n.checkFolderAccess != nil {
		if err := n.checkFolderAccess(hello.FolderID, remotePeerID, true); err != nil {
			_ = writeFrame(stream, mustJSON(SyncHelloReply{OK: false, Error: err.Error()}))
			return
		}
	}

	n.receiveMu.Lock()
	defer n.receiveMu.Unlock()

	targetDir := n.getFolderDirectory(hello.FolderID)
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		_ = writeFrame(stream, mustJSON(SyncHelloReply{OK: false, Error: err.Error()}))
		return
	}

	localFiles, _, err := scanFolder(targetDir)
	if err != nil {
		_ = writeFrame(stream, mustJSON(SyncHelloReply{OK: false, Error: err.Error()}))
		return
	}

	localTombstones := n.getTombstones(hello.FolderID)

	reply := SyncHelloReply{
		OK:           true,
		ReceiverName: n.Name,
		Manifest:     localFiles,
		Tombstones:   localTombstones,
	}
	if err := writeFrame(stream, mustJSON(reply)); err != nil {
		return
	}

	// 1. Receive files sent from initiator (A -> B)
	if err := n.receiveBatch(stream, targetDir, hello.FolderID, hello.SenderName); err != nil {
		return
	}

	// 2. Send files to initiator (B -> A)
	if err := n.sendBatch(stream, targetDir, hello.FolderID); err != nil {
		return
	}

	// 3. Receive deletion tombstones from initiator
	var delFrame struct {
		Deletions []string `json:"deletions"`
	}
	if err := readFrame(stream, &delFrame, 1<<20); err == nil {
		for _, rel := range delFrame.Deletions {
			full := filepath.Join(targetDir, filepath.FromSlash(rel))
			if _, err := os.Stat(full); err == nil {
				_, _ = n.trash.MoveToTrash(hello.FolderID, rel, full)
				n.recordTombstone(hello.FolderID, rel)
			}
		}
		_ = writeFrame(stream, mustJSON(TransferAck{OK: true}))
	}

	// Record received catalog for visibility
	rescanned, _, _ := scanFolder(targetDir)
	fileList := make([]fileDescriptor, 0, len(rescanned))
	for _, f := range rescanned {
		fileList = append(fileList, f)
	}
	senderPeerID := stream.Conn().RemotePeer().String()
	n.mu.Lock()
	folder := n.received[hello.FolderID]
	folder.ID = hello.FolderID
	folder.Name = hello.FolderName
	if folder.Name == "" {
		folder.Name = hello.FolderID
	}
	folder.Path = targetDir
	folder.PeerID = senderPeerID
	folder.LastReceived = time.Now().UTC()
	folder.Files = fileList
	folder.Bytes = 0
	for _, f := range fileList {
		folder.Bytes += f.Size
	}
	n.received[hello.FolderID] = folder
	n.mu.Unlock()
	_ = n.saveReceivedLocked()
}

// receiveBatch receives a sequence of TransferHeader frames until header.End == true.
func (n *Node) receiveBatch(stream network.Stream, folderDir, folderID, peerName string) error {
	for {
		var hdr TransferHeader
		if err := readFrame(stream, &hdr, 64<<10); err != nil {
			return err
		}
		if hdr.End {
			break
		}

		targetRelPath := hdr.Path
		if hdr.IsConflict && hdr.ConflictPath != "" {
			targetRelPath = hdr.ConflictPath
		}

		targetFullPath := filepath.Join(folderDir, filepath.FromSlash(targetRelPath))
		if info, err := os.Lstat(targetFullPath); err == nil {
			if !info.Mode().IsRegular() {
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: "destination is not a regular file"}))
				return fmt.Errorf("destination is not a regular file")
			}
		}
		if err := os.MkdirAll(filepath.Dir(targetFullPath), 0700); err != nil {
			_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
			return err
		}

		// Disk space pre-check
		if err := CheckFreeDiskSpace(folderDir, hdr.Size); err != nil {
			_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
			return err
		}

		if hdr.IsDelta {
			// Read chunk manifest
			var manifest FileChunkManifest
			if err := readFrame(stream, &manifest, 2<<20); err != nil {
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				return err
			}

			needed, err := FindNeededChunks(targetFullPath, &manifest)
			if err != nil {
				needed = make([]int, len(manifest.Chunks))
				for i := range manifest.Chunks {
					needed[i] = i
				}
			}

			// Send needed chunks reply
			if err := writeFrame(stream, mustJSON(NeededChunksReply{Needed: needed})); err != nil {
				return err
			}

			// Read incoming chunks
			chunkMap := make(map[int][]byte, len(needed))
			for i := 0; i < len(needed); i++ {
				var cf ChunkFrame
				if err := readFrame(stream, &cf, 16<<10); err != nil {
					return err
				}
				buf := make([]byte, cf.Size)
				if _, err := io.ReadFull(stream, buf); err != nil {
					return err
				}
				chunkMap[cf.Index] = buf
			}

			// Assemble file
			tmpPath := targetFullPath + ".part-" + fmt.Sprintf("%d", time.Now().UnixNano())
			err = AssemblePatchedFile(tmpPath, targetFullPath, &manifest, func(idx int) ([]byte, error) {
				data, ok := chunkMap[idx]
				if !ok {
					return nil, errors.New("missing chunk")
				}
				return data, nil
			})
			if err != nil {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				return err
			}

			if err := os.Rename(tmpPath, targetFullPath); err != nil {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				return fmt.Errorf("install received file: %w", err)
			}
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(targetFullPath, hdr.ModTime, hdr.ModTime)
			}
		} else {
			// Direct stream transfer
			tmpPath := targetFullPath + ".part-" + fmt.Sprintf("%d", time.Now().UnixNano())
			tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				return err
			}

			hasher := sha256.New()
			written, copyErr := io.CopyN(io.MultiWriter(tmp, hasher), stream, hdr.Size)
			_ = tmp.Sync()
			_ = tmp.Close()

			if copyErr != nil || written != hdr.Size {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: "stream read failure"}))
				return fmt.Errorf("read file stream: %w", copyErr)
			}

			calcHash := hex.EncodeToString(hasher.Sum(nil))
			if hdr.SHA256 != "" && calcHash != hdr.SHA256 {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: "checksum mismatch"}))
				return fmt.Errorf("checksum mismatch")
			}

			if err := os.Rename(tmpPath, targetFullPath); err != nil {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				return fmt.Errorf("install received file: %w", err)
			}
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(targetFullPath, hdr.ModTime, hdr.ModTime)
			}
		}

		// If this was a conflict, record in ConflictStore
		if hdr.IsConflict {
			n.conflicts.Add(Conflict{
				ID:           fmt.Sprintf("conf-%d", time.Now().UnixNano()),
				FolderID:     folderID,
				Path:         hdr.Path,
				ConflictPath: targetRelPath,
				LocalSize:    hdr.Size,
				RemoteSize:   hdr.Size,
				DeviceName:   peerName,
				CreatedAt:    time.Now().UTC(),
			})
		}

		_ = writeFrame(stream, mustJSON(TransferAck{OK: true}))
	}

	return nil
}

// sendBatch sends files requested by initiator or needed by responder.
func (n *Node) sendBatch(stream network.Stream, folderDir, folderID string) error {
	// Read request list
	var req struct {
		FetchPaths []string `json:"fetchPaths"`
	}
	if err := readFrame(stream, &req, 1<<20); err != nil {
		return err
	}

	for _, relPath := range req.FetchPaths {
		fullPath := filepath.Join(folderDir, filepath.FromSlash(relPath))
		stat, err := os.Stat(fullPath)
		if err != nil {
			continue
		}

		hasher := sha256.New()
		f, err := os.Open(fullPath)
		if err != nil {
			continue
		}
		_, _ = io.Copy(hasher, f)
		_ = f.Close()
		fileHash := hex.EncodeToString(hasher.Sum(nil))

		hdr := TransferHeader{
			Path:    relPath,
			Size:    stat.Size(),
			ModTime: stat.ModTime(),
			SHA256:  fileHash,
			IsDelta: stat.Size() >= DefaultChunkSize,
		}

		if err := writeFrame(stream, mustJSON(hdr)); err != nil {
			return err
		}

		if hdr.IsDelta {
			manifest, err := GenerateChunkManifest(fullPath, DefaultChunkSize)
			if err != nil {
				return err
			}
			if err := writeFrame(stream, mustJSON(manifest)); err != nil {
				return err
			}
			var neededReply NeededChunksReply
			if err := readFrame(stream, &neededReply, 64<<10); err != nil {
				return err
			}

			// Send needed chunks
			fileToRead, err := os.Open(fullPath)
			if err != nil {
				return err
			}
			for _, idx := range neededReply.Needed {
				chunk := manifest.Chunks[idx]
				buf := make([]byte, chunk.Size)
				_, _ = fileToRead.ReadAt(buf, chunk.Offset)
				cf := ChunkFrame{Index: idx, Size: chunk.Size}
				if err := writeFrame(stream, mustJSON(cf)); err != nil {
					_ = fileToRead.Close()
					return err
				}
				if _, err := stream.Write(buf); err != nil {
					_ = fileToRead.Close()
					return err
				}
			}
			_ = fileToRead.Close()
		} else {
			fileToRead, err := os.Open(fullPath)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(stream, fileToRead)
			_ = fileToRead.Close()
			if copyErr != nil {
				return copyErr
			}
		}

		var ack TransferAck
		if err := readFrame(stream, &ack, 16<<10); err != nil || !ack.OK {
			return fmt.Errorf("transfer ack error: %v", ack.Error)
		}
	}

	// Signal end of batch
	_ = writeFrame(stream, mustJSON(TransferHeader{End: true}))
	return nil
}

func (n *Node) getFolderDirectory(folderID string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.getCustomPath != nil {
		if p := n.getCustomPath(folderID); p != "" {
			return p
		}
	}
	return filepath.Join(n.ReceiveDir, folderID)
}

func (n *Node) getTombstones(folderID string) map[string]time.Time {
	n.mu.RLock()
	defer n.mu.RUnlock()
	res := make(map[string]time.Time)
	for k, v := range n.tombstones[folderID] {
		res[k] = v
	}
	return res
}

func (n *Node) recordTombstone(folderID, relPath string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.tombstones[folderID] == nil {
		n.tombstones[folderID] = make(map[string]time.Time)
	}
	n.tombstones[folderID][relPath] = time.Now().UTC()
}

func (n *Node) saveReceivedLocked() error {
	data, err := mustJSONPretty(n.received)
	if err != nil {
		return err
	}
	tmp := n.receivedPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, n.receivedPath)
}

func mustJSONPretty(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// syncBidirectional is the initiator execution loop for two-way synchronization.
func (a *App) syncBidirectional(requestCtx context.Context, id string) error {
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
	folder := entry.Folder
	baseline := make(map[string]string, len(entry.manifest))
	for p, h := range entry.manifest {
		baseline[p] = h
	}
	_ = a.persistLocked()
	a.mu.Unlock()

	if requestCtx != nil {
		stop := context.AfterFunc(requestCtx, cancel)
		defer stop()
	}

	// 1. Scan local folder
	localFiles, totalBytes, err := scanFolder(folder.Path)
	if err != nil {
		a.finishSync(id, err, 0, nil)
		return err
	}

	a.mu.Lock()
	if current := a.folders[id]; current != nil {
		current.Folder.Files = len(localFiles)
		current.Folder.Bytes = totalBytes
		_ = a.persistLocked()
	}
	a.mu.Unlock()

	// 2. Compute local tombstones (files previously in baseline but missing from localFiles)
	localTombstones := make(map[string]time.Time)
	for p := range baseline {
		if _, exists := localFiles[p]; !exists {
			localTombstones[p] = time.Now().UTC()
		}
	}

	// 3. Open stream on /loom/sync/2.0.0
	stream, err := a.main.Host.NewStream(network.WithAllowLimitedConn(syncCtx, "loom folder bidirectional sync"), peerID, syncProtocol)
	if err != nil {
		// Fallback to legacy sync if peer doesn't support /loom/sync/2.0.0
		return a.syncFolderLegacy(syncCtx, id, folder, localFiles, baseline)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Minute))

	// 4. Send SyncHello
	hello := SyncHello{
		FolderID:   id,
		FolderName: folder.Name,
		SenderName: a.main.Name,
		Manifest:   localFiles,
		Tombstones: localTombstones,
		Baseline:   baseline,
	}
	if err := writeFrame(stream, mustJSON(hello)); err != nil {
		a.finishSync(id, err, 0, nil)
		return err
	}

	// 5. Read SyncHelloReply
	var reply SyncHelloReply
	if err := readFrame(stream, &reply, 2<<20); err != nil {
		a.finishSync(id, err, 0, nil)
		return err
	}
	if !reply.OK {
		err := errors.New(reply.Error)
		a.finishSync(id, err, 0, nil)
		return err
	}

	// 6. Compute reconciliation plan
	plan := computeSyncPlan(localFiles, localTombstones, reply.Manifest, reply.Tombstones, baseline)

	var toSend []SyncPlanItem
	var toFetch []SyncPlanItem
	var toDeleteLocal []SyncPlanItem
	var toDeleteRemote []string
	var conflicts []SyncPlanItem

	for _, item := range plan {
		switch item.Action {
		case "send":
			toSend = append(toSend, item)
		case "fetch":
			toFetch = append(toFetch, item)
		case "conflict":
			conflicts = append(conflicts, item)
		case "delete_local":
			toDeleteLocal = append(toDeleteLocal, item)
		case "delete_remote":
			toDeleteRemote = append(toDeleteRemote, item.Path)
		}
	}

	// 7. For conflicts, fetch remote version into conflict copy
	for _, c := range conflicts {
		conflictRelPath := GenerateConflictPath(c.Path, time.Now().UTC(), reply.ReceiverName)
		toFetch = append(toFetch, SyncPlanItem{
			Path:         c.Path,
			Action:       "conflict_fetch",
			ReceiverDesc: c.ReceiverDesc,
		})
		a.main.conflicts.Add(Conflict{
			ID:           fmt.Sprintf("conf-%d", time.Now().UnixNano()),
			FolderID:     id,
			Path:         c.Path,
			ConflictPath: conflictRelPath,
			LocalSize:    c.SenderDesc.Size,
			RemoteSize:   c.ReceiverDesc.Size,
			DeviceName:   reply.ReceiverName,
			CreatedAt:    time.Now().UTC(),
		})
	}

	// 8. Send files from A -> B
	transferred := int64(0)
	for _, item := range toSend {
		fullPath := filepath.Join(folder.Path, filepath.FromSlash(item.Path))
		stat, err := os.Stat(fullPath)
		if err != nil {
			continue
		}

		isDelta := stat.Size() >= DefaultChunkSize
		hdr := TransferHeader{
			Path:    item.Path,
			Size:    stat.Size(),
			ModTime: stat.ModTime(),
			SHA256:  item.SenderDesc.SHA256,
			IsDelta: isDelta,
		}
		if err := writeFrame(stream, mustJSON(hdr)); err != nil {
			a.finishSync(id, err, transferred, nil)
			return err
		}

		if isDelta {
			manifest, err := GenerateChunkManifest(fullPath, DefaultChunkSize)
			if err != nil {
				a.finishSync(id, err, transferred, nil)
				return err
			}
			if err := writeFrame(stream, mustJSON(manifest)); err != nil {
				a.finishSync(id, err, transferred, nil)
				return err
			}
			var neededReply NeededChunksReply
			if err := readFrame(stream, &neededReply, 64<<10); err != nil {
				a.finishSync(id, err, transferred, nil)
				return err
			}

			fileToRead, err := os.Open(fullPath)
			if err != nil {
				a.finishSync(id, err, transferred, nil)
				return err
			}
			for _, idx := range neededReply.Needed {
				chunk := manifest.Chunks[idx]
				buf := make([]byte, chunk.Size)
				_, _ = fileToRead.ReadAt(buf, chunk.Offset)
				cf := ChunkFrame{Index: idx, Size: chunk.Size}
				if err := writeFrame(stream, mustJSON(cf)); err != nil {
					_ = fileToRead.Close()
					a.finishSync(id, err, transferred, nil)
					return err
				}
				if _, err := stream.Write(buf); err != nil {
					_ = fileToRead.Close()
					a.finishSync(id, err, transferred, nil)
					return err
				}
				transferred += chunk.Size
			}
			_ = fileToRead.Close()
		} else {
			fileToRead, err := os.Open(fullPath)
			if err != nil {
				a.finishSync(id, err, transferred, nil)
				return err
			}
			n, copyErr := io.Copy(stream, fileToRead)
			_ = fileToRead.Close()
			if copyErr != nil {
				a.finishSync(id, copyErr, transferred, nil)
				return copyErr
			}
			transferred += n
		}

		var ack TransferAck
		if err := readFrame(stream, &ack, 16<<10); err != nil || !ack.OK {
			errMsg := "peer transfer error"
			if err != nil {
				errMsg = err.Error()
			} else if ack.Error != "" {
				errMsg = ack.Error
			}
			transferErr := errors.New(errMsg)
			a.finishSync(id, transferErr, transferred, nil)
			return transferErr
		}
	}
	_ = writeFrame(stream, mustJSON(TransferHeader{End: true}))

	// 9. Fetch files from B -> A
	fetchPaths := make([]string, 0, len(toFetch))
	for _, item := range toFetch {
		fetchPaths = append(fetchPaths, item.Path)
	}
	if err := writeFrame(stream, mustJSON(map[string]any{"fetchPaths": fetchPaths})); err != nil {
		a.finishSync(id, err, transferred, nil)
		return err
	}

	for {
		var hdr TransferHeader
		if err := readFrame(stream, &hdr, 64<<10); err != nil {
			a.finishSync(id, err, transferred, nil)
			return err
		}
		if hdr.End {
			break
		}

		targetRelPath := hdr.Path
		for _, c := range conflicts {
			if c.Path == hdr.Path {
				targetRelPath = GenerateConflictPath(c.Path, time.Now().UTC(), reply.ReceiverName)
				break
			}
		}

		targetFullPath := filepath.Join(folder.Path, filepath.FromSlash(targetRelPath))
		if info, err := os.Lstat(targetFullPath); err == nil && !info.Mode().IsRegular() {
			_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: "destination is not a regular file"}))
			a.finishSync(id, fmt.Errorf("destination is not a regular file"), transferred, nil)
			return fmt.Errorf("destination is not a regular file")
		}
		if err := os.MkdirAll(filepath.Dir(targetFullPath), 0700); err != nil {
			_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
			a.finishSync(id, err, transferred, nil)
			return err
		}

		if hdr.IsDelta {
			var manifest FileChunkManifest
			if err := readFrame(stream, &manifest, 2<<20); err != nil {
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				a.finishSync(id, err, transferred, nil)
				return err
			}
			needed, err := FindNeededChunks(targetFullPath, &manifest)
			if err != nil {
				needed = make([]int, len(manifest.Chunks))
				for i := range manifest.Chunks {
					needed[i] = i
				}
			}
			if err := writeFrame(stream, mustJSON(NeededChunksReply{Needed: needed})); err != nil {
				a.finishSync(id, err, transferred, nil)
				return err
			}

			chunkMap := make(map[int][]byte, len(needed))
			for i := 0; i < len(needed); i++ {
				var cf ChunkFrame
				if err := readFrame(stream, &cf, 16<<10); err != nil {
					a.finishSync(id, err, transferred, nil)
					return err
				}
				buf := make([]byte, cf.Size)
				if _, err := io.ReadFull(stream, buf); err != nil {
					a.finishSync(id, err, transferred, nil)
					return err
				}
				chunkMap[cf.Index] = buf
				transferred += cf.Size
			}

			tmpPath := targetFullPath + ".part-" + fmt.Sprintf("%d", time.Now().UnixNano())
			err = AssemblePatchedFile(tmpPath, targetFullPath, &manifest, func(idx int) ([]byte, error) {
				data, ok := chunkMap[idx]
				if !ok {
					return nil, errors.New("missing chunk")
				}
				return data, nil
			})
			if err != nil {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				a.finishSync(id, err, transferred, nil)
				return err
			}
			if err := os.Rename(tmpPath, targetFullPath); err != nil {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				a.finishSync(id, err, transferred, nil)
				return err
			}
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(targetFullPath, hdr.ModTime, hdr.ModTime)
			}
		} else {
			tmpPath := targetFullPath + ".part-" + fmt.Sprintf("%d", time.Now().UnixNano())
			tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				a.finishSync(id, err, transferred, nil)
				return err
			}
			hasher := sha256.New()
			n, copyErr := io.CopyN(io.MultiWriter(tmp, hasher), stream, hdr.Size)
			_ = tmp.Sync()
			_ = tmp.Close()
			if copyErr != nil || n != hdr.Size {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: "read failure"}))
				a.finishSync(id, copyErr, transferred, nil)
				return copyErr
			}
			transferred += n
			if err := os.Rename(tmpPath, targetFullPath); err != nil {
				_ = os.Remove(tmpPath)
				_ = writeFrame(stream, mustJSON(TransferAck{OK: false, Error: err.Error()}))
				a.finishSync(id, err, transferred, nil)
				return err
			}
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(targetFullPath, hdr.ModTime, hdr.ModTime)
			}
		}

		_ = writeFrame(stream, mustJSON(TransferAck{OK: true}))
	}

	// 10. Send deletions for peer to apply
	_ = writeFrame(stream, mustJSON(map[string]any{"deletions": toDeleteRemote}))
	var delAck TransferAck
	_ = readFrame(stream, &delAck, 16<<10)

	// 11. Apply local deletions
	for _, item := range toDeleteLocal {
		fullPath := filepath.Join(folder.Path, filepath.FromSlash(item.Path))
		if _, err := os.Stat(fullPath); err == nil {
			_, _ = a.main.trash.MoveToTrash(id, item.Path, fullPath)
		}
	}

	// 12. Re-scan and complete
	updatedFiles, newBytes, _ := scanFolder(folder.Path)
	newManifest := make(map[string]string, len(updatedFiles))
	for p, f := range updatedFiles {
		newManifest[p] = f.SHA256
	}

	a.mu.Lock()
	if current := a.folders[id]; current != nil {
		current.syncing = false
		current.cancel = nil
		current.manifest = newManifest
		current.Folder.Files = len(updatedFiles)
		current.Folder.Bytes = newBytes
		current.Folder.Progress = 100
		current.Folder.Status = "synced"
		current.Folder.LastSync = time.Now().UTC()
		current.Folder.Error = ""
		current.Folder.Conflicts = len(a.main.conflicts.List(id))
		changedCount := len(toSend) + len(toFetch) + len(conflicts) + len(toDeleteLocal)
		if changedCount > 0 {
			a.addActivityLocked("sync", "Folder synchronized", fmt.Sprintf("Synchronized %d item(s) with %s.", changedCount, reply.ReceiverName))
		}
	}
	a.transferredBytes += transferred
	_ = a.persistLocked()
	a.mu.Unlock()

	return nil
}

// syncFolderLegacy is the fallback single-file transfer for older peers.
func (a *App) syncFolderLegacy(syncCtx context.Context, id string, folder Folder, files map[string]fileDescriptor, previous map[string]string) error {
	peerID, _ := parsePeerID(folder.PeerID)
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
	sortedStrings(paths)
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
		n, transferErr := a.main.TransferFile(network.WithAllowLimitedConn(syncCtx, "loom folder sync"), peerID, id, desc, input, folder.Name)
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
		current.Folder.Bytes = folder.Bytes
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
