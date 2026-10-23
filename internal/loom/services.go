package loom

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func serviceProtocol(record ServiceRecord) string {
	if record.Protocol == "" {
		return "http"
	}
	return record.Protocol
}
func validateService(record ServiceRecord) error {
	switch serviceProtocol(record) {
	case "http":
		if _, err := validateLoopbackHTTPURL(record.Target); err != nil {
			return fmt.Errorf("HTTP target must be a loopback HTTP URL")
		}
	case "tcp":
		if _, err := extractHostPort(record.Target); err != nil {
			return err
		}
	default:
		return fmt.Errorf("protocol must be http or tcp")
	}
	if len(record.AllowedPeers) > 1000 {
		return fmt.Errorf("too many allowed devices")
	}
	for _, id := range record.AllowedPeers {
		if _, err := parsePeerID(id); err != nil {
			return err
		}
	}
	return nil
}

func probeService(ctx context.Context, record ServiceRecord) string {
	if validateService(record) != nil {
		return "app_unavailable"
	}
	if serviceProtocol(record) == "tcp" {
		target, _ := extractHostPort(record.Target)
		conn, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "tcp", target)
		if err != nil {
			return "app_unavailable"
		}
		_ = conn.Close()
		return "available"
	}
	target, _ := validateLoopbackHTTPURL(record.Target)
	probeCtx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, target.String(), nil)
	if err != nil {
		return "app_unavailable"
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "app_unavailable"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return "app_unavailable"
	}
	return "available"
}

func (a *App) refreshServiceHealth() {
	records := a.main.LocalServices()
	for _, record := range records {
		status := probeService(a.ctx, record)
		a.mu.Lock()
		if current, exists := a.services[record.ID]; exists && current.Target == record.Target {
			current.Status = status
			a.services[record.ID] = current
			a.main.SetService(current)
		}
		a.mu.Unlock()
	}
}

func (a *App) handleServiceAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AllowedPeers []string `json:"allowedPeers"`
	}
	if err := decodeBody(w, r, &body, 32<<10); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	a.mu.Lock()
	record, ok := a.services[r.PathValue("id")]
	if !ok {
		a.mu.Unlock()
		writeError(w, 404, "locally published service not found")
		return
	}
	prior := record
	record.AllowedPeers = body.AllowedPeers
	if err := validateService(record); err != nil {
		a.mu.Unlock()
		writeError(w, 400, err.Error())
		return
	}
	a.services[record.ID] = record
	a.main.SetService(record)
	err := a.persistLocked()
	if err != nil {
		a.services[prior.ID] = prior
		a.main.SetService(prior)
	} else {
		a.addActivityLocked("service", "Service access updated", record.Name)
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, 500, "could not save service access")
		return
	}
	// Close sessions for devices whose grant was revoked. The gater permits a fresh connection for other resources.
	if len(record.AllowedPeers) > 0 {
		for _, id := range a.main.Host.Network().Peers() {
			if !containsPeer(record.AllowedPeers, id.String()) {
				_ = a.main.ClosePeer(id)
			}
		}
	}
	writeJSON(w, 200, a.serviceView(a.main, record))
}

func servicePort(record ServiceRecord) int {
	target, err := extractHostPort(record.Target)
	if err != nil {
		return 0
	}
	_, port, _ := net.SplitHostPort(target)
	n, _ := strconv.Atoi(port)
	return n
}

func normalizeServiceTarget(protocol, target string) string {
	if protocol == "tcp" {
		t, _ := extractHostPort(target)
		return t
	}
	return strings.TrimRight(target, "/")
}
