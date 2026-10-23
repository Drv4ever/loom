package loom

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestGenericTCPPortForwarding(t *testing.T) {
	key := testNetworkKey(14)
	nodeADir := filepath.Join(t.TempDir(), "nodeA")
	nodeBDir := filepath.Join(t.TempDir(), "nodeB")

	nodeA := newTestNode(t, "nodeA", nodeADir, key)
	nodeB := newTestNode(t, "nodeB", nodeBDir, key)
	defer nodeA.Close()
	defer nodeB.Close()

	// Start a simulated backend TCP server on Node B (e.g. listening on 127.0.0.1)
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start echo server: %v", err)
	}
	defer echoListener.Close()

	echoPort := echoListener.Addr().(*net.TCPAddr).Port

	// Echo server: reads line, sends back "ECHO: " + line
	go func() {
		for {
			conn, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					msg := string(buf[:n])
					reply := "ECHO: " + msg
					if _, err := c.Write([]byte(reply)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	// Register the service on Node B
	serviceID := "echo-service"
	nodeB.mu.Lock()
	nodeB.services[serviceID] = ServiceRecord{
		Protocol: "tcp",
		ID:       serviceID,
		Name:     "Echo Service",
		Target:   fmt.Sprintf("127.0.0.1:%d", echoPort),
	}
	nodeB.mu.Unlock()

	// Connect Node A and Node B
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := nodeA.Host.Connect(ctx, peer.AddrInfo{ID: nodeB.Host.ID(), Addrs: nodeB.Host.Addrs()}); err != nil {
		t.Fatalf("connect nodeA to nodeB: %v", err)
	}

	// Create ForwardManager on Node A
	fwdMgr := NewForwardManager(nodeA, func(id string) (string, bool) {
		if id == nodeB.Host.ID().String() {
			return "nodeB", true
		}
		return "", false
	})
	defer fwdMgr.Close()

	// Start port forwarding on Node A pointing to service on Node B
	fwd, err := fwdMgr.StartForward(nodeB.Host.ID().String(), serviceID, "", 0)
	if err != nil {
		t.Fatalf("StartForward: %v", err)
	}

	if fwd.LocalPort <= 0 {
		t.Fatalf("expected allocated local port, got %d", fwd.LocalPort)
	}

	// Test connecting locally to Node A's forward port
	clientConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", fwd.LocalPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial forward port: %v", err)
	}
	defer clientConn.Close()

	testMsg := "Hello Loom Mesh!\n"
	if _, err := clientConn.Write([]byte(testMsg)); err != nil {
		t.Fatalf("write to forward port: %v", err)
	}

	readBuf := make([]byte, 1024)
	n, err := clientConn.Read(readBuf)
	if err != nil {
		t.Fatalf("read from forward port: %v", err)
	}

	expected := "ECHO: " + testMsg
	if got := string(readBuf[:n]); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}

	// Test listing forwards
	list := fwdMgr.ListForwards()
	if len(list) != 1 || list[0].ID != fwd.ID {
		t.Fatalf("unexpected list forwards: %+v", list)
	}

	// Test stopping forward
	if err := fwdMgr.StopForward(fwd.ID); err != nil {
		t.Fatalf("stop forward: %v", err)
	}

	// Existing client sessions must close as well as the listening port.
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	_, activeErr := clientConn.Read(make([]byte, 1))
	if activeErr == nil {
		t.Fatal("stopped forward kept an active session open")
	}
	if timeout, ok := activeErr.(net.Error); ok && timeout.Timeout() {
		t.Fatal("active session did not close when forward stopped")
	}
	// Verify listener is closed
	time.Sleep(50 * time.Millisecond)
	_, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", fwd.LocalPort), 100*time.Millisecond)
	if dialErr == nil {
		t.Fatalf("expected dial to fail after stop forward")
	}
}

func TestTCPForwardDirectTargetValidation(t *testing.T) {
	// Loopback targets should succeed
	h1, err := extractHostPort("127.0.0.1:5432")
	if err != nil || h1 != "127.0.0.1:5432" {
		t.Fatalf("extractHostPort(127.0.0.1:5432) = %q, err=%v", h1, err)
	}

	h2, err := extractHostPort("localhost:6379")
	if err != nil || h2 != "127.0.0.1:6379" {
		t.Fatalf("extractHostPort(localhost:6379) = %q, err=%v", h2, err)
	}

	h3, err := extractHostPort("http://127.0.0.1:8080/path")
	if err != nil || h3 != "127.0.0.1:8080" {
		t.Fatalf("extractHostPort(http URL) = %q, err=%v", h3, err)
	}

	h4, err := extractHostPort("5432")
	if err != nil || h4 != "127.0.0.1:5432" {
		t.Fatalf("extractHostPort(port only) = %q, err=%v", h4, err)
	}

	// Non-loopback external target should be rejected
	_, errExternal := extractHostPort("8.8.8.8:53")
	if errExternal == nil || !strings.Contains(errExternal.Error(), "loopback") {
		t.Fatalf("expected non-loopback rejection, got %v", errExternal)
	}
}
