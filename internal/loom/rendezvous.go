package loom

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	rendezvousProtocol = "/loom/rendezvous/1.0.0"
	defaultTTL         = 180 * time.Second
	maxTopicLen        = 64
	maxAddrsPerPeer    = 16
)

// RendezvousTopic derives a blinded, one-way 32-character hex topic from a private network key.
// The public relay learns only this topic hash and can never recover the original network key.
func RendezvousTopic(networkKey []byte) string {
	hash := sha256.Sum256(append([]byte("loom-rendezvous-v1:"), networkKey...))
	return hex.EncodeToString(hash[:16])
}

// RendezvousPeer represents peer identification and reachable multiaddresses.
type RendezvousPeer struct {
	ID    string   `json:"id"`
	Addrs []string `json:"addrs"`
	Data  string   `json:"data,omitempty"`
}

// RendezvousMessage is the request/response frame for the rendezvous protocol.
type RendezvousMessage struct {
	Type  string           `json:"type"` // "register", "register_ack", "discover", "discover_resp"
	Topic string           `json:"topic"`
	Peers []RendezvousPeer `json:"peers,omitempty"`
	Data  string           `json:"data,omitempty"`
	TTL   int              `json:"ttl,omitempty"` // in seconds
	Limit int              `json:"limit,omitempty"`
	OK    bool             `json:"ok,omitempty"`
	Error string           `json:"error,omitempty"`
}

type rendezvousEntry struct {
	id        peer.ID
	addrs     []ma.Multiaddr
	data      string
	expiresAt time.Time
}

// RendezvousStore manages topic registrations in memory with automatic expiration.
type RendezvousStore struct {
	mu      sync.RWMutex
	topics  map[string]map[peer.ID]*rendezvousEntry
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

// NewRendezvousStore creates a new in-memory rendezvous directory with a background reaper.
func NewRendezvousStore() *RendezvousStore {
	s := &RendezvousStore{
		topics: make(map[string]map[peer.ID]*rendezvousEntry),
		done:   make(chan struct{}),
	}
	go s.reaperLoop()
	return s
}

func (s *RendezvousStore) reaperLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.PruneExpired()
		}
	}
}

// Register adds or refreshes a peer registration under a specific topic.
func (s *RendezvousStore) Register(topic string, id peer.ID, addrs []ma.Multiaddr, ttl time.Duration, data ...string) {
	if topic == "" || len(topic) > maxTopicLen || len(addrs) == 0 {
		return
	}
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = defaultTTL
	}
	cleanAddrs := make([]ma.Multiaddr, 0, len(addrs))
	for _, a := range addrs {
		if a != nil {
			cleanAddrs = append(cleanAddrs, a)
			if len(cleanAddrs) >= maxAddrsPerPeer {
				break
			}
		}
	}
	if len(cleanAddrs) == 0 {
		return
	}

	payloadData := ""
	if len(data) > 0 {
		payloadData = data[0]
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entries, ok := s.topics[topic]
	if !ok {
		entries = make(map[peer.ID]*rendezvousEntry)
		s.topics[topic] = entries
	}
	entries[id] = &rendezvousEntry{
		id:        id,
		addrs:     cleanAddrs,
		data:      payloadData,
		expiresAt: time.Now().Add(ttl),
	}
}

// DiscoverEntries returns active, unexpired entries registered under a topic (excluding the caller).
func (s *RendezvousStore) DiscoverEntries(topic string, limit int, exclude peer.ID) []rendezvousEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, ok := s.topics[topic]
	if !ok || len(entries) == 0 {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	now := time.Now()
	result := make([]rendezvousEntry, 0, min(len(entries), limit))
	for id, entry := range entries {
		if id == exclude || entry.expiresAt.Before(now) {
			continue
		}
		result = append(result, *entry)
		if len(result) >= limit {
			break
		}
	}
	return result
}

// Discover returns active, unexpired peers registered under a topic (excluding the caller).
func (s *RendezvousStore) Discover(topic string, limit int, exclude peer.ID) []peer.AddrInfo {
	entries := s.DiscoverEntries(topic, limit, exclude)
	result := make([]peer.AddrInfo, 0, len(entries))
	for _, entry := range entries {
		result = append(result, peer.AddrInfo{
			ID:    entry.id,
			Addrs: entry.addrs,
		})
	}
	return result
}

// PruneExpired removes all expired entries and empty topic buckets.
func (s *RendezvousStore) PruneExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for topic, entries := range s.topics {
		for id, entry := range entries {
			if entry.expiresAt.Before(now) {
				delete(entries, id)
			}
		}
		if len(entries) == 0 {
			delete(s.topics, topic)
		}
	}
}

// Close stops the background expiration reaper.
func (s *RendezvousStore) Close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}

// Attach registers the rendezvous protocol handler on a libp2p host.
func (s *RendezvousStore) Attach(h host.Host) {
	h.SetStreamHandler(rendezvousProtocol, s.handleStream)
}

func (s *RendezvousStore) handleStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))

	reader := bufio.NewReader(stream)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}

	var req RendezvousMessage
	if err := json.Unmarshal(line, &req); err != nil {
		_ = json.NewEncoder(stream).Encode(RendezvousMessage{
			Type:  "error",
			OK:    false,
			Error: "invalid json format",
		})
		return
	}

	remotePeer := stream.Conn().RemotePeer()

	switch req.Type {
	case "register":
		if req.Topic == "" || len(req.Topic) > maxTopicLen {
			_ = json.NewEncoder(stream).Encode(RendezvousMessage{
				Type:  "register_ack",
				OK:    false,
				Error: "invalid topic",
			})
			return
		}
		var parsedAddrs []ma.Multiaddr
		for _, raw := range req.Peers {
			if raw.ID == remotePeer.String() || raw.ID == "" {
				for _, a := range raw.Addrs {
					if m, err := ma.NewMultiaddr(a); err == nil {
						parsedAddrs = append(parsedAddrs, m)
					}
				}
			}
		}
		// If caller provided no addrs, derive from the inbound connection
		if len(parsedAddrs) == 0 {
			parsedAddrs = append(parsedAddrs, stream.Conn().RemoteMultiaddr())
		}
		ttl := time.Duration(req.TTL) * time.Second
		if ttl <= 0 {
			ttl = defaultTTL
		}
		s.Register(req.Topic, remotePeer, parsedAddrs, ttl, req.Data)

		_ = json.NewEncoder(stream).Encode(RendezvousMessage{
			Type:  "register_ack",
			Topic: req.Topic,
			OK:    true,
		})

	case "discover":
		entries := s.DiscoverEntries(req.Topic, req.Limit, remotePeer)
		wirePeers := make([]RendezvousPeer, 0, len(entries))
		for _, e := range entries {
			addrStrs := make([]string, 0, len(e.addrs))
			for _, a := range e.addrs {
				addrStrs = append(addrStrs, a.String())
			}
			wirePeers = append(wirePeers, RendezvousPeer{
				ID:    e.id.String(),
				Addrs: addrStrs,
				Data:  e.data,
			})
		}
		_ = json.NewEncoder(stream).Encode(RendezvousMessage{
			Type:  "discover_resp",
			Topic: req.Topic,
			Peers: wirePeers,
			OK:    true,
		})

	default:
		_ = json.NewEncoder(stream).Encode(RendezvousMessage{
			Type:  "error",
			OK:    false,
			Error: fmt.Sprintf("unknown rendezvous message type %q", req.Type),
		})
	}
}

// RegisterPeer registers the local host with a remote rendezvous server under a specific topic.
func RegisterPeer(ctx context.Context, h host.Host, relayID peer.ID, topic string, ttl time.Duration) error {
	return RegisterPeerWithData(ctx, h, relayID, topic, ttl, "")
}

// RegisterPeerWithData registers the local host and arbitrary payload (e.g. encrypted voucher) under a topic.
func RegisterPeerWithData(ctx context.Context, h host.Host, relayID peer.ID, topic string, ttl time.Duration, dataStr string) error {
	stream, err := h.NewStream(ctx, relayID, rendezvousProtocol)
	if err != nil {
		return fmt.Errorf("open rendezvous stream: %w", err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	var addrs []string
	for _, a := range h.Addrs() {
		addrs = append(addrs, a.String())
	}

	req := RendezvousMessage{
		Type:  "register",
		Topic: topic,
		Peers: []RendezvousPeer{
			{
				ID:    h.ID().String(),
				Addrs: addrs,
				Data:  dataStr,
			},
		},
		Data: dataStr,
		TTL:  int(ttl.Seconds()),
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := stream.Write(data); err != nil {
		return fmt.Errorf("send register request: %w", err)
	}

	reader := bufio.NewReader(stream)
	respLine, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read register response: %w", err)
	}

	var resp RendezvousMessage
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return fmt.Errorf("decode register response: %w", err)
	}
	if !resp.OK {
		return errors.New("rendezvous register failed: " + resp.Error)
	}
	return nil
}

// DiscoverPeers queries a rendezvous server for peers registered under a specific topic.
func DiscoverPeers(ctx context.Context, h host.Host, relayID peer.ID, topic string, limit int) ([]peer.AddrInfo, error) {
	raw, err := DiscoverPeersWithData(ctx, h, relayID, topic, limit)
	if err != nil {
		return nil, err
	}
	var peers []peer.AddrInfo
	for _, rp := range raw {
		id, err := peer.Decode(rp.ID)
		if err != nil || id == h.ID() {
			continue
		}
		var parsedAddrs []ma.Multiaddr
		for _, a := range rp.Addrs {
			if m, err := ma.NewMultiaddr(a); err == nil {
				parsedAddrs = append(parsedAddrs, m)
			}
		}
		peers = append(peers, peer.AddrInfo{
			ID:    id,
			Addrs: parsedAddrs,
		})
	}
	return peers, nil
}

// DiscoverPeersWithData queries a rendezvous server returning raw RendezvousPeers including attached Data payloads.
func DiscoverPeersWithData(ctx context.Context, h host.Host, relayID peer.ID, topic string, limit int) ([]RendezvousPeer, error) {
	stream, err := h.NewStream(ctx, relayID, rendezvousProtocol)
	if err != nil {
		return nil, fmt.Errorf("open rendezvous stream: %w", err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	req := RendezvousMessage{
		Type:  "discover",
		Topic: topic,
		Limit: limit,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := stream.Write(data); err != nil {
		return nil, fmt.Errorf("send discover request: %w", err)
	}

	reader := bufio.NewReader(stream)
	respLine, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read discover response: %w", err)
	}

	var resp RendezvousMessage
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, fmt.Errorf("decode discover response: %w", err)
	}
	if !resp.OK {
		return nil, errors.New("rendezvous discover failed: " + resp.Error)
	}

	return resp.Peers, nil
}
