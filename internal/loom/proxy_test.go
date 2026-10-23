package loom

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestProxyLargeStreamingPayload(t *testing.T) {
	key := testNetworkKey(15)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)
	defer nodeA.Close()
	defer nodeB.Close()

	// 12 MiB payload (larger than old 10 MiB limit)
	const payloadSize = 12 * 1024 * 1024
	testPayload := make([]byte, payloadSize)
	for i := range testPayload {
		testPayload[i] = byte(i % 251)
	}
	expectedHash := sha256.Sum256(testPayload)

	// Target backend on Node B
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		receivedHash := sha256.Sum256(body)
		if receivedHash != expectedHash {
			http.Error(w, "checksum mismatch", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body) // Echo the 12 MiB back
	}))
	defer backend.Close()

	serviceID := "streaming-service"
	nodeB.mu.Lock()
	nodeB.services[serviceID] = ServiceRecord{
		ID:     serviceID,
		Name:   "Streaming Service",
		Target: backend.URL,
	}
	nodeB.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA to nodeB: %v", err)
	}

	appA := &App{
		main:      nodeA,
		connected: true,
		peers: map[string]*peerEntry{
			nodeB.Host.ID().String(): {
				Device:   Device{ID: nodeB.Host.ID().String(), Online: true},
				Services: []ServiceRecord{{ID: serviceID, Name: "Streaming Service", Target: backend.URL}},
			},
		},
		services: make(map[string]ServiceRecord),
	}

	handler := aHandler(appA)
	proxyURL := fmt.Sprintf("/proxy/%s/%s/stream", nodeB.Host.ID().String(), serviceID)

	req := httptest.NewRequest(http.MethodPost, proxyURL, bytes.NewReader(testPayload))
	req.Host = "localhost:8787"
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	if len(respBody) != payloadSize {
		t.Fatalf("expected %d bytes, got %d", payloadSize, len(respBody))
	}
	respHash := sha256.Sum256(respBody)
	if respHash != expectedHash {
		t.Fatalf("response hash mismatch")
	}
}

func TestProxyTrustedHeadersAndCSP(t *testing.T) {
	key := testNetworkKey(16)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)
	defer nodeA.Close()
	defer nodeB.Close()

	var receivedAuth, receivedCookie, receivedOrigin string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedCookie = r.Header.Get("Cookie")
		receivedOrigin = r.Header.Get("Origin")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	untrustedID := "untrusted-srv"
	trustedID := "trusted-srv"

	nodeB.mu.Lock()
	nodeB.services[untrustedID] = ServiceRecord{ID: untrustedID, Name: "Untrusted", Target: backend.URL, Trusted: false}
	nodeB.services[trustedID] = ServiceRecord{ID: trustedID, Name: "Trusted", Target: backend.URL, Trusted: true}
	nodeB.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	appA := &App{
		main:      nodeA,
		connected: true,
		peers: map[string]*peerEntry{
			nodeB.Host.ID().String(): {
				Device: Device{ID: nodeB.Host.ID().String(), Online: true},
				Services: []ServiceRecord{
					{ID: untrustedID, Name: "Untrusted", Target: backend.URL, Trusted: false},
					{ID: trustedID, Name: "Trusted", Target: backend.URL, Trusted: true},
				},
			},
		},
		services: make(map[string]ServiceRecord),
	}
	handler := aHandler(appA)

	// 1. Untrusted: headers stripped and CSP sandbox applied
	receivedAuth, receivedCookie, receivedOrigin = "", "", ""
	req1 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/proxy/%s/%s/test", nodeB.Host.ID().String(), untrustedID), nil)
	req1.Host = "localhost:8787"
	req1.Header.Set("Authorization", "Bearer secret123")
	req1.Header.Set("Cookie", "session=abc")
	req1.Header.Set("Origin", "http://attacker.com")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("untrusted request failed: %d", rec1.Code)
	}
	if receivedAuth != "" || receivedCookie != "" || receivedOrigin != "" {
		t.Fatalf("untrusted mode should strip auth/cookie/origin, got auth=%q cookie=%q origin=%q", receivedAuth, receivedCookie, receivedOrigin)
	}
	if csp := rec1.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatalf("untrusted mode should set sandbox CSP, got %q", csp)
	}

	// 2. Trusted: headers preserved and no sandbox CSP
	receivedAuth, receivedCookie, receivedOrigin = "", "", ""
	req2 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/proxy/%s/%s/test", nodeB.Host.ID().String(), trustedID), nil)
	req2.Host = "localhost:8787"
	req2.Header.Set("Authorization", "Bearer secret123")
	req2.Header.Set("Cookie", "session=abc")
	req2.Header.Set("Origin", "http://my-trusted-app.local")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("trusted request failed: %d", rec2.Code)
	}
	if receivedAuth != "Bearer secret123" {
		t.Fatalf("trusted mode should preserve Authorization, got %q", receivedAuth)
	}
	if receivedCookie != "session=abc" {
		t.Fatalf("trusted mode should preserve Cookie, got %q", receivedCookie)
	}
	if receivedOrigin != "http://my-trusted-app.local" {
		t.Fatalf("trusted mode should preserve Origin, got %q", receivedOrigin)
	}
	if csp := rec2.Header().Get("Content-Security-Policy"); strings.Contains(csp, "sandbox") {
		t.Fatalf("trusted mode should not force sandbox CSP, got %q", csp)
	}
}

func TestProxyWebSocketUpgrade(t *testing.T) {
	key := testNetworkKey(17)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)
	defer nodeA.Close()
	defer nodeB.Close()

	// Mock WebSocket server: accepts Upgrade request, returns 101 Switching Protocols, then echoes frames
	wsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer wsListener.Close()

	go func() {
		for {
			conn, err := wsListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				req, err := http.ReadRequest(r)
				if err != nil {
					return
				}
				if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
					resp := "HTTP/1.1 400 Bad Request\r\n\r\n"
					_, _ = c.Write([]byte(resp))
					return
				}
				// Send 101 Switching Protocols handshake response
				handshake := "HTTP/1.1 101 Switching Protocols\r\n" +
					"Upgrade: websocket\r\n" +
					"Connection: Upgrade\r\n" +
					"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"
				if _, err := c.Write([]byte(handshake)); err != nil {
					return
				}
				// Echo payload back
				buf := make([]byte, 512)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write([]byte("WS-ECHO: " + string(buf[:n]))); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	wsPort := wsListener.Addr().(*net.TCPAddr).Port
	serviceID := "ws-service"
	nodeB.mu.Lock()
	nodeB.services[serviceID] = ServiceRecord{
		ID:     serviceID,
		Name:   "WS Service",
		Target: fmt.Sprintf("http://127.0.0.1:%d", wsPort),
	}
	nodeB.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	appA := &App{
		main:      nodeA,
		connected: true,
		peers: map[string]*peerEntry{
			nodeB.Host.ID().String(): {
				Device:   Device{ID: nodeB.Host.ID().String(), Online: true},
				Services: []ServiceRecord{{ID: serviceID, Name: "WS Service", Target: fmt.Sprintf("http://127.0.0.1:%d", wsPort)}},
			},
		},
		services: make(map[string]ServiceRecord),
	}

	// Start real HTTP listener for AppA to allow genuine connection hijacking
	server := httptest.NewServer(aHandler(appA))
	defer server.Close()

	// Dial real TCP connection to AppA's proxy endpoint
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	proxyPath := fmt.Sprintf("/proxy/%s/%s/ws", nodeB.Host.ID().String(), serviceID)
	handshakeReq := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: localhost\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", proxyPath)

	if _, err := conn.Write([]byte(handshakeReq)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", resp.StatusCode)
	}

	// Test bidirectional communication over the hijacked WebSocket connection
	testMsg := "Ping Loom WS!"
	if _, err := conn.Write([]byte(testMsg)); err != nil {
		t.Fatalf("write over ws: %v", err)
	}

	readBuf := make([]byte, 256)
	n, err := conn.Read(readBuf)
	if err != nil {
		t.Fatalf("read over ws: %v", err)
	}
	expected := "WS-ECHO: " + testMsg
	if got := string(readBuf[:n]); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func aHandler(a *App) http.Handler {
	return a.Handler("8787")
}
