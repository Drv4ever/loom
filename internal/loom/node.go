package loom

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	infoProtocol   = protocol.ID("/loom/info/1.0.0")
	httpProtocol   = protocol.ID("/loom/http/1.0.0")
	folderProtocol = protocol.ID("/loom/folder/1.0.0")
	maxFileSize    = 100 << 20
	maxProxyBody   = 10 << 20
	maxProxyReply  = 32 << 20
)

type NodeOptions struct {
	Name          string
	Platform      string
	DataDir       string
	IdentityPath  string
	ReceiveDir    string
	NetworkKey    []byte
	ListenAddress string
	RelayService  bool
	AllowLoopback bool
	IsBlocked     func(peer.ID) bool
}

type Node struct {
	Host       host.Host
	Name       string
	Platform   string
	ReceiveDir string
	HistoryDir string

	mu           sync.RWMutex
	services     map[string]ServiceRecord
	received     map[string]ReceivedFolder
	receivedPath string
	mdns         mdns.Service
	closed       bool
	enabled      *atomic.Bool
	receiveMu    sync.Mutex

	trash             *TrashManager
	conflicts         *ConflictStore
	tombstones        map[string]map[string]time.Time
	getCustomPath     func(folderID string) string
	checkFolderAccess func(folderID, peerID string, isWrite bool) error
}

type nodeGater struct {
	enabled   *atomic.Bool
	isBlocked func(peer.ID) bool
}

func (g nodeGater) InterceptPeerDial(p peer.ID) bool {
	if !g.enabled.Load() || (g.isBlocked != nil && g.isBlocked(p)) {
		return false
	}
	return true
}

func (g nodeGater) InterceptAddrDial(p peer.ID, _ ma.Multiaddr) bool {
	if !g.enabled.Load() || (g.isBlocked != nil && g.isBlocked(p)) {
		return false
	}
	return true
}

func (g nodeGater) InterceptAccept(network.ConnMultiaddrs) bool {
	return g.enabled.Load()
}

func (g nodeGater) InterceptSecured(_ network.Direction, p peer.ID, _ network.ConnMultiaddrs) bool {
	if !g.enabled.Load() || (g.isBlocked != nil && g.isBlocked(p)) {
		return false
	}
	return true
}

func (g nodeGater) InterceptUpgraded(c network.Conn) (bool, control.DisconnectReason) {
	if !g.enabled.Load() || (g.isBlocked != nil && g.isBlocked(c.RemotePeer())) {
		return false, control.DisconnectReason(1)
	}
	return true, 0
}

var _ connmgr.ConnectionGater = nodeGater{}

type mdnsNotifee struct{ found func(peer.AddrInfo) }

func (m mdnsNotifee) HandlePeerFound(info peer.AddrInfo) {
	if m.found != nil {
		m.found(info)
	}
}

func NewNode(opts NodeOptions) (*Node, error) {
	if len(opts.NetworkKey) != 32 {
		return nil, fmt.Errorf("network key must be 32 bytes")
	}
	if opts.ListenAddress == "" {
		opts.ListenAddress = "/ip4/0.0.0.0/tcp/0"
	}
	if opts.Platform == "" {
		opts.Platform = "unknown"
	}
	if err := os.MkdirAll(opts.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("create node data directory: %w", err)
	}
	if err := os.Chmod(opts.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("secure node data directory: %w", err)
	}
	identity, err := loadOrCreateIdentity(opts.IdentityPath)
	if err != nil {
		return nil, err
	}
	var enabled atomic.Bool
	enabled.Store(true)
	libp2pOptions := []libp2p.Option{
		libp2p.Identity(identity),
		libp2p.PrivateNetwork(pnet.PSK(opts.NetworkKey)),
		libp2p.ListenAddrStrings(opts.ListenAddress),
		libp2p.UserAgent("loom/" + Version),
		libp2p.ConnectionGater(nodeGater{enabled: &enabled, isBlocked: opts.IsBlocked}),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
	}
	if opts.RelayService {
		libp2pOptions = append(libp2pOptions, libp2p.ForceReachabilityPublic())
		if opts.AllowLoopback {
			libp2pOptions = append(libp2pOptions, libp2p.EnableRelayService(
				relay.WithLimit(&relay.RelayLimit{Duration: 30 * time.Minute, Data: 1 << 30}),
				relay.WithReservationAddressFilter(func(ma.Multiaddr) bool { return true }),
			))
		} else {
			libp2pOptions = append(libp2pOptions, libp2p.EnableRelayService(relay.WithLimit(&relay.RelayLimit{Duration: 30 * time.Minute, Data: 1 << 30})))
		}
	}
	h, err := libp2p.New(libp2pOptions...)
	if err != nil {
		return nil, fmt.Errorf("start libp2p host: %w", err)
	}
	for _, dir := range []string{opts.ReceiveDir, filepath.Join(opts.DataDir, "history")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("create managed transfer directory: %w", err)
		}
		if err := os.Chmod(dir, 0700); err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("secure managed transfer directory: %w", err)
		}
	}
	n := &Node{
		Host: h, Name: opts.Name, Platform: opts.Platform, ReceiveDir: opts.ReceiveDir,
		HistoryDir: filepath.Join(opts.DataDir, "history"), services: make(map[string]ServiceRecord), enabled: &enabled,
		received: make(map[string]ReceivedFolder), receivedPath: filepath.Join(opts.DataDir, "received.json"),
		trash:      NewTrashManager(filepath.Join(opts.DataDir, "trash"), 30*24*time.Hour),
		conflicts:  NewConflictStore(),
		tombstones: make(map[string]map[string]time.Time),
	}
	if err := n.loadReceived(); err != nil {
		_ = h.Close()
		return nil, err
	}
	h.SetStreamHandler(infoProtocol, n.handleInfo)
	h.SetStreamHandler(httpProtocol, n.handleHTTP)
	h.SetStreamHandler(folderProtocol, n.handleFolder)
	h.SetStreamHandler(syncProtocol, n.handleBidirectionalSync)
	h.SetStreamHandler(tcpForwardProtocol, n.handleTCPForward)
	return n, nil
}

func (n *Node) SetEnabled(enabled bool) {
	n.enabled.Store(enabled)
}

// ClosePeer immediately terminates all active connections to the specified peer.
func (n *Node) ClosePeer(p peer.ID) error {
	if n == nil || n.Host == nil {
		return nil
	}
	return n.Host.Network().ClosePeer(p)
}

// IsRelayed checks if the active connection to a peer is through a circuit relay.
func (n *Node) IsRelayed(id peer.ID) bool {
	if n == nil || n.Host == nil {
		return false
	}
	conns := n.Host.Network().ConnsToPeer(id)
	if len(conns) == 0 {
		return false
	}
	for _, c := range conns {
		if strings.Contains(c.RemoteMultiaddr().String(), "p2p-circuit") {
			return true
		}
	}
	return false
}

func loadOrCreateIdentity(file string) (crypto.PrivKey, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return nil, fmt.Errorf("create identity directory: %w", err)
	}
	data, err := os.ReadFile(file)
	if err == nil {
		key, unmarshalErr := crypto.UnmarshalPrivateKey(data)
		if unmarshalErr != nil {
			return nil, fmt.Errorf("read stored identity: %w", unmarshalErr)
		}
		if err := os.Chmod(file, 0600); err != nil {
			return nil, fmt.Errorf("secure stored identity: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read stored identity: %w", err)
	}
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}
	encoded, err := crypto.MarshalPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode identity: %w", err)
	}
	if err := os.WriteFile(file, encoded, 0600); err != nil {
		return nil, fmt.Errorf("save identity: %w", err)
	}
	return key, nil
}

func (n *Node) StartMDNS(onPeer func(peer.AddrInfo)) error {
	service := mdns.NewMdnsService(n.Host, "loom", mdnsNotifee{found: onPeer})
	if err := service.Start(); err != nil {
		return fmt.Errorf("start LAN discovery: %w", err)
	}
	n.mdns = service
	return nil
}

func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	service := n.mdns
	n.mu.Unlock()
	if service != nil {
		_ = service.Close()
	}
	return n.Host.Close()
}

func (n *Node) Addrs() []string {
	addresses := make([]string, 0, len(n.Host.Addrs()))
	for _, addr := range n.Host.Addrs() {
		addresses = append(addresses, addr.Encapsulate(ma.StringCast("/p2p/"+n.Host.ID().String())).String())
	}
	// The dashboard's copy-address action uses the first address. Prefer a LAN
	// address so that pasting it on another computer doesn't dial its loopback.
	sort.SliceStable(addresses, func(i, j int) bool {
		return addressRank(addresses[i]) < addressRank(addresses[j])
	})
	return addresses
}

func addressRank(address string) int {
	addr, err := ma.NewMultiaddr(address)
	if err != nil {
		return 3
	}
	value, err := addr.ValueForProtocol(ma.P_IP4)
	if err != nil {
		value, _ = addr.ValueForProtocol(ma.P_IP6)
	}
	ip := net.ParseIP(value)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return 2
	}
	if ip.IsPrivate() {
		return 0
	}
	return 1
}

func (n *Node) LocalServices() []ServiceRecord {
	n.mu.RLock()
	defer n.mu.RUnlock()
	services := make([]ServiceRecord, 0, len(n.services))
	for _, service := range n.services {
		services = append(services, service)
	}
	return services
}

func (n *Node) SetService(record ServiceRecord) {
	n.mu.Lock()
	n.services[record.ID] = record
	n.mu.Unlock()
}

func (n *Node) DeleteService(id string) {
	n.mu.Lock()
	delete(n.services, id)
	n.mu.Unlock()
}

func (n *Node) handleInfo(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	if !n.enabled.Load() {
		return
	}
	_ = json.NewEncoder(stream).Encode(PeerSummary{Name: n.Name, Platform: n.Platform, Services: n.LocalServices()})
}

func (n *Node) FetchInfo(ctx context.Context, id peer.ID) (PeerSummary, error) {
	stream, err := n.Host.NewStream(network.WithAllowLimitedConn(ctx, "loom peer info"), id, infoProtocol)
	if err != nil {
		return PeerSummary{}, err
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	var summary PeerSummary
	decoder := json.NewDecoder(io.LimitReader(stream, 64<<10))
	if err := decoder.Decode(&summary); err != nil {
		return PeerSummary{}, err
	}
	return summary, nil
}

func (n *Node) handleHTTP(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(45 * time.Second))
	if !n.enabled.Load() {
		writeSimpleHTTPError(stream, http.StatusServiceUnavailable, "network is paused")
		return
	}
	reader := bufio.NewReader(stream)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	defer req.Body.Close()

	serviceID := req.Header.Get("X-Loom-Service-ID")
	req.Header.Del("X-Loom-Service-ID")
	n.mu.RLock()
	service, ok := n.services[serviceID]
	n.mu.RUnlock()
	if !ok {
		writeSimpleHTTPError(stream, http.StatusNotFound, "service is not published")
		return
	}
	remotePeer := stream.Conn().RemotePeer().String()
	if len(service.AllowedPeers) > 0 && !containsPeer(service.AllowedPeers, remotePeer) {
		writeSimpleHTTPError(stream, http.StatusForbidden, "service access denied for this device")
		return
	}
	pathPart, err := safeServicePath(req.URL)
	if err != nil {
		writeSimpleHTTPError(stream, http.StatusBadRequest, "invalid service path")
		return
	}
	target, err := validateLoopbackHTTPURL(service.Target)
	if err != nil {
		writeSimpleHTTPError(stream, http.StatusBadGateway, "published service target is invalid")
		return
	}
	basePath := strings.TrimRight(target.Path, "/")
	if pathPart != "" {
		if basePath == "" {
			basePath = "/"
		}
		target.Path = path.Join(basePath, pathPart)
	} else if basePath != "" {
		target.Path = basePath
	} else {
		target.Path = "/"
	}
	target.RawQuery = req.URL.RawQuery
	target.Fragment = ""

	// Support WebSocket upgrade
	if isWebSocketUpgrade(req) {
		targetConn, err := net.DialTimeout("tcp", target.Host, 5*time.Second)
		if err != nil {
			writeSimpleHTTPError(stream, http.StatusBadGateway, "published service is unavailable")
			return
		}
		defer targetConn.Close()

		req.URL.Path = target.Path
		req.Host = target.Host
		if err := req.Write(targetConn); err != nil {
			writeSimpleHTTPError(stream, http.StatusBadGateway, "failed to send websocket handshake to service")
			return
		}

		bridgeConns(targetConn, stream)
		return
	}

	outReq, err := http.NewRequestWithContext(context.Background(), req.Method, target.String(), req.Body)
	if err != nil {
		writeSimpleHTTPError(stream, http.StatusBadRequest, "invalid request")
		return
	}
	outReq.ContentLength = req.ContentLength
	copyEndToEndHeaders(outReq.Header, req.Header)
	if !service.Trusted {
		outReq.Header.Del("Authorization")
		outReq.Header.Del("Cookie")
		outReq.Header.Del("Origin")
		outReq.Header.Del("Referer")
	}
	outReq.Host = target.Host
	client := &http.Client{
		Timeout:       60 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(outReq)
	if err != nil {
		writeSimpleHTTPError(stream, http.StatusBadGateway, "published service is unavailable")
		return
	}
	defer resp.Body.Close()

	cleanHeaders := make(http.Header)
	copyEndToEndHeaders(cleanHeaders, resp.Header)
	resp.Header = cleanHeaders
	_ = resp.Write(stream)
}

func writeSimpleHTTPError(w io.Writer, status int, message string) {
	response := &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader(message)), ContentLength: int64(len(message)),
	}
	_ = response.Write(w)
}

func safeServicePath(u *url.URL) (string, error) {
	if u == nil || u.IsAbs() || u.Host != "" || strings.Contains(u.EscapedPath(), "\\") {
		return "", fmt.Errorf("absolute or malformed path")
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil || strings.Contains(decoded, "\\") {
		return "", fmt.Errorf("malformed path")
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("dot segment")
		}
	}
	return path.Clean("/" + decoded)[1:], nil
}

func copyEndToEndHeaders(dst, src http.Header) {
	hop := map[string]bool{
		"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
		"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	}
	for _, value := range src.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			hop[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
		if hop[canonical] || strings.HasPrefix(canonical, "X-Forwarded-") {
			continue
		}
		dst[canonical] = append([]string(nil), values...)
	}
}

func isWebSocketUpgrade(req *http.Request) bool {
	if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		return true
	}
	for _, v := range req.Header.Values("Connection") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
				return true
			}
		}
	}
	return false
}

func validateLoopbackHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("target must be an HTTP loopback URL")
	}
	if !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("target must use loopback")
	}
	if port := u.Port(); port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return nil, fmt.Errorf("invalid target port")
		}
	}
	return u, nil
}

func isLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func (n *Node) TransferFile(ctx context.Context, id peer.ID, folderID string, desc fileDescriptor, input io.Reader, folderName ...string) (int64, error) {
	stream, err := n.Host.NewStream(network.WithAllowLimitedConn(ctx, "loom folder sync"), id, folderProtocol)
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(90 * time.Second))
	if desc.Size < 0 || desc.Size > maxFileSize {
		return 0, fmt.Errorf("file exceeds transfer limit")
	}
	name := folderID
	if len(folderName) > 0 {
		name = folderName[0]
	}
	header, err := json.Marshal(struct {
		FolderID   string         `json:"folderId"`
		FolderName string         `json:"folderName,omitempty"`
		File       fileDescriptor `json:"file"`
	}{FolderID: folderID, FolderName: name, File: desc})
	if err != nil {
		return 0, err
	}
	if err := writeFrame(stream, header); err != nil {
		return 0, err
	}
	written, err := io.CopyN(stream, input, desc.Size)
	if err != nil {
		return written, err
	}
	if err := stream.CloseWrite(); err != nil {
		return written, err
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := readFrame(stream, &result, 16<<10); err != nil {
		return written, err
	}
	if !result.OK {
		return written, errors.New(result.Error)
	}
	return written, nil
}

func (n *Node) handleFolder(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(90 * time.Second))
	if !n.enabled.Load() {
		_ = writeFrame(stream, mustJSON(map[string]any{"ok": false, "error": "network is paused"}))
		return
	}
	var header struct {
		FolderID   string         `json:"folderId"`
		FolderName string         `json:"folderName"`
		File       fileDescriptor `json:"file"`
	}
	if err := readFrame(stream, &header, 16<<10); err != nil {
		return
	}
	result := map[string]any{"ok": false, "error": "invalid transfer"}
	if !isID(header.FolderID) || len(header.FolderName) > 120 || !validRelativePath(header.File.Path) || header.File.Size < 0 || header.File.Size > maxFileSize || !validSHA256(header.File.SHA256) {
		_ = writeFrame(stream, mustJSON(result))
		return
	}
	if err := n.receiveFile(stream, header.FolderID, header.FolderName, header.File); err != nil {
		result["error"] = err.Error()
	} else {
		result = map[string]any{"ok": true}
	}
	_ = writeFrame(stream, mustJSON(result))
}

func (n *Node) receiveFile(stream network.Stream, folderID, folderName string, desc fileDescriptor) error {
	n.receiveMu.Lock()
	defer n.receiveMu.Unlock()
	sender := stream.Conn().RemotePeer().String()
	n.mu.RLock()
	prior, exists := n.received[folderID]
	n.mu.RUnlock()
	if exists && prior.PeerID != sender {
		return fmt.Errorf("folder belongs to a different sender")
	}
	receiveBase := filepath.Join(n.ReceiveDir, folderID)
	historyBase := filepath.Join(n.HistoryDir, folderID)
	if err := os.MkdirAll(receiveBase, 0700); err != nil {
		return fmt.Errorf("prepare receive folder: %w", err)
	}
	if err := os.MkdirAll(historyBase, 0700); err != nil {
		return fmt.Errorf("prepare version history: %w", err)
	}
	root, err := os.OpenRoot(receiveBase)
	if err != nil {
		return fmt.Errorf("open receive folder: %w", err)
	}
	defer root.Close()
	history, err := os.OpenRoot(historyBase)
	if err != nil {
		return fmt.Errorf("open history folder: %w", err)
	}
	defer history.Close()
	if err := ensureSafeParent(root, path.Dir(desc.Path)); err != nil {
		return err
	}
	if err := ensureSafeParent(history, path.Dir(desc.Path)); err != nil {
		return err
	}
	if existing, err := root.Lstat(desc.Path); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return fmt.Errorf("destination is not a regular file")
		}
		if existing.Size() > maxFileSize {
			return fmt.Errorf("existing destination exceeds transfer size limit")
		}
		prior, err := root.Open(desc.Path)
		if err != nil {
			return fmt.Errorf("open existing file: %w", err)
		}
		hash := sha256.New()
		copied, copyErr := io.Copy(hash, io.LimitReader(prior, maxFileSize+1))
		closeErr := prior.Close()
		if copyErr != nil {
			return fmt.Errorf("read existing file: %w", copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
		if copied > maxFileSize {
			return fmt.Errorf("existing destination exceeds transfer size limit")
		}
		if hex.EncodeToString(hash.Sum(nil)) != desc.SHA256 {
			backup := desc.Path + "." + time.Now().UTC().Format("20060102T150405.000000000") + ".bak"
			source, err := root.Open(desc.Path)
			if err != nil {
				return fmt.Errorf("open version to preserve: %w", err)
			}
			if err := copyIntoRoot(history, backup, source, maxFileSize); err != nil {
				_ = source.Close()
				return fmt.Errorf("preserve previous version: %w", err)
			}
			_ = source.Close()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	tmp, err := randomSuffix()
	if err != nil {
		return err
	}
	tmpName := path.Join(path.Dir(desc.Path), ".loom-part-"+tmp)
	destination, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(destination, hash), stream, desc.Size)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil || written != desc.Size {
		_ = root.Remove(tmpName)
		if copyErr != nil {
			return fmt.Errorf("receive file bytes: %w", copyErr)
		}
		return io.ErrUnexpectedEOF
	}
	if closeErr != nil {
		_ = root.Remove(tmpName)
		return closeErr
	}
	if syncErr != nil {
		_ = root.Remove(tmpName)
		return syncErr
	}
	if desc.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != desc.SHA256 {
		_ = root.Remove(tmpName)
		return fmt.Errorf("file checksum mismatch")
	}
	if err := root.Rename(tmpName, desc.Path); err != nil {
		_ = root.Remove(tmpName)
		return fmt.Errorf("install received file: %w", err)
	}
	if !desc.ModTime.IsZero() {
		_ = root.Chtimes(desc.Path, desc.ModTime, desc.ModTime)
	}
	return n.recordReceived(folderID, folderName, sender, desc)
}

func copyIntoRoot(root *os.Root, destination string, input io.Reader, max int64) error {
	if err := ensureSafeParent(root, path.Dir(destination)); err != nil {
		return err
	}
	file, err := root.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(input, max+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = root.Remove(destination)
		return copyErr
	}
	if written > max {
		_ = root.Remove(destination)
		return fmt.Errorf("existing version exceeds transfer size limit")
	}
	if closeErr != nil {
		_ = root.Remove(destination)
		return closeErr
	}
	return nil
}

func ensureSafeParent(root *os.Root, parent string) error {
	if parent == "." || parent == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(parent, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid destination path")
		}
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create destination directory: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect destination directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("destination contains an unsafe directory component")
		}
	}
	return nil
}

func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > 1<<20 {
		return fmt.Errorf("message too large")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	if _, err := w.Write(size[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader, target any, max uint32) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > max {
		return fmt.Errorf("invalid message size")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func randomSuffix() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validRelativePath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) {
		return false
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isID(value string) bool {
	if len(value) < 8 || len(value) > 80 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func containsPeer(peers []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, p := range peers {
		if strings.TrimSpace(p) == target {
			return true
		}
	}
	return false
}
