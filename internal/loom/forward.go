package loom

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const tcpForwardProtocol = "/loom/tcp/1.0.0"

// TCPForwardHandshake is sent by the initiator to identify which service or target to connect to.
type TCPForwardHandshake struct {
	ServiceID string `json:"serviceId,omitempty"`
	Target    string `json:"target,omitempty"` // loopback target: "127.0.0.1:5432" or "localhost:5432"
}

// TCPForwardResponse is sent back by the responder before bidirectional raw streaming starts.
type TCPForwardResponse struct {
	Target string `json:"target,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// Forward represents an active local port-forwarding listener.
type Forward struct {
	ID        string `json:"id"`
	PeerID    string `json:"peerId"`
	PeerName  string `json:"peerName,omitempty"`
	ServiceID string `json:"serviceId,omitempty"`
	Target    string `json:"target"`
	LocalPort int    `json:"localPort"`
	Status    string `json:"status"` // "active", "stopped", "error"
	Error     string `json:"error,omitempty"`

	listener net.Listener       `json:"-"`
	ctx      context.Context    `json:"-"`
	cancel   context.CancelFunc `json:"-"`
}

// ForwardManager handles local TCP listener tunnels into remote peer targets.
type ForwardManager struct {
	mu       sync.RWMutex
	forwards map[string]*Forward
	node     *Node
	getPeer  func(id string) (string, bool) // returns peerName, online
}

// NewForwardManager creates a new ForwardManager attached to a Node.
func NewForwardManager(node *Node, getPeer func(id string) (string, bool)) *ForwardManager {
	return &ForwardManager{
		forwards: make(map[string]*Forward),
		node:     node,
		getPeer:  getPeer,
	}
}

// StartForward binds a local TCP port and tunnels inbound connections through a libp2p stream to the peer.
func (fm *ForwardManager) StartForward(peerIDStr, serviceID, target string, requestedPort int) (*Forward, error) {
	if serviceID == "" || target != "" {
		return nil, errors.New("select an explicitly published TCP service; raw targets are not allowed")
	}
	if requestedPort < 0 || requestedPort > 65535 {
		return nil, errors.New("local port must be between 0 and 65535")
	}
	if fm.node == nil {
		return nil, errors.New("node is not initialized")
	}
	targetPeerID, err := peer.Decode(peerIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid peer id: %w", err)
	}

	if !fm.node.enabled.Load() {
		return nil, errNetworkPaused
	}
	if fm.getPeer != nil {
		if _, online := fm.getPeer(peerIDStr); !online {
			return nil, errors.New("selected device is offline")
		}
	}
	// Confirm the peer's publication and permission before reporting success.
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	check, err := fm.node.Host.NewStream(network.WithAllowLimitedConn(checkCtx, "loom TCP forward check"), targetPeerID, tcpForwardProtocol)
	if err != nil {
		checkCancel()
		return nil, err
	}
	_ = check.SetDeadline(time.Now().Add(8 * time.Second))
	err = writeLengthPrefixedJSON(check, TCPForwardHandshake{ServiceID: serviceID})
	var response TCPForwardResponse
	if err == nil {
		err = readLengthPrefixedJSON(check, &response, 4<<10)
	}
	_ = check.Close()
	checkCancel()
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, errors.New(response.Error)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", requestedPort))
	if err != nil {
		return nil, fmt.Errorf("bind local port %d: %w", requestedPort, err)
	}

	allocatedPort := listener.Addr().(*net.TCPAddr).Port
	id, err := randomSuffix()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	peerName := peerIDStr
	if fm.getPeer != nil {
		if name, ok := fm.getPeer(peerIDStr); ok && name != "" {
			peerName = name
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	fwd := &Forward{
		ID:        id,
		PeerID:    peerIDStr,
		PeerName:  peerName,
		ServiceID: serviceID,
		Target:    response.Target,
		LocalPort: allocatedPort,
		Status:    "active",
		listener:  listener,
		ctx:       ctx,
		cancel:    cancel,
	}

	fm.mu.Lock()
	fm.forwards[id] = fwd
	fm.mu.Unlock()

	go fm.acceptLoop(fwd, targetPeerID)
	return fwd, nil
}

func (fm *ForwardManager) acceptLoop(fwd *Forward, targetPeer peer.ID) {
	defer func() {
		_ = fwd.listener.Close()
		fm.mu.Lock()
		if fwd.Status == "active" {
			fwd.Status = "stopped"
		}
		fm.mu.Unlock()
	}()

	for {
		clientConn, err := fwd.listener.Accept()
		if err != nil {
			select {
			case <-fwd.ctx.Done():
				return
			default:
				return
			}
		}

		go fm.handleClientConnection(fwd, targetPeer, clientConn)
	}
}

func (fm *ForwardManager) handleClientConnection(fwd *Forward, targetPeer peer.ID, clientConn net.Conn) {
	defer clientConn.Close()

	if !fm.node.enabled.Load() {
		return
	}

	streamCtx, cancel := context.WithTimeout(fwd.ctx, 15*time.Second)
	defer cancel()

	stream, err := fm.node.Host.NewStream(network.WithAllowLimitedConn(streamCtx, "loom TCP forward"), targetPeer, tcpForwardProtocol)
	if err != nil {
		return
	}
	defer stream.Close()
	stop := context.AfterFunc(fwd.ctx, func() { _ = clientConn.Close(); _ = stream.Reset() })
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	handshake := TCPForwardHandshake{
		ServiceID: fwd.ServiceID,
		Target:    "",
	}
	if err := writeLengthPrefixedJSON(stream, handshake); err != nil {
		return
	}

	var resp TCPForwardResponse
	if err := readLengthPrefixedJSON(stream, &resp, 4<<10); err != nil || !resp.OK {
		return
	}

	_ = stream.SetDeadline(time.Time{})
	bridgeConns(clientConn, stream)
}

// bridgeConns copies data bidirectionally between two ReadWriteClosers until one side closes or errors.
func bridgeConns(c1, c2 io.ReadWriteCloser) {
	var closed atomic.Bool
	closeBoth := func() {
		if closed.CompareAndSwap(false, true) {
			_ = c1.Close()
			_ = c2.Close()
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer closeBoth()
		_, _ = io.Copy(c1, c2)
	}()

	go func() {
		defer wg.Done()
		defer closeBoth()
		_, _ = io.Copy(c2, c1)
	}()

	wg.Wait()
}

// StopForward stops and removes an active local port-forwarding listener.
func (fm *ForwardManager) StopForward(id string) error {
	fm.mu.Lock()
	fwd, ok := fm.forwards[id]
	if !ok {
		fm.mu.Unlock()
		return fmt.Errorf("forward %q not found", id)
	}
	delete(fm.forwards, id)
	fm.mu.Unlock()

	fwd.cancel()
	_ = fwd.listener.Close()
	return nil
}

// ListForwards returns a copy of all registered forwards.
func (fm *ForwardManager) ListForwards() []*Forward {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	list := make([]*Forward, 0, len(fm.forwards))
	for _, f := range fm.forwards {
		copyFwd := *f
		list = append(list, &copyFwd)
	}
	return list
}

// Close stops all active forward listeners.
func (fm *ForwardManager) Close() {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	for _, fwd := range fm.forwards {
		fwd.cancel()
		_ = fwd.listener.Close()
		fwd.Status = "stopped"
	}
	fm.forwards = make(map[string]*Forward)
}

// handleTCPForward handles incoming /loom/tcp/1.0.0 streams on the destination peer.
func (n *Node) handleTCPForward(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	if !n.enabled.Load() {
		_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{OK: false, Error: "network is paused"})
		return
	}

	var handshake TCPForwardHandshake
	if err := readLengthPrefixedJSON(stream, &handshake, 4<<10); err != nil {
		_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{OK: false, Error: "invalid handshake"})
		return
	}

	var dialTarget string

	if handshake.ServiceID != "" {
		n.mu.RLock()
		service, ok := n.services[handshake.ServiceID]
		n.mu.RUnlock()
		if !ok || serviceProtocol(service) != "tcp" {
			_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{OK: false, Error: "service not found"})
			return
		}
		remotePeer := stream.Conn().RemotePeer().String()
		if len(service.AllowedPeers) > 0 && !containsPeer(service.AllowedPeers, remotePeer) {
			_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{OK: false, Error: "service access denied for this device"})
			return
		}
		extracted, err := extractHostPort(service.Target)
		if err != nil {
			_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{OK: false, Error: "invalid service target"})
			return
		}
		dialTarget = extracted
	} else {
		_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{Error: "an explicitly published TCP service is required"})
		return
	}
	if handshake.Target != "" {
		_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{Error: "raw targets are not allowed"})
		return
	}

	targetConn, err := net.DialTimeout("tcp", dialTarget, 5*time.Second)
	if err != nil {
		_ = writeLengthPrefixedJSON(stream, TCPForwardResponse{OK: false, Error: fmt.Sprintf("dial target error: %v", err)})
		return
	}
	defer targetConn.Close()

	if err := writeLengthPrefixedJSON(stream, TCPForwardResponse{OK: true, Target: dialTarget}); err != nil {
		return
	}

	_ = stream.SetDeadline(time.Time{})
	bridgeConns(targetConn, stream)
}

func extractHostPort(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		raw = u.Host
	}

	host, portStr, err := net.SplitHostPort(raw)
	if err != nil {
		// Might just be a port number like "5432"
		if p, pErr := strconv.Atoi(raw); pErr == nil && p > 0 && p < 65536 {
			return fmt.Sprintf("127.0.0.1:%d", p), nil
		}
		return "", fmt.Errorf("invalid host:port: %w", err)
	}

	// Ensure destination host is loopback only for security
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "" {
		host = "127.0.0.1"
	} else {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", errors.New("only loopback targets are permitted")
		}
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid TCP port")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func writeLengthPrefixedJSON(w io.Writer, val any) error {
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readLengthPrefixedJSON(r io.Reader, val any, maxBytes uint32) error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length > maxBytes {
		return fmt.Errorf("payload length %d exceeds maximum %d", length, maxBytes)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, val)
}
