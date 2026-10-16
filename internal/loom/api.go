package loom

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
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
	mux.HandleFunc("POST /api/services", a.handleAddService)
	mux.HandleFunc("DELETE /api/services/{id}", a.handleDeleteService)
	mux.HandleFunc("POST /api/folders", a.handleAddFolder)
	mux.HandleFunc("POST /api/folders/{id}/sync", a.handleSyncFolder)
	mux.HandleFunc("POST /api/folders/{id}/pause", a.handlePauseFolder)
	mux.HandleFunc("DELETE /api/folders/{id}", a.handleDeleteFolder)
	mux.HandleFunc("POST /api/network", a.handleNetwork)
	mux.HandleFunc("POST /api/demo/change", a.handleDemoChange)
	mux.HandleFunc("GET /api/diagnostics", a.handleDiagnostics)
	mux.HandleFunc("/proxy/", a.handleProxy)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validRequestHost(r.Host, apiPort) {
			writeError(w, http.StatusMisdirectedRequest, "request host must be localhost")
			return
		}
		if isMutation(r.Method) {
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
	host, port, err := net.SplitHostPort(raw)
	if err != nil || !isLoopbackHost(host) {
		return false
	}
	return port == apiPort || port == "3000"
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
		Name   string `json:"name"`
		Target string `json:"target"`
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
	record := ServiceRecord{ID: id, Name: body.Name, Target: strings.TrimRight(body.Target, "/")}
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
		Path   string `json:"path"`
		PeerID string `json:"peerId"`
		Name   string `json:"name"`
	}
	if err := decodeBody(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	folder, err := a.addFolder(body.Path, body.PeerID, body.Name)
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

func (a *App) handleDemoChange(w http.ResponseWriter, r *http.Request) {
	if !a.demo {
		http.NotFound(w, nil)
		return
	}
	var folderID, source string
	a.mu.RLock()
	for id, entry := range a.folders {
		if entry.Folder.ID == "demo-shared-assets" {
			folderID = id
			source = entry.Folder.Path
			break
		}
	}
	a.mu.RUnlock()
	if folderID == "" {
		writeError(w, http.StatusInternalServerError, "demo folder is unavailable")
		return
	}
	if err := a.requireConnectedAndUnpaused(folderID); err != nil {
		writeMutationError(w, err)
		return
	}
	id, err := randomSuffix()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create sample file")
		return
	}
	name := "change-" + time.Now().UTC().Format("20060102T150405") + "-" + id + ".txt"
	content := fmt.Sprintf("A real Loom demo transfer created at %s.\nThis sample was sent over an encrypted libp2p stream through the Studio relay.\n", time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0600); err != nil {
		writeError(w, http.StatusInternalServerError, "could not write demo source file")
		return
	}
	if err := a.syncFolder(r.Context(), folderID); err != nil {
		writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) requireConnectedAndUnpaused(folderID string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	entry := a.folders[folderID]
	if entry == nil {
		return errors.New("demo folder is unavailable")
	}
	if !a.connected {
		return errNetworkPaused
	}
	if entry.Folder.Paused {
		return errFolderPaused
	}
	return nil
}

func (a *App) handleDiagnostics(w http.ResponseWriter, _ *http.Request) {
	state := a.State()
	diagnostics := map[string]any{
		"ok":             true,
		"version":        Version,
		"uptimeSeconds":  int64(time.Since(a.started).Seconds()),
		"network":        map[string]any{"connected": state.Network.Connected, "peerId": state.Network.PeerID, "addresses": state.Network.Addresses, "private": true, "demo": a.demo},
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
			basePath = "/"
		}
		target.Path = path.Join(basePath, tail)
	}
	target.RawQuery = r.URL.RawQuery
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProxyBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid proxy request")
		return
	}
	copyEndToEndHeaders(request.Header, r.Header)
	request.Header.Del("Authorization")
	request.Header.Del("Cookie")
	request.Header.Del("Origin")
	request.Header.Del("Referer")
	request.Host = target.Host
	client := &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "published service is unavailable")
		return
	}
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, maxProxyReply+1))
	if err != nil || len(responseBytes) > maxProxyReply {
		writeError(w, http.StatusBadGateway, "service response exceeds the size limit")
		return
	}
	writeProxyResponse(w, response, responseBytes, record.Target, r.URL.Path)
}

func (a *App) proxyOverStream(w http.ResponseWriter, r *http.Request, stream network.Stream, serviceID, tail string, record ServiceRecord) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(45 * time.Second))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProxyBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	requestURL := &url.URL{Scheme: "http", Host: "loom.invalid", Path: "/" + tail, RawQuery: r.URL.RawQuery}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid proxy request")
		return
	}
	copyEndToEndHeaders(request.Header, r.Header)
	request.Header.Set("X-Loom-Service-ID", serviceID)
	request.Host = "loom.invalid"
	if err := request.Write(stream); err != nil {
		writeError(w, http.StatusBadGateway, "could not send request to service peer")
		return
	}
	if err := stream.CloseWrite(); err != nil {
		writeError(w, http.StatusBadGateway, "could not finish request to service peer")
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(stream), request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "service peer returned an invalid response")
		return
	}
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, maxProxyReply+1))
	if err != nil || len(responseBytes) > maxProxyReply {
		writeError(w, http.StatusBadGateway, "service response exceeds the size limit")
		return
	}
	writeProxyResponse(w, response, responseBytes, record.Target, r.URL.Path)
}

func writeProxyResponse(w http.ResponseWriter, response *http.Response, responseBytes []byte, target, requestPath string) {
	if location := response.Header.Get("Location"); location != "" {
		rewritten, ok := proxyLocation(location, target, requestPath)
		if !ok {
			response.StatusCode = http.StatusBadGateway
			response.Status = "502 Bad Gateway"
			response.Header.Del("Location")
			responseBytes = []byte("service redirect was blocked because it left the published loopback service")
		} else {
			response.Header.Set("Location", rewritten)
		}
	}
	clean := make(http.Header)
	copyEndToEndHeaders(clean, response.Header)
	response.Header = clean
	response.Header.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups; frame-ancestors 'none'")
	response.Header.Set("X-Content-Type-Options", "nosniff")
	response.Header.Del("Access-Control-Allow-Origin")
	response.Header.Del("Access-Control-Allow-Credentials")
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(responseBytes)))
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBytes)
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
