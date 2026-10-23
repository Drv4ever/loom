package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Version 3 requires receiver consent and validates every operation against the handshake.
const syncProtocol = protocol.ID("/loom/sync/3.0.0")
const maxSyncFiles = 10000

const modeTwoWay = "two_way"
const modeSendOnly = "send_only"

type SyncHello struct {
	FolderID     string                    `json:"folderId"`
	FolderName   string                    `json:"folderName"`
	SenderName   string                    `json:"senderName"`
	BlockedPaths []string                  `json:"blockedPaths,omitempty"`
	Mode         string                    `json:"mode"`
	Manifest     map[string]fileDescriptor `json:"manifest"`
	Tombstones   map[string]time.Time      `json:"tombstones"`
	Baseline     map[string]string         `json:"baseline"`
}
type SyncHelloReply struct {
	OK           bool                      `json:"ok"`
	Error        string                    `json:"error,omitempty"`
	Status       string                    `json:"status,omitempty"`
	ReceiverName string                    `json:"receiverName"`
	Destination  string                    `json:"destination"`
	Manifest     map[string]fileDescriptor `json:"manifest"`
	Tombstones   map[string]time.Time      `json:"tombstones"`
}
type SyncPlanItem struct {
	Path         string
	Action       string
	SenderDesc   fileDescriptor
	ReceiverDesc fileDescriptor
}
type TransferHeader struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	SHA256  string    `json:"sha256"`
	IsDelta bool      `json:"isDelta"`
	End     bool      `json:"end"`
}
type TransferAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
type NeededChunksReply struct {
	Needed []int `json:"needed"`
}
type ChunkFrame struct {
	Index int   `json:"index"`
	Size  int64 `json:"size"`
	Last  bool  `json:"last"`
}
type syncResponseError struct{ status, message string }

func (e *syncResponseError) Error() string { return e.message }

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
			if remDesc.SHA256 == baseHash && baseHash != "" {
				plan = append(plan, SyncPlanItem{Path: p, Action: "delete_remote", ReceiverDesc: remDesc})
			} else {
				plan = append(plan, SyncPlanItem{Path: p, Action: "fetch", ReceiverDesc: remDesc})
			}
			continue
		}
		if !remDeleted.IsZero() && hasLoc {
			if locDesc.SHA256 == baseHash && baseHash != "" {
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

func reconciliationPlan(local map[string]fileDescriptor, localDeleted map[string]time.Time, remote map[string]fileDescriptor, remoteDeleted map[string]time.Time, baseline map[string]string, mode string) []SyncPlanItem {
	if mode != modeSendOnly {
		return computeSyncPlan(local, localDeleted, remote, remoteDeleted, baseline)
	}
	var plan []SyncPlanItem
	for p, f := range local {
		if r, ok := remote[p]; !ok || r.SHA256 != f.SHA256 {
			plan = append(plan, SyncPlanItem{Path: p, Action: "send", SenderDesc: f, ReceiverDesc: r})
		}
	}
	for p := range localDeleted {
		if r, ok := remote[p]; ok {
			plan = append(plan, SyncPlanItem{Path: p, Action: "delete_remote", ReceiverDesc: r})
		}
	}
	return plan
}

func freezeConflicts(plan []SyncPlanItem, blocked []string) []SyncPlanItem {
	frozen := make(map[string]bool, len(blocked))
	for _, p := range blocked {
		frozen[p] = true
	}
	result := make([]SyncPlanItem, 0, len(plan))
	for _, item := range plan {
		if !frozen[item.Path] {
			result = append(result, item)
		}
	}
	return result
}

func detectTombstones(files map[string]fileDescriptor, previous map[string]string, deleted map[string]time.Time) map[string]time.Time {
	result := make(map[string]time.Time)
	for p, t := range deleted {
		if _, exists := files[p]; !exists {
			result[p] = t
		}
	}
	for p := range previous {
		if _, exists := files[p]; !exists && result[p].IsZero() {
			result[p] = time.Now().UTC()
		}
	}
	return result
}

func (n *Node) getFolderDirectory(id string) string {
	if n.getCustomPath != nil {
		if p := n.getCustomPath(id); p != "" {
			return p
		}
	}
	n.mu.RLock()
	folder := n.received[id]
	n.mu.RUnlock()
	if folder.Path != "" {
		return folder.Path
	}
	return filepath.Join(n.ReceiveDir, id)
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

func (n *Node) prepareIncoming(hello SyncHello, sender string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	prior, exists := n.received[hello.FolderID]
	if exists && prior.PeerID != sender {
		return "", fmt.Errorf("folder belongs to a different sender")
	}
	if !exists {
		if len(n.received) >= 1000 {
			return "", fmt.Errorf("too many incoming folders")
		}
		prior = ReceivedFolder{ID: hello.FolderID, Name: hello.FolderName, PeerID: sender, PeerName: hello.SenderName, Path: filepath.Join(n.ReceiveDir, hello.FolderID), Status: "pending", Mode: hello.Mode, Files: []fileDescriptor{}}
		if !n.requireAcceptance {
			prior.Status = "accepted"
		}
		n.received[hello.FolderID] = prior
		if err := n.saveReceivedLocked(); err != nil {
			delete(n.received, hello.FolderID)
			return "", err
		}
	}
	if prior.Status != "accepted" {
		return "", &syncResponseError{status: prior.Status, message: "receiver must accept this folder before synchronization (" + prior.Status + ")"}
	}
	if prior.Mode != "" && prior.Mode != hello.Mode {
		return "", fmt.Errorf("sync mode differs from the accepted folder")
	}
	return prior.Path, nil
}

func (n *Node) handleBidirectionalSync(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Minute))
	reject := func(err error) {
		reply := SyncHelloReply{Error: err.Error()}
		var se *syncResponseError
		if errors.As(err, &se) {
			reply.Status = se.status
		}
		_ = writeFrame(stream, mustJSON(reply))
	}
	if !n.enabled.Load() {
		reject(errNetworkPaused)
		return
	}
	var hello SyncHello
	if err := readFrame(stream, &hello, 8<<20); err != nil {
		return
	}
	if !isID(hello.FolderID) || len(hello.FolderName) > 120 || len(hello.SenderName) > 120 || (hello.Mode != modeTwoWay && hello.Mode != modeSendOnly) {
		reject(fmt.Errorf("invalid sync hello"))
		return
	}
	if len(hello.BlockedPaths) > maxSyncFiles {
		reject(fmt.Errorf("too many blocked paths"))
		return
	}
	for _, p := range hello.BlockedPaths {
		if !validRelativePath(p) {
			reject(fmt.Errorf("invalid blocked path"))
			return
		}
	}
	if err := validateManifest(hello.Manifest, hello.Tombstones, hello.Baseline); err != nil {
		reject(err)
		return
	}
	sender := stream.Conn().RemotePeer().String()
	n.receiveMu.Lock()
	defer n.receiveMu.Unlock()
	if n.checkFolderAccess != nil {
		if err := n.checkFolderAccess(hello.FolderID, sender, true); err != nil {
			reject(err)
			return
		}
	}
	targetDir, err := n.prepareIncoming(hello, sender)
	if err != nil {
		reject(err)
		return
	}
	n.mu.Lock()
	n.activeSync[hello.FolderID] = stream
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.activeSync, hello.FolderID); n.mu.Unlock() }()
	if n.getCustomPath != nil {
		if p := n.getCustomPath(hello.FolderID); p != "" {
			targetDir = p
		}
	}
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		reject(err)
		return
	}
	root, err := os.OpenRoot(targetDir)
	if err != nil {
		reject(err)
		return
	}
	defer root.Close()
	local, _, err := scanFolder(targetDir)
	if err != nil {
		reject(err)
		return
	}
	n.mu.RLock()
	prior := n.received[hello.FolderID]
	n.mu.RUnlock()
	previous := make(map[string]string)
	for _, f := range prior.Files {
		previous[f.Path] = f.SHA256
	}
	deleted := detectTombstones(local, previous, prior.Tombstones)
	// Persist deletions before transferring, including when this session later fails.
	n.mu.Lock()
	prior.Tombstones = deleted
	n.received[hello.FolderID] = prior
	err = n.saveReceivedLocked()
	n.mu.Unlock()
	if err != nil {
		reject(err)
		return
	}
	if err := writeFrame(stream, mustJSON(SyncHelloReply{OK: true, ReceiverName: n.Name, Destination: targetDir, Manifest: local, Tombstones: deleted})); err != nil {
		return
	}
	plan := freezeConflicts(reconciliationPlan(hello.Manifest, hello.Tombstones, local, deleted, hello.Baseline, hello.Mode), hello.BlockedPaths)
	var transferred int64
	changes := 0
	incoming := make(map[string]fileDescriptor)
	fetchable := make(map[string]fileDescriptor)
	deletable := make(map[string]fileDescriptor)
	for _, item := range plan {
		switch item.Action {
		case "send":
			incoming[item.Path] = item.SenderDesc
		case "fetch", "conflict":
			fetchable[item.Path] = item.ReceiverDesc
		case "delete_remote":
			deletable[item.Path] = item.ReceiverDesc
		}
	}
	for {
		var hdr TransferHeader
		if err := readFrame(stream, &hdr, 64<<10); err != nil {
			return
		}
		if hdr.End {
			break
		}
		desc, ok := incoming[hdr.Path]
		if !ok || desc.Size != hdr.Size || desc.SHA256 != hdr.SHA256 {
			_ = writeFrame(stream, mustJSON(TransferAck{Error: "unexpected transfer"}))
			return
		}
		var expected *fileDescriptor
		if f, exists := local[hdr.Path]; exists {
			expected = &f
		}
		nbytes, err := receiveSyncFile(stream, root, hdr, hdr.Path, expected)
		if err != nil {
			return
		}
		transferred += nbytes
		changes++
		delete(incoming, hdr.Path)
	}
	if len(incoming) != 0 {
		return
	}
	var request struct {
		FetchPaths []string `json:"fetchPaths"`
	}
	if err := readFrame(stream, &request, 2<<20); err != nil {
		return
	}
	seen := make(map[string]bool)
	for _, p := range request.FetchPaths {
		desc, ok := fetchable[p]
		if !ok || seen[p] {
			return
		}
		seen[p] = true
		nbytes, err := sendSyncFile(stream, root, desc)
		if err != nil {
			return
		}
		transferred += nbytes
		changes++
	}
	if err := writeFrame(stream, mustJSON(TransferHeader{End: true})); err != nil {
		return
	}
	var delFrame struct {
		Deletions []string `json:"deletions"`
	}
	if err := readFrame(stream, &delFrame, 2<<20); err != nil {
		return
	}
	for _, p := range delFrame.Deletions {
		desc, ok := deletable[p]
		if !ok {
			_ = writeFrame(stream, mustJSON(TransferAck{Error: "unexpected deletion"}))
			return
		}
		if err := n.trash.archiveFromRoot(hello.FolderID, p, root, desc); err != nil {
			_ = writeFrame(stream, mustJSON(TransferAck{Error: err.Error()}))
			return
		}
		deleted[p] = time.Now().UTC()
		delete(deletable, p)
		changes++
	}
	if len(deletable) != 0 {
		return
	}
	files, bytes, err := scanFolder(targetDir)
	if err != nil {
		return
	}
	n.mu.Lock()
	folder := n.received[hello.FolderID]
	folder.Files = make([]fileDescriptor, 0, len(files))
	folder.Bytes = bytes
	folder.LastReceived = time.Now().UTC()
	for p, f := range files {
		folder.Files = append(folder.Files, f)
		delete(deleted, p)
	}
	folder.Tombstones = deleted
	n.received[hello.FolderID] = folder
	err = n.saveReceivedLocked()
	n.mu.Unlock()
	if err != nil {
		_ = writeFrame(stream, mustJSON(TransferAck{Error: err.Error()}))
		return
	}
	if n.onFolderSynced != nil {
		n.onFolderSynced(folder, transferred, changes)
	}
	_ = writeFrame(stream, mustJSON(TransferAck{OK: true}))
}

func (a *App) syncBidirectional(requestCtx context.Context, id string) (result error) {
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
	folder := entry.Folder
	peerID, err := parsePeerID(folder.PeerID)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	entry.syncing = true
	entry.Folder.Status = "syncing"
	ctx, cancel := context.WithCancel(a.ctx)
	entry.cancel = cancel
	baseline := make(map[string]string)
	for p, h := range entry.manifest {
		baseline[p] = h
	}
	oldDeleted := entry.tombstones
	a.mu.Unlock()
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			result = ctx.Err()
		}
		a.finishSync(id, result, 0, nil)
	}()
	if requestCtx != nil {
		stop := context.AfterFunc(requestCtx, cancel)
		defer stop()
	}
	local, _, err := scanFolder(folder.Path)
	if err != nil {
		return err
	}
	activeConflicts := a.main.conflicts.List(id)
	for _, c := range activeConflicts {
		delete(local, c.ConflictPath)
	}
	deleted := detectTombstones(local, baseline, oldDeleted)
	a.mu.Lock()
	entry.tombstones = deleted
	err = a.persistLocked()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	stream, err := a.main.Host.NewStream(network.WithAllowLimitedConn(ctx, "loom folder sync"), peerID, syncProtocol)
	if err != nil {
		return err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Minute))
	mode := folder.Mode
	if mode == "" {
		mode = modeTwoWay
		if folder.ReadOnly {
			mode = modeSendOnly
		}
	}
	blocked := make([]string, 0, len(activeConflicts))
	for _, c := range activeConflicts {
		blocked = append(blocked, c.Path)
	}
	hello := SyncHello{FolderID: id, FolderName: folder.Name, SenderName: a.main.Name, Mode: mode, Manifest: local, Tombstones: deleted, Baseline: baseline}
	hello.BlockedPaths = blocked
	if err := writeFrame(stream, mustJSON(hello)); err != nil {
		return err
	}
	var reply SyncHelloReply
	if err := readFrame(stream, &reply, 8<<20); err != nil {
		return err
	}
	if !reply.OK {
		return &syncResponseError{status: reply.Status, message: reply.Error}
	}
	if err := validateManifest(reply.Manifest, reply.Tombstones, nil); err != nil {
		return err
	}
	root, err := os.OpenRoot(folder.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	plan := freezeConflicts(reconciliationPlan(local, deleted, reply.Manifest, reply.Tombstones, baseline, mode), blocked)
	fetches := make(map[string]SyncPlanItem)
	conflictCopies := make(map[string]Conflict)
	var fetchPaths, remoteDeletes []string
	var localDeletes []SyncPlanItem
	var transferred int64
	for _, item := range plan {
		switch item.Action {
		case "send":
			n, err := sendSyncFile(stream, root, item.SenderDesc)
			transferred += n
			if err != nil {
				return err
			}
		case "fetch":
			fetches[item.Path] = item
			fetchPaths = append(fetchPaths, item.Path)
		case "conflict":
			alreadySaved := false
			for _, c := range activeConflicts {
				if c.Path == item.Path && c.RemoteSHA256 == item.ReceiverDesc.SHA256 {
					alreadySaved = true
					break
				}
			}
			if alreadySaved {
				continue
			}
			suffix, err := randomSuffix()
			if err != nil {
				return err
			}
			c := Conflict{ID: "conf-" + suffix, FolderID: id, Path: item.Path, ConflictPath: GenerateConflictPath(item.Path, time.Now().UTC(), reply.ReceiverName) + "-" + suffix, LocalSize: item.SenderDesc.Size, RemoteSize: item.ReceiverDesc.Size, LocalSHA256: item.SenderDesc.SHA256, RemoteSHA256: item.ReceiverDesc.SHA256, LocalModTime: item.SenderDesc.ModTime, RemoteModTime: item.ReceiverDesc.ModTime, DeviceName: reply.ReceiverName, CreatedAt: time.Now().UTC()}
			conflictCopies[item.Path] = c
			fetches[item.Path] = item
			fetchPaths = append(fetchPaths, item.Path)
		case "delete_local":
			localDeletes = append(localDeletes, item)
		case "delete_remote":
			remoteDeletes = append(remoteDeletes, item.Path)
		}
	}
	if err := writeFrame(stream, mustJSON(TransferHeader{End: true})); err != nil {
		return err
	}
	if err := writeFrame(stream, mustJSON(map[string]any{"fetchPaths": fetchPaths})); err != nil {
		return err
	}
	for {
		var hdr TransferHeader
		if err := readFrame(stream, &hdr, 64<<10); err != nil {
			return err
		}
		if hdr.End {
			break
		}
		item, ok := fetches[hdr.Path]
		if !ok || hdr.Size != item.ReceiverDesc.Size || hdr.SHA256 != item.ReceiverDesc.SHA256 {
			return fmt.Errorf("unexpected fetched file")
		}
		destination := hdr.Path
		var expected *fileDescriptor
		if c, conflict := conflictCopies[hdr.Path]; conflict {
			destination = c.ConflictPath
		} else if f, exists := local[hdr.Path]; exists {
			expected = &f
		}
		n, err := receiveSyncFile(stream, root, hdr, destination, expected)
		transferred += n
		if err != nil {
			return err
		}
		if c, conflict := conflictCopies[hdr.Path]; conflict {
			a.main.conflicts.Add(c)
			a.mu.Lock()
			err = a.persistLocked()
			a.mu.Unlock()
			if err != nil {
				return err
			}
		}
		delete(fetches, hdr.Path)
	}
	if len(fetches) > 0 {
		return fmt.Errorf("peer omitted requested files")
	}
	if err := writeFrame(stream, mustJSON(map[string]any{"deletions": remoteDeletes})); err != nil {
		return err
	}
	var ack TransferAck
	if err := readFrame(stream, &ack, 16<<10); err != nil {
		return err
	}
	if !ack.OK {
		return fmt.Errorf("peer failed sync: %s", ack.Error)
	}
	for _, item := range localDeletes {
		if err := a.main.trash.archiveFromRoot(id, item.Path, root, item.SenderDesc); err != nil {
			return err
		}
		deleted[item.Path] = time.Now().UTC()
	}
	// Advance only paths whose values are agreed, never an unresolved conflict or a concurrent local edit.
	next := make(map[string]string)
	for p, h := range baseline {
		next[p] = h
	}
	for p, f := range local {
		if remote, ok := reply.Manifest[p]; ok && f.SHA256 == remote.SHA256 && !containsPeer(blocked, p) {
			next[p] = f.SHA256
		}
	}
	for p := range baseline {
		_, hasLocal := local[p]
		_, hasRemote := reply.Manifest[p]
		if !hasLocal && !hasRemote && !containsPeer(blocked, p) {
			delete(next, p)
		}
	}
	for _, item := range plan {
		switch item.Action {
		case "send":
			next[item.Path] = item.SenderDesc.SHA256
		case "fetch":
			next[item.Path] = item.ReceiverDesc.SHA256
		case "delete_local", "delete_remote":
			delete(next, item.Path)
		}
	}
	updated, bytes, err := scanFolder(folder.Path)
	if err != nil {
		return err
	}
	for p := range updated {
		delete(deleted, p)
	}
	a.mu.Lock()
	if current := a.folders[id]; current != nil {
		current.manifest = next
		current.tombstones = deleted
		current.Folder.Files = len(updated)
		current.Folder.Bytes = bytes
		current.Folder.Progress = 100
		current.Folder.LastSync = time.Now().UTC()
		current.Folder.RemotePath = reply.Destination
		if transferred > 0 || len(localDeletes) > 0 || len(remoteDeletes) > 0 || len(conflictCopies) > 0 {
			a.addActivityLocked("sync", "Folder synchronized", fmt.Sprintf("Processed %d change(s) with %s.", len(plan), reply.ReceiverName))
		}
	}
	a.transferredBytes += transferred
	err = a.persistLocked()
	a.mu.Unlock()
	return err
}

func mustJSONPretty(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
