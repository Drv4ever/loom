package loom

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func (a *App) Handler(apiListen string) http.Handler {
	if apiListen == "" {
		apiListen = "127.0.0.1:8787"
	}
	_, apiPort, err := net.SplitHostPort(apiListen)
	if err != nil {
		apiPort = "8787"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("GET /api/received/{id}/files/{path...}", a.handleReceivedFile)
	mux.HandleFunc("POST /api/peers", a.handleJoin)
	mux.HandleFunc("POST /api/pairing/generate", a.handlePairingGenerate)
	mux.HandleFunc("GET /api/pairing", a.handlePairingStatus)
	mux.HandleFunc("POST /api/pairing/claim", a.handlePairingClaim)
	mux.HandleFunc("GET /api/devices/blocked", a.handleListBlockedDevices)
	mux.HandleFunc("POST /api/devices/{id}/block", a.handleBlockDevice)
	mux.HandleFunc("POST /api/devices/{id}/unblock", a.handleUnblockDevice)
	mux.HandleFunc("POST /api/services", a.handleAddService)
	mux.HandleFunc("DELETE /api/services/{id}", a.handleDeleteService)
	mux.HandleFunc("POST /api/folders", a.handleAddFolder)
	mux.HandleFunc("POST /api/folders/{id}/sync", a.handleSyncFolder)
	mux.HandleFunc("POST /api/folders/{id}/pause", a.handlePauseFolder)
	mux.HandleFunc("DELETE /api/folders/{id}", a.handleDeleteFolder)
	mux.HandleFunc("GET /api/folders/{id}/conflicts", a.handleGetConflicts)
	mux.HandleFunc("POST /api/folders/{id}/resolve", a.handleResolveConflict)
	mux.HandleFunc("GET /api/folders/{id}/trash", a.handleGetTrash)
	mux.HandleFunc("GET /api/forwards", a.handleListForwards)
	mux.HandleFunc("POST /api/forwards", a.handleStartForward)
	mux.HandleFunc("DELETE /api/forwards/{id}", a.handleStopForward)
	mux.HandleFunc("GET /api/relays", a.handleListRelays)
	mux.HandleFunc("POST /api/relays", a.handleAddRelay)
	mux.HandleFunc("DELETE /api/relays/{id}", a.handleDeleteRelay)
	mux.HandleFunc("DELETE /api/relays", a.handleDeleteRelay)
	mux.HandleFunc("POST /api/network", a.handleNetwork)
	mux.HandleFunc("GET /api/diagnostics", a.handleDiagnostics)
	mux.HandleFunc("/proxy/", a.handleProxy)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validRequestHost(r.Host, apiPort) {
			writeError(w, http.StatusMisdirectedRequest, "request host must be localhost")
			return
		}
		if isMutation(r.Method) && !strings.HasPrefix(r.URL.Path, "/proxy/") {
			if !validRequestOrigin(r.Header.Get("Origin"), apiPort) {
				writeError(w, http.StatusForbidden, "request origin is not allowed")
				return
			}
			if r.Method != http.MethodDelete && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func validRequestHost(raw, apiPort string) bool {
	host := raw
	port := ""
	if h, p, err := net.SplitHostPort(raw); err == nil {
		host = h
		port = p
	}
	if !isLoopbackHost(host) {
		return false
	}
	if port == "" || apiPort == "" || port == apiPort || port == "3000" {
		return true
	}
	return false
}

func validRequestOrigin(raw, apiPort string) bool {
	if raw == "" {
		// Local command-line clients have no Origin header. The API is bound to loopback.
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" || !isLoopbackHost(u.Hostname()) {
		return false
	}
	port := u.Port()
	return port == apiPort || port == "3000"
}

func (a *App) handleState(w http.ResponseWriter, _ *http.Request) {
	state := a.State()
	if a.main != nil {
		state.Network.PeerID = a.main.Host.ID().String()
		state.Network.Addresses = a.main.Addrs()
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) handleJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := parseJoinAddress(body.Address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.joinAddress(body.Address); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.refreshPeers()
	for _, device := range a.State().Devices {
		if device.Online && !device.Self {
			writeJSON(w, http.StatusOK, device)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleAddService(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string   `json:"name"`
		Target       string   `json:"target"`
		Trusted      bool     `json:"trusted"`
		AllowedPeers []string `json:"allowedPeers,omitempty"`
	}
	if err := decodeBody(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Name == "" || len(body.Name) > 120 {
		writeError(w, http.StatusBadRequest, "service name is required and must be under 120 characters")
		return
	}
	if _, err := validateLoopbackHTTPURL(body.Target); err != nil {
		writeError(w, http.StatusBadRequest, "target must be an HTTP URL using localhost or a loopback IP")
		return
	}
	id, err := randomSuffix()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create service id")
		return
	}
	record := ServiceRecord{
		ID:           id,
		Name:         body.Name,
		Target:       strings.TrimRight(body.Target, "/"),
		Trusted:      body.Trusted,
		AllowedPeers: body.AllowedPeers,
	}
	a.mu.Lock()
	if !a.connected {
		a.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "network is paused")
		return
	}
	a.services[record.ID] = record
	a.main.SetService(record)
	a.addActivityLocked("service", "Service published", body.Name+" is available to private network peers.")
	if err := a.persistLocked(); err != nil {
		delete(a.services, record.ID)
		a.main.DeleteService(record.ID)
		a.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "could not save service")
		return
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusCreated, a.serviceView(a.main, record))
}

func (a *App) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.mu.Lock()
	service, ok := a.services[id]
	if !ok {
		a.mu.Unlock()
		writeError(w, http.StatusNotFound, "locally published service was not found")
		return
	}
	delete(a.services, id)
	a.main.DeleteService(id)
	a.addActivityLocked("service", "Service removed", service.Name+" is no longer published.")
	if err := a.persistLocked(); err != nil {
		a.services[id] = service
		a.main.SetService(service)
		a.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "could not save service change")
		return
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleAddFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path         string   `json:"path"`
		PeerID       string   `json:"peerId"`
		Name         string   `json:"name"`
		AllowedPeers []string `json:"allowedPeers,omitempty"`
		ReadOnly     bool     `json:"readOnly,omitempty"`
	}
	if err := decodeBody(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	folder, err := a.addFolderWithOptions(body.Path, body.PeerID, body.Name, body.AllowedPeers, body.ReadOnly)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, folder)
}

func (a *App) handleSyncFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.syncFolder(r.Context(), id); err != nil {
		writeMutationError(w, err)
		return
	}
	a.mu.RLock()
	entry := a.folders[id]
	var folder Folder
	if entry != nil {
		folder = entry.Folder
	}
	a.mu.RUnlock()
	writeJSON(w, http.StatusOK, folder)
}

func (a *App) handlePauseFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paused bool `json:"paused"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	folder, err := a.setFolderPaused(r.PathValue("id"), body.Paused)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, folder)
}

func (a *App) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	if err := a.removeFolder(r.PathValue("id")); err != nil {
		writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleGetConflicts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.main == nil || a.main.conflicts == nil {
		writeJSON(w, http.StatusOK, []Conflict{})
		return
	}
	conflicts := a.main.conflicts.List(id)
	if conflicts == nil {
		conflicts = []Conflict{}
	}
	writeJSON(w, http.StatusOK, conflicts)
}

func (a *App) handleResolveConflict(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		ConflictID string `json:"conflictId"`
		Resolution string `json:"resolution"` // "local", "remote", "both"
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ConflictID == "" {
		writeError(w, http.StatusBadRequest, "conflictId is required")
		return
	}
	res := ConflictResolution(strings.ToLower(strings.TrimSpace(body.Resolution)))
	if res != ResolutionKeepLocal && res != ResolutionKeepRemote && res != ResolutionKeepBoth {
		writeError(w, http.StatusBadRequest, "resolution must be 'local', 'remote', or 'both'")
		return
	}

	a.mu.RLock()
	entry := a.folders[id]
	a.mu.RUnlock()
	if entry == nil {
		writeError(w, http.StatusNotFound, "folder not found")
		return
	}

	if err := a.main.conflicts.Resolve(entry.Folder.Path, id, body.ConflictID, res); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	a.mu.Lock()
	if current := a.folders[id]; current != nil {
		current.Folder.Conflicts = len(a.main.conflicts.List(id))
		_ = a.persistLocked()
	}
	a.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleGetTrash(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.main == nil || a.main.trash == nil {
		writeJSON(w, http.StatusOK, []TrashEntry{})
		return
	}
	entries, err := a.main.trash.ListTrash(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []TrashEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (a *App) handleNetwork(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Connected bool `json:"connected"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.mu.Lock()
	if a.connected == body.Connected {
		a.mu.Unlock()
		state := a.State()
		writeJSON(w, http.StatusOK, state.Network)
		return
	}
	a.connected = body.Connected
	a.main.SetEnabled(body.Connected)
	peerAddrs := make([]peer.AddrInfo, 0, len(a.peers))
	peerIDs := make([]peer.ID, 0, len(a.peers))
	for _, entry := range a.peers {
		if entry.Info.ID != "" {
			info := entry.Info
			if relayInfo, ok := a.relayInfos[info.ID.String()]; ok && len(relayInfo.Addrs) != 0 {
				info = relayInfo
			}
			peerAddrs = append(peerAddrs, info)
			peerIDs = append(peerIDs, entry.Info.ID)
		}
		if !body.Connected {
			entry.Device.Online = false
			entry.Services = nil
			a.updateFoldersForPeerLocked(entry.Device.ID, false)
		}
	}
	if !body.Connected {
		for _, entry := range a.folders {
			if entry.cancel != nil {
				entry.cancel()
			}
		}
		a.addActivityLocked("system", "Network paused", "Application peer connections were closed.")
	} else {
		a.addActivityLocked("system", "Network resumed", "Reconnecting to known devices.")
	}
	_ = a.persistLocked()
	a.mu.Unlock()
	if !body.Connected {
		for _, id := range peerIDs {
			_ = a.main.Host.Network().ClosePeer(id)
		}
	} else {
		for _, info := range peerAddrs {
			if a.forceRelay[info.ID.String()] && len(info.Addrs) > 0 {
				a.main.Host.Peerstore().ClearAddrs(info.ID)
				a.main.Host.Peerstore().SetAddrs(info.ID, info.Addrs, 24*time.Hour)
			}
			ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
			if a.forceRelay[info.ID.String()] {
				ctx = network.WithAllowLimitedConn(ctx, "loom forced relay connection")
			}
			if err := a.main.Host.Connect(ctx, info); err == nil && a.forceRelay[info.ID.String()] {
				a.main.Host.Peerstore().ClearAddrs(info.ID)
				a.main.Host.Peerstore().SetAddrs(info.ID, info.Addrs, 24*time.Hour)
			}
			cancel()
		}
		a.refreshPeers()
	}
	state := a.State()
	writeJSON(w, http.StatusOK, state.Network)
}

func (a *App) handleDiagnostics(w http.ResponseWriter, _ *http.Request) {
	state := a.State()
	diagnostics := map[string]any{
		"ok":             true,
		"version":        Version,
		"uptimeSeconds":  int64(time.Since(a.started).Seconds()),
		"network":        map[string]any{"connected": state.Network.Connected, "peerId": state.Network.PeerID, "addresses": state.Network.Addresses, "private": true},
		"devices":        state.Devices,
		"folderStatuses": folderDiagnostic(state.Folders),
		"limits":         map[string]int64{"fileBytes": maxFileSize, "proxyRequestBytes": maxProxyBody, "proxyResponseBytes": maxProxyReply},
	}
	writeJSON(w, http.StatusOK, diagnostics)
}

func folderDiagnostic(folders []Folder) []map[string]any {
	result := make([]map[string]any, 0, len(folders))
	for _, folder := range folders {
		result = append(result, map[string]any{"id": folder.ID, "status": folder.Status, "paused": folder.Paused, "files": folder.Files, "bytes": folder.Bytes, "error": folder.Error})
	}
	return result
}

func (a *App) handleProxy(w http.ResponseWriter, r *http.Request) {
	peerID, serviceID, tail, err := parseProxyPath(r.URL)
	if err != nil {
		writeError(w, http.StatusNotFound, "published service was not found")
		return
	}
	a.mu.RLock()
	connected := a.connected
	var record ServiceRecord
	var found bool
	if a.main != nil && peerID == a.main.Host.ID().String() {
		record, found = a.services[serviceID]
	} else if entry := a.peers[peerID]; entry != nil && entry.Device.Online {
		for _, service := range entry.Services {
			if service.ID == serviceID {
				record, found = service, true
				break
			}
		}
	}
	a.mu.RUnlock()
	if !connected {
		writeError(w, http.StatusServiceUnavailable, "network is paused")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "published service was not found")
		return
	}
	if isWebSocketUpgrade(r) {
		if peerID == a.main.Host.ID().String() {
			a.proxyLocalWebSocket(w, r, tail, record)
			return
		}
		id, err := peer.Decode(peerID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid peer id")
			return
		}
		stream, err := a.main.Host.NewStream(network.WithAllowLimitedConn(r.Context(), "loom WS proxy"), id, httpProtocol)
		if err != nil {
			writeError(w, http.StatusBadGateway, "service peer is unavailable")
			return
		}
		a.proxyOverStreamWebSocket(w, r, stream, serviceID, tail, record)
		return
	}
	if peerID == a.main.Host.ID().String() {
		a.proxyLocal(w, r, serviceID, tail, record)
		return
	}
	id, err := peer.Decode(peerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid peer id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	stream, err := a.main.Host.NewStream(network.WithAllowLimitedConn(ctx, "loom HTTP proxy"), id, httpProtocol)
	if err != nil {
		writeError(w, http.StatusBadGateway, "service peer is unavailable")
		return
	}
	a.proxyOverStream(w, r, stream, serviceID, tail, record)
}

func parseProxyPath(u *url.URL) (peerID, serviceID, tail string, err error) {
	if u == nil || strings.Contains(u.EscapedPath(), "\\") {
		return "", "", "", fmt.Errorf("invalid path")
	}
	parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/proxy/"), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("missing id")
	}
	peerID, err = url.PathUnescape(parts[0])
	if err != nil {
		return "", "", "", err
	}
	serviceID, err = url.PathUnescape(parts[1])
	if err != nil {
		return "", "", "", err
	}
	if _, err := peer.Decode(peerID); err != nil || !isID(serviceID) {
		return "", "", "", fmt.Errorf("invalid id")
	}
	if len(parts) == 3 {
		tail, err = safeServicePath(&url.URL{Path: "/" + parts[2], RawPath: "/" + parts[2]})
		if err != nil {
			return "", "", "", err
		}
	}
	return peerID, serviceID, tail, nil
}

func (a *App) proxyLocal(w http.ResponseWriter, r *http.Request, serviceID, tail string, record ServiceRecord) {
	target, err := validateLoopbackHTTPURL(record.Target)
	if err != nil {
		writeError(w, http.StatusBadGateway, "published service target is invalid")
		return
	}
	basePath := strings.TrimRight(target.Path, "/")
	if tail == "" {
		if basePath == "" {
			target.Path = "/"
		} else {
			target.Path = basePath
		}
	} else {
		if basePath == "" {
			target.Path = "/"
		}
		target.Path = path.Join(basePath, tail)
	}
	target.RawQuery = r.URL.RawQuery
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid proxy request")
		return
	}
	request.ContentLength = r.ContentLength
	copyEndToEndHeaders(request.Header, r.Header)
	if !record.Trusted {
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		request.Header.Del("Origin")
		request.Header.Del("Referer")
	}
	request.Host = target.Host
	client := &http.Client{
		Timeout:       60 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "published service is unavailable")
		return
	}
	defer response.Body.Close()
	writeProxyResponseStream(w, response, record.Target, r.URL.Path, record.Trusted)
}

func (a *App) proxyOverStream(w http.ResponseWriter, r *http.Request, stream network.Stream, serviceID, tail string, record ServiceRecord) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(60 * time.Second))
	requestURL := &url.URL{Scheme: "http", Host: "loom.invalid", Path: "/" + tail, RawQuery: r.URL.RawQuery}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, requestURL.String(), r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid proxy request")
		return
	}
	request.ContentLength = r.ContentLength
	copyEndToEndHeaders(request.Header, r.Header)
	if !record.Trusted {
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		request.Header.Del("Origin")
		request.Header.Del("Referer")
	}
	request.Header.Set("X-Loom-Service-ID", serviceID)
	request.Host = "loom.invalid"
	if err := request.Write(stream); err != nil {
		writeError(w, http.StatusBadGateway, "could not send request to service peer")
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(stream), request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "service peer returned an invalid response")
		return
	}
	defer response.Body.Close()
	writeProxyResponseStream(w, response, record.Target, r.URL.Path, record.Trusted)
}

func writeProxyResponseStream(w http.ResponseWriter, response *http.Response, target, requestPath string, trusted bool) {
	if location := response.Header.Get("Location"); location != "" {
		rewritten, ok := proxyLocation(location, target, requestPath)
		if !ok {
			writeError(w, http.StatusBadGateway, "service redirect was blocked because it left the published loopback service")
			return
		}
		response.Header.Set("Location", rewritten)
	}
	clean := make(http.Header)
	copyEndToEndHeaders(clean, response.Header)
	response.Header = clean
	if !trusted {
		response.Header.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups; frame-ancestors 'none'")
		response.Header.Del("Access-Control-Allow-Origin")
		response.Header.Del("Access-Control-Allow-Credentials")
	}
	response.Header.Set("X-Content-Type-Options", "nosniff")
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *App) proxyLocalWebSocket(w http.ResponseWriter, r *http.Request, tail string, record ServiceRecord) {
	target, err := validateLoopbackHTTPURL(record.Target)
	if err != nil {
		writeError(w, http.StatusBadGateway, "published service target is invalid")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "webserver does not support hijacking")
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer clientConn.Close()

	targetConn, err := net.DialTimeout("tcp", target.Host, 5*time.Second)
	if err != nil {
		return
	}
	defer targetConn.Close()

	outReq := r.Clone(r.Context())
	basePath := strings.TrimRight(target.Path, "/")
	if tail == "" {
		if basePath == "" {
			outReq.URL.Path = "/"
		} else {
			outReq.URL.Path = basePath
		}
	} else {
		if basePath == "" {
			outReq.URL.Path = "/" + tail
		} else {
			outReq.URL.Path = path.Join(basePath, tail)
		}
	}
	outReq.Host = target.Host

	if err := outReq.Write(targetConn); err != nil {
		return
	}

	bridgeConns(clientConn, targetConn)
}

func (a *App) proxyOverStreamWebSocket(w http.ResponseWriter, r *http.Request, stream network.Stream, serviceID, tail string, record ServiceRecord) {
	defer stream.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "webserver does not support hijacking")
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer clientConn.Close()

	outReq := r.Clone(r.Context())
	outReq.URL.Path = "/" + tail
	outReq.Host = "loom.invalid"
	outReq.Header.Set("X-Loom-Service-ID", serviceID)

	if err := outReq.Write(stream); err != nil {
		return
	}

	bridgeConns(clientConn, stream)
}

func (a *App) handleListForwards(w http.ResponseWriter, r *http.Request) {
	if a.forwards == nil {
		writeJSON(w, http.StatusOK, []*Forward{})
		return
	}
	writeJSON(w, http.StatusOK, a.forwards.ListForwards())
}

func (a *App) handleStartForward(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PeerID    string `json:"peerId"`
		ServiceID string `json:"serviceId"`
		Target    string `json:"target"`
		LocalPort int    `json:"localPort"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if a.forwards == nil {
		writeError(w, http.StatusInternalServerError, "forward manager is not available")
		return
	}
	fwd, err := a.forwards.StartForward(body.PeerID, body.ServiceID, body.Target, body.LocalPort)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	a.mu.Lock()
	a.addActivityLocked("service", "Port forward started", fmt.Sprintf("127.0.0.1:%d forwarding to %s.", fwd.LocalPort, fwd.PeerName))
	a.mu.Unlock()
	writeJSON(w, http.StatusCreated, fwd)
}

func (a *App) handleStopForward(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.forwards == nil {
		writeError(w, http.StatusInternalServerError, "forward manager is not available")
		return
	}
	if err := a.forwards.StopForward(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleListRelays(w http.ResponseWriter, _ *http.Request) {
	relays := a.ListRelays()
	writeJSON(w, http.StatusOK, relays)
}

func (a *App) handleAddRelay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Address = strings.TrimSpace(body.Address)
	if body.Address == "" {
		writeError(w, http.StatusBadRequest, "relay address is required")
		return
	}
	if err := a.AddRelay(body.Address); err != nil {
		writeMutationError(w, err)
		return
	}
	addr, _ := ma.NewMultiaddr(body.Address)
	info, _ := peer.AddrInfoFromP2pAddr(addr)
	relayInfo := RelayInfo{
		Address: body.Address,
		PeerID:  info.ID.String(),
		Status:  "configured",
	}
	if a.main != nil && a.main.Host.Network().Connectedness(info.ID) == network.Connected {
		relayInfo.Status = "connected"
	}
	writeJSON(w, http.StatusCreated, relayInfo)
}

func (a *App) handleDeleteRelay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		var body struct {
			Address string `json:"address"`
			ID      string `json:"id"`
		}
		if err := decodeBody(w, r, &body, 16<<10); err == nil {
			if body.Address != "" {
				id = body.Address
			} else if body.ID != "" {
				id = body.ID
			}
		}
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "relay id or address is required")
		return
	}
	decoded, err := url.PathUnescape(id)
	if err == nil && decoded != "" {
		id = decoded
	}
	if err := a.RemoveRelay(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func proxyLocation(location, target, requestPath string) (string, bool) {
	targetURL, err := validateLoopbackHTTPURL(target)
	if err != nil {
		return "", false
	}
	base, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	if base.IsAbs() || base.Host != "" {
		resolved := targetURL.ResolveReference(base)
		if resolved.Scheme != "http" || !strings.EqualFold(resolved.Host, targetURL.Host) {
			return "", false
		}
		base = resolved
	}
	pathPart := base.EscapedPath()
	if strings.Contains(pathPart, "\\") {
		return "", false
	}
	clean, err := safeServicePath(&url.URL{Path: "/" + strings.TrimPrefix(pathPart, "/"), RawPath: "/" + strings.TrimPrefix(pathPart, "/")})
	if err != nil {
		return "", false
	}
	// Relative redirects stay under the current service route.
	if !strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "http://") {
		return location, true
	}
	basePath := strings.TrimRight(targetURL.Path, "/")
	if basePath != "" && strings.HasPrefix("/"+clean, basePath+"/") {
		clean = strings.TrimPrefix("/"+clean, basePath+"/")
	} else if basePath != "" && "/"+clean == basePath {
		clean = ""
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, "/proxy/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	prefix := "/proxy/" + parts[0] + "/" + parts[1] + "/"
	if clean != "" {
		prefix += path.Clean(clean)
	}
	return prefix + querySuffix(base), true
}

func querySuffix(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

func (a *App) handleNetworkConnection(connected bool) {
	_ = connected
}

func parseJoinAddress(raw string) (*peer.AddrInfo, error) {
	addr, err := ma.NewMultiaddr(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid multiaddress")
	}
	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("address must end in /p2p/<peer-id>")
	}
	return info, nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}

func writeMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errFolderPaused):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errFolderBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errNetworkPaused):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusConflict, "operation was paused or canceled")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "operation timed out")
	case errors.Is(err, io.EOF):
		writeError(w, http.StatusNotFound, "item was not found")
	default:
		if errors.Is(err, net.ErrClosed) {
			writeError(w, http.StatusServiceUnavailable, "network connection closed")
		} else if strings.Contains(strings.ToLower(err.Error()), "not exist") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (a *App) handlePairingGenerate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DurationSeconds int `json:"durationSeconds,omitempty"`
	}
	if r.Body != nil {
		_ = decodeBody(w, r, &body, 16<<10)
	}
	ttl := 10 * time.Minute
	if body.DurationSeconds > 0 {
		ttl = time.Duration(body.DurationSeconds) * time.Second
	}
	session, err := a.GeneratePairingCode(ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (a *App) handlePairingStatus(w http.ResponseWriter, _ *http.Request) {
	session := a.GetPairingSession()
	if session == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":  true,
		"session": session,
	})
}

func (a *App) handlePairingClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PIN   string `json:"pin"`
		Relay string `json:"relay,omitempty"`
	}
	if err := decodeBody(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.PIN == "" {
		writeError(w, http.StatusBadRequest, "pairing PIN is required")
		return
	}
	payload, err := a.ClaimPairingCode(body.PIN, body.Relay)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleListBlockedDevices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"blocked": a.ListBlockedPeers(),
	})
}

func (a *App) handleBlockDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "device id is required")
		return
	}
	if err := a.BlockPeer(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleUnblockDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "device id is required")
		return
	}
	if err := a.UnblockPeer(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
