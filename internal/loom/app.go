package loom

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	ma "github.com/multiformats/go-multiaddr"
)

type Config struct {
	Name       string
	Listen     string
	DataDir    string
	NetworkKey string
	Demo       bool
	Relay      bool
}

type folderEntry struct {
	Folder   Folder
	cancel   context.CancelFunc
	syncing  bool
	manifest map[string]string
}

type peerEntry struct {
	Device   Device
	Info     peer.AddrInfo
	Services []ServiceRecord
}

type persistedFolder struct {
	Folder   Folder            `json:"folder"`
	Manifest map[string]string `json:"manifest"`
}

type persistedState struct {
	Name             string            `json:"name"`
	Connected        bool              `json:"connected"`
	Services         []ServiceRecord   `json:"services"`
	Folders          []persistedFolder `json:"folders"`
	TransferredBytes int64             `json:"transferredBytes"`
	Activity         []Activity        `json:"activity"`
	Peers            []peer.AddrInfo   `json:"peers"`
}

type App struct {
	mu               sync.RWMutex
	name             string
	networkID        string
	demo             bool
	connected        bool
	dataDir          string
	statePath        string
	networkKey       []byte
	main             *Node
	nodes            map[string]*Node
	peers            map[string]*peerEntry
	services         map[string]ServiceRecord
	remoteServices   map[string][]ServiceRecord
	forceRelay       map[string]bool
	relayInfos       map[string]peer.AddrInfo
	lastDial         map[string]time.Time
	folders          map[string]*folderEntry
	activities       []Activity
	transferredBytes int64
	started          time.Time
	ctx              context.Context
	cancel           context.CancelFunc
	servers          []*http.Server
	listeners        []net.Listener
	watcherDone      chan struct{}
	apiListen        string
}

func New(ctx context.Context, config Config) (*App, error) {
	dataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("secure data directory: %w", err)
	}
	key, err := loadNetworkKey(filepath.Join(dataDir, "network.key"), config.NetworkKey)
	if err != nil {
		return nil, err
	}
	appCtx, cancel := context.WithCancel(ctx)
	if config.Listen == "" {
		config.Listen = "127.0.0.1:8787"
	}
	a := &App{
		name: config.Name, demo: config.Demo, connected: true, dataDir: dataDir,
		statePath: filepath.Join(dataDir, "state.json"), networkKey: key, nodes: make(map[string]*Node),
		peers: make(map[string]*peerEntry), services: make(map[string]ServiceRecord),
		remoteServices: make(map[string][]ServiceRecord), forceRelay: make(map[string]bool), relayInfos: make(map[string]peer.AddrInfo), lastDial: make(map[string]time.Time), folders: make(map[string]*folderEntry),
		started: time.Now(), ctx: appCtx, cancel: cancel, watcherDone: make(chan struct{}), apiListen: config.Listen,
	}
	hash := sha256.Sum256(key)
	a.networkID = "loom-" + hex.EncodeToString(hash[:6])
	if a.name == "" {
		a.name = "Personal workspace"
	}
	if config.Demo {
		if err := a.startDemo(config); err != nil {
			_ = a.Close(context.Background())
			return nil, err
		}
	} else {
		mainDir := filepath.Join(dataDir, "node")
		node, err := NewNode(NodeOptions{
			Name: deviceName(config.Name), Platform: runtime.GOOS, DataDir: mainDir,
			IdentityPath: filepath.Join(dataDir, "identity.key"),
			ReceiveDir:   filepath.Join(dataDir, "received"), NetworkKey: key, ListenAddress: "/ip4/0.0.0.0/tcp/0", RelayService: config.Relay,
		})
		if err != nil {
			return nil, err
		}
		a.main = node
		a.nodes[node.Host.ID().String()] = node
		a.loadPersisted()
		node.SetEnabled(a.connected)
		if err := node.StartMDNS(func(info peer.AddrInfo) {
			go a.connectPeer(info)
		}); err != nil {
			_ = node.Close()
			return nil, err
		}
	}
	go a.refreshLoop()
	go a.syncLoop()
	return a, nil
}

func loadNetworkKey(file, supplied string) ([]byte, error) {
	var requested []byte
	if supplied != "" {
		decoded, err := hex.DecodeString(supplied)
		if err != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("--network-key must contain exactly 64 hexadecimal characters")
		}
		requested = decoded
	}
	existing, err := os.ReadFile(file)
	if err == nil {
		if err := os.Chmod(file, 0600); err != nil {
			return nil, fmt.Errorf("secure stored network key: %w", err)
		}
		stored, decodeErr := hex.DecodeString(strings.TrimSpace(string(existing)))
		if decodeErr != nil || len(stored) != 32 {
			return nil, fmt.Errorf("stored network key is invalid")
		}
		if requested != nil && !equalBytes(requested, stored) {
			return nil, fmt.Errorf("provided network key does not match the key stored in %s", file)
		}
		return stored, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read network key: %w", err)
	}
	if requested == nil {
		requested = make([]byte, 32)
		if _, err := rand.Read(requested); err != nil {
			return nil, fmt.Errorf("generate network key: %w", err)
		}
	}
	if err := os.WriteFile(file, []byte(hex.EncodeToString(requested)+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("save network key: %w", err)
	}
	return requested, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (a *App) loadPersisted() {
	data, err := os.ReadFile(a.statePath)
	if err != nil {
		return
	}
	var saved persistedState
	if json.Unmarshal(data, &saved) != nil {
		return
	}
	if saved.Name != "" {
		a.name = saved.Name
	}
	a.connected = saved.Connected
	if len(saved.Activity) > 50 {
		saved.Activity = saved.Activity[:50]
	}
	a.activities = saved.Activity
	a.transferredBytes = saved.TransferredBytes
	for _, service := range saved.Services {
		if _, err := validateLoopbackHTTPURL(service.Target); err != nil || service.ID == "" || service.Name == "" {
			continue
		}
		a.services[service.ID] = service
		a.main.SetService(service)
	}
	for _, savedFolder := range saved.Folders {
		folder := savedFolder.Folder
		if folder.ID == "" || folder.Path == "" || folder.PeerID == "" {
			continue
		}
		if _, err := os.Stat(folder.Path); err != nil {
			continue
		}
		folder.Manifest = nil
		if folder.Paused {
			folder.Status = "paused"
		} else {
			folder.Status = "pending"
		}
		a.folders[folder.ID] = &folderEntry{Folder: folder, manifest: savedFolder.Manifest}
	}
	for _, info := range saved.Peers {
		if info.ID == "" || info.ID == a.main.Host.ID() {
			continue
		}
		a.main.Host.Peerstore().AddAddrs(info.ID, info.Addrs, time.Hour*24)
		if len(info.Addrs) > 0 {
			a.peers[info.ID.String()] = &peerEntry{Info: info, Device: Device{ID: info.ID.String(), Name: "Device " + shortID(info.ID.String()), Platform: "unknown", Connection: "direct"}}
		}
	}
}

func (a *App) persistLocked() error {
	saved := persistedState{Name: a.name, Connected: a.connected, TransferredBytes: a.transferredBytes, Activity: a.activities}
	for _, service := range a.services {
		saved.Services = append(saved.Services, service)
	}
	for _, entry := range a.folders {
		saved.Folders = append(saved.Folders, persistedFolder{Folder: entry.Folder, Manifest: entry.manifest})
	}
	for _, entry := range a.peers {
		if entry.Info.ID != "" && len(entry.Info.Addrs) != 0 {
			saved.Peers = append(saved.Peers, entry.Info)
		}
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.statePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (a *App) addActivityLocked(kind, title, detail string) {
	id, _ := randomSuffix()
	a.activities = append([]Activity{{ID: id, Type: kind, Title: title, Detail: detail, Time: time.Now().UTC()}}, a.activities...)
	if len(a.activities) > 50 {
		a.activities = a.activities[:50]
	}
}

func (a *App) refreshLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.refreshPeers()
		}
	}
}

func (a *App) refreshPeers() {
	if a.main == nil {
		return
	}
	ids := a.main.Host.Network().Peers()
	active := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == a.main.Host.ID() {
			continue
		}
		idString := id.String()
		conns := a.main.Host.Network().ConnsToPeer(id)
		if len(conns) == 0 {
			continue
		}
		addr := conns[0].RemoteMultiaddr()
		connection := "direct"
		if strings.Contains(addr.String(), "p2p-circuit") {
			connection = "relay"
		}
		a.mu.RLock()
		forceRelay := a.forceRelay[idString]
		relayInfo := a.relayInfos[idString]
		a.mu.RUnlock()
		if forceRelay && connection != "relay" {
			// The target can advertise its direct address during the relay handshake.
			// The demo deliberately keeps this edge on the relay so the UI reflects
			// the path actually used for service and folder traffic.
			_ = a.main.Host.Network().ClosePeer(id)
			continue
		}
		active[idString] = true
		info := peer.AddrInfo{ID: id, Addrs: a.main.Host.Peerstore().Addrs(id)}
		if forceRelay && connection == "relay" && len(relayInfo.Addrs) != 0 {
			info = relayInfo
			a.main.Host.Peerstore().ClearAddrs(id)
			a.main.Host.Peerstore().SetAddrs(id, relayInfo.Addrs, 24*time.Hour)
		}
		summary := PeerSummary{}
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
		if result, err := a.main.FetchInfo(ctx, id); err == nil {
			summary = result
		}
		cancel()
		a.mu.Lock()
		entry := a.peers[idString]
		wasOnline := entry != nil && entry.Device.Online
		if entry == nil {
			entry = &peerEntry{}
			a.peers[idString] = entry
		}
		if summary.Name != "" {
			entry.Device.Name = summary.Name
		}
		if summary.Platform != "" {
			entry.Device.Platform = summary.Platform
		}
		if entry.Device.Name == "" {
			entry.Device.Name = "Device " + shortID(idString)
		}
		if entry.Device.Platform == "" {
			entry.Device.Platform = "unknown"
		}
		entry.Device.ID = idString
		entry.Device.Online = true
		entry.Device.Self = false
		entry.Device.Connection = connection
		entry.Device.Address = addr.String()
		entry.Device.LastSeen = time.Now().UTC()
		entry.Info = info
		entry.Services = summary.Services
		entry.Device.LatencyMs = 0
		if len(info.Addrs) != 0 && !forceRelay {
			a.main.Host.Peerstore().AddAddrs(id, info.Addrs, time.Hour*24)
		}
		a.updateFoldersForPeerLocked(idString, true)
		if !wasOnline {
			a.addActivityLocked("device", "Device connected", entry.Device.Name+" joined the private network.")
		}
		_ = a.persistLocked()
		a.mu.Unlock()
	}
	a.mu.Lock()
	for id, entry := range a.peers {
		if !active[id] && entry.Device.Online {
			entry.Device.Online = false
			entry.Device.LastSeen = time.Now().UTC()
			entry.Services = nil
			a.updateFoldersForPeerLocked(id, false)
			a.addActivityLocked("device", "Device disconnected", entry.Device.Name+" left the private network.")
		}
	}
	var redial []peer.AddrInfo
	now := time.Now()
	if a.connected {
		for id, entry := range a.peers {
			if active[id] || entry.Info.ID == "" || now.Sub(a.lastDial[id]) < 8*time.Second {
				continue
			}
			info := entry.Info
			if a.forceRelay[id] && len(a.relayInfos[id].Addrs) != 0 {
				info = a.relayInfos[id]
				a.main.Host.Peerstore().ClearAddrs(info.ID)
				a.main.Host.Peerstore().SetAddrs(info.ID, info.Addrs, 24*time.Hour)
			}
			if len(info.Addrs) == 0 {
				continue
			}
			a.lastDial[id] = now
			redial = append(redial, info)
		}
	}
	_ = a.persistLocked()
	a.mu.Unlock()
	for _, info := range redial {
		go func(info peer.AddrInfo) { _ = a.connectPeer(info) }(info)
	}
}

func (a *App) updateFoldersForPeerLocked(peerID string, online bool) {
	for _, folder := range a.folders {
		if folder.Folder.PeerID != peerID || folder.Folder.Paused || folder.syncing {
			continue
		}
		if online && folder.Folder.Status == "offline" {
			folder.Folder.Status = "pending"
			folder.Folder.Error = ""
		} else if !online && folder.Folder.Status != "offline" {
			folder.Folder.Status = "offline"
			folder.Folder.Error = "device is offline"
		}
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func deviceName(name string) string {
	if name != "" {
		return name
	}
	return "" + runtime.GOOS + " device"
}

func (a *App) connectPeer(info peer.AddrInfo) error {
	if a.main == nil || info.ID == "" || info.ID == a.main.Host.ID() {
		return fmt.Errorf("invalid peer address")
	}
	a.mu.RLock()
	connected := a.connected
	forceRelay := a.forceRelay[info.ID.String()]
	if forceRelay && len(a.relayInfos[info.ID.String()].Addrs) != 0 {
		info = a.relayInfos[info.ID.String()]
	}
	a.mu.RUnlock()
	if !connected {
		return fmt.Errorf("network is paused")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	if forceRelay {
		ctx = network.WithAllowLimitedConn(ctx, "loom forced relay connection")
		a.main.Host.Peerstore().ClearAddrs(info.ID)
		a.main.Host.Peerstore().SetAddrs(info.ID, info.Addrs, 24*time.Hour)
	}
	if err := a.main.Host.Connect(ctx, info); err != nil {
		return err
	}
	if forceRelay {
		a.main.Host.Peerstore().ClearAddrs(info.ID)
		a.main.Host.Peerstore().SetAddrs(info.ID, info.Addrs, 24*time.Hour)
	} else {
		a.main.Host.Peerstore().AddAddrs(info.ID, info.Addrs, time.Hour*24)
	}
	a.refreshPeers()
	return nil
}

func (a *App) joinAddress(raw string) error {
	addr, err := ma.NewMultiaddr(raw)
	if err != nil {
		return fmt.Errorf("invalid multiaddress")
	}
	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return fmt.Errorf("address must end in /p2p/<peer-id>")
	}
	return a.connectPeer(*info)
}

func (a *App) State() State {
	a.mu.RLock()
	connected := a.connected
	name := a.name
	demo := a.demo
	folders := make([]Folder, 0, len(a.folders))
	for _, entry := range a.folders {
		folder := entry.Folder
		folder.Manifest = nil
		folders = append(folders, folder)
	}
	services := make([]Service, 0, len(a.services)+4)
	for _, record := range a.services {
		services = append(services, a.serviceView(a.main, record))
	}
	for peerID, entry := range a.peers {
		if !entry.Device.Online {
			continue
		}
		for _, record := range entry.Services {
			node := a.nodes[peerID]
			services = append(services, a.serviceViewWithID(node, peerID, entry.Device.Name, record))
		}
	}
	devices := []Device{}
	if a.main != nil {
		devices = append(devices, Device{ID: a.main.Host.ID().String(), Name: a.main.Name, Platform: a.main.Platform, Online: true, Self: true, Connection: "local", Address: firstAddress(a.main.Addrs()), LastSeen: time.Now().UTC()})
	}
	for _, entry := range a.peers {
		devices = append(devices, entry.Device)
	}
	activity := append(make([]Activity, 0, len(a.activities)), a.activities...)
	transferred := a.transferredBytes
	peerCount := len(devices)
	a.mu.RUnlock()
	sort.Slice(folders, func(i, j int) bool { return folders[i].Name < folders[j].Name })
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	stats := Stats{OnlineDevices: 0, Services: len(services), TransferredBytes: transferred}
	for _, device := range devices {
		if device.Online {
			stats.OnlineDevices++
		}
	}
	if peerCount == 0 {
		stats.OnlineDevices = 0
	}
	for _, folder := range folders {
		if folder.Status == "synced" {
			stats.SyncedFiles += folder.Files
		}
	}
	return State{
		Network: NetworkState{ID: a.networkID, Name: name, Connected: connected, Demo: demo, PeerID: func() string {
			if a.main != nil {
				return a.main.Host.ID().String()
			}
			return ""
		}(), Addresses: func() []string {
			if a.main != nil {
				return a.main.Addrs()
			}
			return nil
		}(), Version: Version},
		Devices: devices, Services: services, Folders: folders, Activity: activity, Stats: stats,
	}
}

func (a *App) serviceView(node *Node, record ServiceRecord) Service {
	if node == nil {
		return a.serviceViewWithID(nil, "", "", record)
	}
	return a.serviceViewWithID(node, node.Host.ID().String(), node.Name, record)
}

func (a *App) serviceViewWithID(node *Node, peerID, peerName string, record ServiceRecord) Service {
	target, _ := validateLoopbackHTTPURL(record.Target)
	port := 0
	if target != nil {
		port, _ = net.LookupPort("tcp", target.Port())
	}
	return Service{ID: record.ID, Name: record.Name, DeviceID: peerID, DeviceName: peerName, Protocol: "http", Target: record.Target, Port: port, Status: "published", URL: "/proxy/" + peerID + "/" + record.ID + "/"}
}

func firstAddress(addresses []string) string {
	if len(addresses) == 0 {
		return ""
	}
	return addresses[0]
}

func (a *App) Close(ctx context.Context) error {
	a.cancel()
	for _, server := range a.servers {
		_ = server.Shutdown(ctx)
	}
	for _, listener := range a.listeners {
		_ = listener.Close()
	}
	for _, node := range a.nodes {
		_ = node.Close()
	}
	return nil
}

func (a *App) startDemo(config Config) error {
	primaryDir := filepath.Join(a.dataDir, "nodes", "primary")
	studioDir := filepath.Join(a.dataDir, "nodes", "studio")
	linuxDir := filepath.Join(a.dataDir, "nodes", "linux")
	for _, dir := range []string{primaryDir, studioDir, linuxDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create demo node folder: %w", err)
		}
	}
	primary, err := NewNode(NodeOptions{Name: "MacBook Pro", Platform: "darwin", DataDir: primaryDir, IdentityPath: filepath.Join(primaryDir, "identity.key"), ReceiveDir: filepath.Join(primaryDir, "received"), NetworkKey: a.networkKey, ListenAddress: "/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		return err
	}
	a.main = primary
	a.nodes[primary.Host.ID().String()] = primary
	a.loadPersisted()
	// Demo topology is also assembled on startup when a pause was persisted.
	// The primary is disabled again after the relay route is ready.
	primary.SetEnabled(true)
	studio, err := NewNode(NodeOptions{Name: "Studio Workstation", Platform: "darwin", DataDir: studioDir, IdentityPath: filepath.Join(studioDir, "identity.key"), ReceiveDir: filepath.Join(studioDir, "received"), NetworkKey: a.networkKey, ListenAddress: "/ip4/127.0.0.1/tcp/0", RelayService: true, AllowLoopback: true})
	if err != nil {
		return err
	}
	linux, err := NewNode(NodeOptions{Name: "Linux Server", Platform: "linux", DataDir: linuxDir, IdentityPath: filepath.Join(linuxDir, "identity.key"), ReceiveDir: filepath.Join(linuxDir, "received"), NetworkKey: a.networkKey, ListenAddress: "/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		_ = studio.Close()
		return err
	}
	a.nodes[studio.Host.ID().String()] = studio
	a.nodes[linux.Host.ID().String()] = linux
	a.forceRelay[linux.Host.ID().String()] = true
	if err := a.startDemoWebService(studio, "studio-preview", "Project preview", "Studio Workstation"); err != nil {
		return err
	}
	if err := a.startDemoWebService(linux, "linux-status", "Linux status", "Linux Server"); err != nil {
		return err
	}
	// Reserve the Linux node through Studio, then let the primary node connect only
	// to the relay path. This makes the advertised relay edge a real transport hop.
	studioInfo := peer.AddrInfo{ID: studio.Host.ID(), Addrs: studio.Host.Addrs()}
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	if err := linux.Host.Connect(ctx, studioInfo); err != nil {
		return fmt.Errorf("connect Linux demo peer to Studio relay: %w", err)
	}
	if _, err := client.Reserve(ctx, linux.Host, studioInfo); err != nil {
		return fmt.Errorf("reserve Linux demo relay path: %w", err)
	}
	if err := a.main.Host.Connect(ctx, studioInfo); err != nil {
		return fmt.Errorf("connect primary demo peer to Studio: %w", err)
	}
	relayAddresses := make([]ma.Multiaddr, 0, len(studio.Host.Addrs()))
	for _, address := range studio.Host.Addrs() {
		relayPart, _ := ma.NewMultiaddr("/p2p/" + studio.Host.ID().String() + "/p2p-circuit/p2p/" + linux.Host.ID().String())
		relayAddresses = append(relayAddresses, address.Encapsulate(relayPart))
	}
	if len(relayAddresses) == 0 {
		return fmt.Errorf("Studio relay did not advertise a listener")
	}
	a.main.Host.Peerstore().SetAddrs(linux.Host.ID(), relayAddresses, time.Hour*24)
	a.mu.Lock()
	a.relayInfos[linux.Host.ID().String()] = peer.AddrInfo{ID: linux.Host.ID(), Addrs: relayAddresses}
	a.mu.Unlock()
	if err := a.main.Host.Connect(ctx, peer.AddrInfo{ID: linux.Host.ID(), Addrs: relayAddresses}); err != nil {
		return fmt.Errorf("connect Linux demo peer through Studio relay: %w", err)
	}

	if err := a.prepareDemoFolder(linux); err != nil {
		return err
	}
	go a.renewDemoRelay(studio, linux)
	if !a.connected {
		// Refresh once while bootstrap connections are live so their current
		// addresses are persisted for an explicit later resume.
		a.refreshPeers()
		for _, id := range primary.Host.Network().Peers() {
			_ = primary.Host.Network().ClosePeer(id)
		}
		primary.SetEnabled(false)
	}
	a.refreshPeers()
	return nil
}

func (a *App) renewDemoRelay(relayNode, target *Node) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
			relayInfo := peer.AddrInfo{ID: relayNode.Host.ID(), Addrs: relayNode.Host.Addrs()}
			if target.Host.Network().Connectedness(relayNode.Host.ID()) == network.NotConnected {
				_ = target.Host.Connect(ctx, relayInfo)
			}
			_, _ = client.Reserve(ctx, target.Host, relayInfo)
			cancel()
		}
	}
}

func (a *App) startDemoWebService(node *Node, id, name, deviceName string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start demo HTTP service: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	target := fmt.Sprintf("http://127.0.0.1:%d", port)
	page := template.Must(template.New("demo-service").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Name}} · Loom</title><style>
:root{color-scheme:dark;font:16px/1.55 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#101a2c;color:#eef4ff}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(ellipse at 82% 12%,#284a7b 0,transparent 38%),linear-gradient(145deg,#101a2c,#182943 62%,#111c30)}
main{width:min(680px,100%);padding:38px;border:1px solid #3a5173;border-radius:18px;background:#17263bdc;box-shadow:0 24px 80px #07101f88}
.eyebrow{margin:0 0 18px;color:#87c9ff;font-size:12px;font-weight:700;letter-spacing:.16em;text-transform:uppercase}.mark{display:inline-grid;width:36px;height:36px;place-items:center;margin-right:9px;border-radius:11px;background:#386be8;color:white;font-weight:800}
h1{margin:0 0 10px;font-size:clamp(27px,5vw,40px);letter-spacing:-.04em}.sub{margin:0;color:#b9c7db}.card{margin-top:28px;padding:18px 20px;border:1px solid #324761;border-radius:12px;background:#111d30}.label{color:#91a4bf;font-size:12px;text-transform:uppercase;letter-spacing:.1em}.value{margin-top:5px;overflow-wrap:anywhere;color:#edf5ff;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:14px}.footer{display:flex;align-items:center;gap:9px;margin-top:26px;color:#a7b8cf;font-size:13px}.dot{width:8px;height:8px;border-radius:50%;background:#55d4b2;box-shadow:0 0 14px #55d4b277}
</style></head><body><main><p class="eyebrow"><span class="mark">L</span>Private app mesh</p><h1>{{.Name}}</h1><p class="sub">A live preview served by <strong>{{.Peer}}</strong>.</p><section class="card"><div class="label">Requested path</div><div class="value">{{.Path}}</div><div class="label" style="margin-top:18px">Peer response time</div><div class="value">{{.Time}}</div></section><div class="footer"><span class="dot"></span>Delivered through Loom · encrypted peer stream</div></main></body></html>`))
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = page.Execute(w, struct{ Name, Peer, Path, Time string }{Name: name, Peer: node.Name, Path: r.URL.Path, Time: time.Now().UTC().Format(time.RFC3339)})
	})}
	a.listeners = append(a.listeners, listener)
	a.servers = append(a.servers, httpServer)
	go func() { _ = httpServer.Serve(listener) }()
	record := ServiceRecord{ID: id, Name: name, Target: target}
	node.SetService(record)
	if node == a.main {
		a.services[id] = record
	}
	_ = deviceName
	return nil
}

func (a *App) prepareDemoFolder(target *Node) error {
	source := filepath.Join(a.dataDir, "demo-source")
	if err := os.MkdirAll(source, 0700); err != nil {
		return err
	}
	fixtures := map[string]string{
		"welcome.txt":      "Loom demo folder sync: this file was copied over an encrypted libp2p stream.\n",
		"tokens.css":       ":root { --loom-blue: #386be8; --loom-ink: #182235; }\n",
		"notes/design.txt": "Shared workspace sample assets. Change these files to see real folder transfer.\n",
	}
	for name, contents := range fixtures {
		file := filepath.Join(source, filepath.FromSlash(name))
		if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
				return err
			}
		}
	}
	folderID := "demo-shared-assets"
	if !isID(folderID) {
		return fmt.Errorf("invalid demo folder identifier")
	}
	folder := Folder{ID: folderID, Name: "Shared demo assets", Path: source, PeerID: target.Host.ID().String(), PeerName: target.Name, Status: "pending", Progress: 0}
	a.mu.Lock()
	entry := a.folders[folderID]
	if entry == nil {
		entry = &folderEntry{Folder: folder, manifest: make(map[string]string)}
		a.folders[folderID] = entry
	}
	shouldSync := a.connected && !entry.Folder.Paused
	if entry.Folder.Paused {
		entry.Folder.Status = "paused"
		entry.Folder.Error = ""
	} else if !a.connected {
		entry.Folder.Status = "offline"
		entry.Folder.Error = "network is paused"
	}
	_ = a.persistLocked()
	a.mu.Unlock()
	if shouldSync {
		if err := a.syncFolder(context.Background(), folderID); err != nil {
			return fmt.Errorf("initial demo folder sync: %w", err)
		}
	}
	return nil
}

func parsePeerID(id string) (peer.ID, error) {
	parsed, err := peer.Decode(id)
	if err != nil {
		return "", fmt.Errorf("invalid peer id")
	}
	return parsed, nil
}

func sortedStrings(values []string) {
	sort.Strings(values)
}

var _ = io.EOF
