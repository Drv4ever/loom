package loom

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	PeerListen string
	Relay      bool
	Relays     []string
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
	Folder    Folder            `json:"folder"`
	Manifest  map[string]string `json:"manifest"`
	Conflicts []Conflict        `json:"conflicts,omitempty"`
}

type persistedState struct {
	Name             string            `json:"name"`
	Connected        bool              `json:"connected"`
	Services         []ServiceRecord   `json:"services"`
	Folders          []persistedFolder `json:"folders"`
	TransferredBytes int64             `json:"transferredBytes"`
	Activity         []Activity        `json:"activity"`
	Peers            []peer.AddrInfo   `json:"peers"`
	Relays           []string          `json:"relays,omitempty"`
	BlockedPeers     []string          `json:"blockedPeers,omitempty"`
}

type App struct {
	mu               sync.RWMutex
	name             string
	networkID        string
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
	relays           []string
	blockedPeers     map[string]time.Time
	pairing          *PairingManager
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
	forwards         *ForwardManager
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
		name: config.Name, connected: true, dataDir: dataDir,
		statePath: filepath.Join(dataDir, "state.json"), networkKey: key, nodes: make(map[string]*Node),
		peers: make(map[string]*peerEntry), services: make(map[string]ServiceRecord),
		remoteServices: make(map[string][]ServiceRecord), forceRelay: make(map[string]bool), relayInfos: make(map[string]peer.AddrInfo),
		relays: make([]string, 0), blockedPeers: make(map[string]time.Time), pairing: NewPairingManager(),
		lastDial: make(map[string]time.Time), folders: make(map[string]*folderEntry),
		started: time.Now(), ctx: appCtx, cancel: cancel, watcherDone: make(chan struct{}), apiListen: config.Listen,
	}
	hash := sha256.Sum256(key)
	a.networkID = "loom-" + hex.EncodeToString(hash[:6])
	if a.name == "" {
		a.name = "Personal workspace"
	}
	mainDir := filepath.Join(dataDir, "node")
	node, err := NewNode(NodeOptions{
		Name: deviceName(config.Name), Platform: runtime.GOOS, DataDir: mainDir,
		IdentityPath: filepath.Join(dataDir, "identity.key"),
		ReceiveDir:   filepath.Join(dataDir, "received"), NetworkKey: key, ListenAddress: config.PeerListen, RelayService: config.Relay,
		IsBlocked: func(p peer.ID) bool {
			return a.IsPeerBlocked(p.String())
		},
	})
	if err != nil {
		return nil, err
	}
	a.main = node
	node.getCustomPath = func(folderID string) string {
		a.mu.RLock()
		defer a.mu.RUnlock()
		if f, ok := a.folders[folderID]; ok && f.Folder.Path != "" {
			return f.Folder.Path
		}
		return ""
	}
	node.checkFolderAccess = func(folderID, peerID string, isWrite bool) error {
		return a.checkFolderAccess(folderID, peerID, isWrite)
	}
	a.nodes[node.Host.ID().String()] = node
	a.loadPersisted()
	node.SetEnabled(a.connected)
	if err := node.StartMDNS(func(info peer.AddrInfo) {
		go a.connectPeer(info)
	}); err != nil {
		_ = node.Close()
		return nil, err
	}
	for _, r := range config.Relays {
		r = strings.TrimSpace(r)
		if r != "" {
			a.relays = append(a.relays, r)
		}
	}
	a.forwards = NewForwardManager(a.main, func(id string) (string, bool) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		if p, ok := a.peers[id]; ok {
			return p.Device.Name, p.Device.Online
		}
		return "", false
	})
	go a.refreshLoop()
	go a.syncLoop()
	go a.rendezvousLoop()
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
		if a.main != nil && a.main.conflicts != nil {
			for _, c := range savedFolder.Conflicts {
				a.main.conflicts.Add(c)
			}
			folder.Conflicts = len(savedFolder.Conflicts)
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
	for _, r := range saved.Relays {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		found := false
		for _, existing := range a.relays {
			if existing == r {
				found = true
				break
			}
		}
		if !found {
			a.relays = append(a.relays, r)
		}
	}
	for _, p := range saved.BlockedPeers {
		p = strings.TrimSpace(p)
		if p != "" {
			a.blockedPeers[p] = time.Now().UTC()
		}
	}
}

func (a *App) persistLocked() error {
	saved := persistedState{
		Name:             a.name,
		Connected:        a.connected,
		TransferredBytes: a.transferredBytes,
		Activity:         a.activities,
		Relays:           append([]string(nil), a.relays...),
		BlockedPeers:     a.listBlockedPeersLocked(),
	}
	for _, service := range a.services {
		saved.Services = append(saved.Services, service)
	}
	for _, entry := range a.folders {
		var conflicts []Conflict
		if a.main != nil && a.main.conflicts != nil {
			conflicts = a.main.conflicts.List(entry.Folder.ID)
		}
		saved.Folders = append(saved.Folders, persistedFolder{Folder: entry.Folder, Manifest: entry.manifest, Conflicts: conflicts})
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
		if a.main.IsRelayed(id) || strings.Contains(addr.String(), "p2p-circuit") {
			connection = "relay"
		}
		a.mu.RLock()
		forceRelay := a.forceRelay[idString]
		relayInfo := a.relayInfos[idString]
		a.mu.RUnlock()
		if forceRelay && connection != "relay" {
			// If forced relay is configured, disconnect direct link and keep the relay path.
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
	if a.IsPeerBlocked(info.ID.String()) {
		return fmt.Errorf("peer %s is blocked", info.ID.String())
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
	ctx = network.WithAllowLimitedConn(ctx, "loom peer connection")
	if forceRelay {
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
	folders := make([]Folder, 0, len(a.folders))
	for _, entry := range a.folders {
		folder := entry.Folder
		folder.Manifest = nil
		if a.main != nil && a.main.conflicts != nil {
			folder.Conflicts = len(a.main.conflicts.List(folder.ID))
		}
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
	relays := append([]string(nil), a.relays...)
	a.mu.RUnlock()
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Self != devices[j].Self {
			return devices[i].Self
		}
		left, right := strings.ToLower(devices[i].Name), strings.ToLower(devices[j].Name)
		if left != right {
			return left < right
		}
		return devices[i].ID < devices[j].ID
	})
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
	received := []ReceivedFolder{}
	if a.main != nil {
		received = a.main.ReceivedFolders()
	}
	for i := range received {
		for _, device := range devices {
			if device.ID == received[i].PeerID {
				received[i].PeerName = device.Name
			}
		}
	}
	forwards := []*Forward{}
	if a.forwards != nil {
		forwards = a.forwards.ListForwards()
	}
	return State{
		Network: NetworkState{ID: a.networkID, Name: name, Connected: connected, PeerID: func() string {
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
		Devices: devices, Services: services, Forwards: forwards, Folders: folders, ReceivedFolders: received, Relays: relays, BlockedDevices: a.ListBlockedPeers(), Activity: activity, Stats: stats,
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
	return Service{
		ID: record.ID, Name: record.Name, DeviceID: peerID, DeviceName: peerName,
		Protocol: "http", Target: record.Target, Port: port, Status: "published",
		URL: "/proxy/" + peerID + "/" + record.ID + "/", Trusted: record.Trusted,
		AllowedPeers: record.AllowedPeers,
	}
}

func firstAddress(addresses []string) string {
	if len(addresses) == 0 {
		return ""
	}
	return addresses[0]
}

func (a *App) Close(ctx context.Context) error {
	a.cancel()
	if a.forwards != nil {
		a.forwards.Close()
	}
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

// AddRelay registers a Circuit Relay v2 server multiaddress and triggers discovery.
func (a *App) AddRelay(address string) error {
	addr, err := ma.NewMultiaddr(address)
	if err != nil {
		return fmt.Errorf("invalid relay multiaddress: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return fmt.Errorf("relay address must include /p2p/<peer-id>: %w", err)
	}
	a.mu.Lock()
	for _, existing := range a.relays {
		if existing == address {
			a.mu.Unlock()
			return nil
		}
	}
	a.relays = append(a.relays, address)
	a.addActivityLocked("relay", "Relay added", fmt.Sprintf("Added relay server %s", shortID(info.ID.String())))
	_ = a.persistLocked()
	a.mu.Unlock()

	go a.syncRelay(address)
	return nil
}

// RemoveRelay unregisters a relay by multiaddress or peer ID.
func (a *App) RemoveRelay(target string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := -1
	for i, r := range a.relays {
		if r == target {
			idx = i
			break
		}
		if addr, err := ma.NewMultiaddr(r); err == nil {
			if info, err := peer.AddrInfoFromP2pAddr(addr); err == nil {
				if info.ID.String() == target {
					idx = i
					break
				}
			}
		}
	}
	if idx < 0 {
		return fmt.Errorf("relay not found: %s", target)
	}
	a.relays = append(a.relays[:idx], a.relays[idx+1:]...)
	a.addActivityLocked("relay", "Relay removed", fmt.Sprintf("Removed relay server %s", target))
	_ = a.persistLocked()
	return nil
}

// ListRelays returns the status of all configured relays.
func (a *App) ListRelays() []RelayInfo {
	a.mu.RLock()
	relays := append([]string(nil), a.relays...)
	a.mu.RUnlock()

	result := make([]RelayInfo, 0, len(relays))
	for _, r := range relays {
		info := RelayInfo{Address: r, Status: "configured"}
		addr, err := ma.NewMultiaddr(r)
		if err != nil {
			info.Status = "unreachable"
			info.Error = "invalid multiaddress"
			result = append(result, info)
			continue
		}
		pinfo, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			info.Status = "unreachable"
			info.Error = err.Error()
			result = append(result, info)
			continue
		}
		info.PeerID = pinfo.ID.String()
		if a.main != nil && a.main.Host.Network().Connectedness(pinfo.ID) == network.Connected {
			info.Status = "connected"
		}
		result = append(result, info)
	}
	return result
}

func (a *App) syncRelay(address string) {
	if a.main == nil {
		return
	}
	addr, err := ma.NewMultiaddr(address)
	if err != nil {
		return
	}
	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()

	if err := a.main.Host.Connect(ctx, *info); err != nil {
		return
	}

	_, _ = client.Reserve(ctx, a.main.Host, *info)

	topic := RendezvousTopic(a.networkKey)
	_ = RegisterPeer(ctx, a.main.Host, info.ID, topic, 2*time.Minute)

	discovered, err := DiscoverPeers(ctx, a.main.Host, info.ID, topic, 50)
	if err != nil {
		return
	}

	for _, p := range discovered {
		if p.ID == a.main.Host.ID() {
			continue
		}
		if len(p.Addrs) > 0 {
			a.main.Host.Peerstore().AddAddrs(p.ID, p.Addrs, 24*time.Hour)
		}
		circuitAddrStr := address + "/p2p-circuit/p2p/" + p.ID.String()
		if circuitMA, err := ma.NewMultiaddr(circuitAddrStr); err == nil {
			a.main.Host.Peerstore().AddAddrs(p.ID, []ma.Multiaddr{circuitMA}, 24*time.Hour)
		}

		if a.main.Host.Network().Connectedness(p.ID) != network.Connected {
			go func(pi peer.AddrInfo) {
				_ = a.connectPeer(pi)
			}(p)
		}
	}
}

func (a *App) syncRendezvous() {
	a.mu.RLock()
	relays := append([]string(nil), a.relays...)
	connected := a.connected
	a.mu.RUnlock()

	if !connected || a.main == nil {
		return
	}

	for _, relayAddr := range relays {
		a.syncRelay(relayAddr)
	}
}

func (a *App) rendezvousLoop() {
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(2 * time.Second):
		a.syncRendezvous()
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.syncRendezvous()
		}
	}
}

func (a *App) listBlockedPeersLocked() []string {
	res := make([]string, 0, len(a.blockedPeers))
	for p := range a.blockedPeers {
		res = append(res, p)
	}
	sort.Strings(res)
	return res
}

// ListBlockedPeers returns a sorted list of blocked peer IDs.
func (a *App) ListBlockedPeers() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.listBlockedPeersLocked()
}

// IsPeerBlocked checks whether a peer ID is in the blocklist.
func (a *App) IsPeerBlocked(peerID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.isPeerBlockedLocked(peerID)
}

func (a *App) isPeerBlockedLocked(peerID string) bool {
	if a.blockedPeers == nil {
		return false
	}
	_, blocked := a.blockedPeers[peerID]
	return blocked
}

// BlockPeer adds a peer ID to the blocklist, closes any active connections to it,
// clears its addresses from the peerstore, and persists the update.
func (a *App) BlockPeer(peerID string) error {
	pID, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("invalid peer id: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.blockedPeers == nil {
		a.blockedPeers = make(map[string]time.Time)
	}
	a.blockedPeers[peerID] = time.Now().UTC()
	if a.main != nil {
		_ = a.main.ClosePeer(pID)
		a.main.Host.Peerstore().ClearAddrs(pID)
	}
	for _, n := range a.nodes {
		_ = n.ClosePeer(pID)
		n.Host.Peerstore().ClearAddrs(pID)
	}
	if p, ok := a.peers[peerID]; ok {
		p.Device.Online = false
		p.Device.LastSeen = time.Now().UTC()
		p.Services = nil
		a.updateFoldersForPeerLocked(peerID, false)
	}
	a.addActivityLocked("device", "Device blocked", fmt.Sprintf("Device %s was blocked.", shortID(peerID)))
	return a.persistLocked()
}

// UnblockPeer removes a peer ID from the blocklist and persists the update.
func (a *App) UnblockPeer(peerID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.blockedPeers != nil {
		delete(a.blockedPeers, peerID)
	}
	a.addActivityLocked("device", "Device unblocked", fmt.Sprintf("Device %s was unblocked.", shortID(peerID)))
	return a.persistLocked()
}

// checkFolderAccess checks if a remote peer is permitted to access a folder.
func (a *App) checkFolderAccess(folderID, peerID string, isWrite bool) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.isPeerBlockedLocked(peerID) {
		return fmt.Errorf("peer %s is blocked", peerID)
	}
	entry, ok := a.folders[folderID]
	if !ok {
		return nil
	}
	if len(entry.Folder.AllowedPeers) > 0 {
		allowed := false
		for _, p := range entry.Folder.AllowedPeers {
			if p == peerID {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("peer %s is not authorized to access folder %s", peerID, folderID)
		}
	}
	if isWrite && entry.Folder.ReadOnly {
		return fmt.Errorf("folder %s is read-only", folderID)
	}
	return nil
}

// GeneratePairingCode initiates a time-limited 6-digit PIN pairing session.
func (a *App) GeneratePairingCode(duration time.Duration) (*PairingSession, error) {
	if a.pairing == nil {
		a.pairing = NewPairingManager()
	}
	if duration <= 0 {
		duration = 10 * time.Minute
	}
	a.mu.RLock()
	relays := append([]string(nil), a.relays...)
	var peerID string
	var peerAddrs []string
	if a.main != nil {
		peerID = a.main.Host.ID().String()
		peerAddrs = a.main.Addrs()
	}
	netID := a.networkID
	netName := a.name
	netKey := a.networkKey
	a.mu.RUnlock()

	session, err := a.pairing.CreateSessionWithOptions(netKey, netID, netName, peerID, relays, peerAddrs, duration)
	if err != nil {
		return nil, err
	}

	// Register with configured rendezvous relays in background
	if a.main != nil && len(relays) > 0 {
		go func(pin string, ttl time.Duration) {
			topic, err := PairingTopic(pin)
			if err != nil {
				return
			}
			enc, err := EncryptPairingPayload(session.Payload, pin)
			if err != nil {
				return
			}
			dataStr := hex.EncodeToString(enc)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for _, relayAddr := range relays {
				addr, err := ma.NewMultiaddr(relayAddr)
				if err != nil {
					continue
				}
				info, err := peer.AddrInfoFromP2pAddr(addr)
				if err != nil {
					continue
				}
				if a.main.Host.Network().Connectedness(info.ID) != network.Connected {
					_ = a.main.Host.Connect(ctx, *info)
				}
				_ = RegisterPeerWithData(ctx, a.main.Host, info.ID, topic, ttl, dataStr)
			}
		}(session.PIN, duration)
	}

	return session, nil
}

// GetPairingSession returns the current active pairing session, or nil if none.
func (a *App) GetPairingSession() *PairingSession {
	if a.pairing == nil {
		return nil
	}
	return a.pairing.CurrentSession()
}

// ClaimPairingCode redeems a 6-digit pairing PIN either locally or via circuit relays.
func (a *App) ClaimPairingCode(pin string, relayAddr string) (*PairingPayload, error) {
	cleaned, err := CleanPIN(pin)
	if err != nil {
		return nil, err
	}

	// 1. Check local session
	if a.pairing != nil {
		if payload, err := a.pairing.Claim(cleaned); err == nil && payload != nil {
			return payload, nil
		}
	}

	// 2. Discover via circuit relay
	a.mu.RLock()
	relays := append([]string(nil), a.relays...)
	a.mu.RUnlock()
	if relayAddr != "" {
		relays = append([]string{relayAddr}, relays...)
	}

	if len(relays) == 0 {
		return nil, fmt.Errorf("no relay configured to discover pairing session for PIN %s", FormatPIN(cleaned))
	}
	if a.main == nil {
		return nil, fmt.Errorf("loom node not initialized")
	}

	topic, err := PairingTopic(cleaned)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, r := range relays {
		addr, err := ma.NewMultiaddr(r)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			continue
		}
		if a.main.Host.Network().Connectedness(info.ID) != network.Connected {
			_ = a.main.Host.Connect(ctx, *info)
		}
		entries, err := DiscoverPeersWithData(ctx, a.main.Host, info.ID, topic, 10)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Data == "" {
				continue
			}
			encBytes, err := hex.DecodeString(entry.Data)
			if err != nil {
				continue
			}
			payload, err := DecryptPairingPayload(encBytes, cleaned)
			if err == nil && payload != nil {
				a.mu.Lock()
				a.addActivityLocked("pairing", "Device paired", fmt.Sprintf("Successfully paired with peer %s", shortID(payload.IssuedBy)))
				_ = a.persistLocked()
				a.mu.Unlock()
				return payload, nil
			}
		}
	}

	return nil, fmt.Errorf("pairing session not found or invalid PIN")
}

var _ = io.EOF
