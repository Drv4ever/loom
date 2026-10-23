package cli

import (
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
	"strings"
	"time"

	"github.com/ayush/loom/internal/loom"
)

// Client communicates with a running Loom daemon HTTP API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient returns a Client for the specified API URL, falling back to LOOM_API_URL or 127.0.0.1:8787.
func NewClient(rawURL string) *Client {
	if rawURL == "" {
		rawURL = os.Getenv("LOOM_API_URL")
	}
	if rawURL == "" {
		rawURL = "http://127.0.0.1:8787"
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "http://" + rawURL
	}
	return &Client{
		BaseURL: strings.TrimRight(rawURL, "/"),
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// GetHealth checks if the Loom daemon is running and healthy.
func (c *Client) GetHealth(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/health", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false, c.wrapConnError(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// GetState fetches the complete mesh state.
func (c *Client) GetState(ctx context.Context) (*loom.State, error) {
	var state loom.State
	if err := c.request(ctx, http.MethodGet, "/api/state", nil, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// GetDiagnostics fetches safe daemon diagnostics.
func (c *Client) GetDiagnostics(ctx context.Context) (map[string]any, error) {
	var diag map[string]any
	if err := c.request(ctx, http.MethodGet, "/api/diagnostics", nil, &diag); err != nil {
		return nil, err
	}
	return diag, nil
}

// ConnectPeer sends a multiaddress to join the private mesh.
func (c *Client) ConnectPeer(ctx context.Context, address string) (*loom.Device, error) {
	body := map[string]string{"address": address}
	var device loom.Device
	err := c.request(ctx, http.MethodPost, "/api/peers", body, &device)
	if err != nil {
		return nil, err
	}
	return &device, nil
}

// PublishService registers a loopback HTTP service.
func (c *Client) PublishService(ctx context.Context, name, target string, trusted ...bool) (*loom.Service, error) {
	isTrusted := false
	if len(trusted) > 0 {
		isTrusted = trusted[0]
	}
	return c.PublishServiceWithOptions(ctx, name, target, isTrusted, nil)
}

// PublishServiceWithOptions registers a loopback HTTP service with granular peer access control.
func (c *Client) PublishServiceWithOptions(ctx context.Context, name, target string, trusted bool, allowedPeers []string) (*loom.Service, error) {
	body := map[string]any{
		"name":         name,
		"target":       target,
		"trusted":      trusted,
		"allowedPeers": allowedPeers,
	}
	var svc loom.Service
	err := c.request(ctx, http.MethodPost, "/api/services", body, &svc)
	if err != nil {
		return nil, err
	}
	return &svc, nil
}

// UnpublishService deletes a locally published service.
func (c *Client) UnpublishService(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/api/services/"+url.PathEscape(id), nil, nil)
}

// ListForwards returns all active TCP port forwards.
func (c *Client) ListForwards(ctx context.Context) ([]*loom.Forward, error) {
	var forwards []*loom.Forward
	if err := c.request(ctx, http.MethodGet, "/api/forwards", nil, &forwards); err != nil {
		return nil, err
	}
	return forwards, nil
}

// StartForward starts a local TCP listener forwarding to a remote peer service or target.
func (c *Client) StartForward(ctx context.Context, peerID, serviceID, target string, localPort int) (*loom.Forward, error) {
	body := map[string]any{
		"peerId":    peerID,
		"serviceId": serviceID,
		"target":    target,
		"localPort": localPort,
	}
	var fwd loom.Forward
	if err := c.request(ctx, http.MethodPost, "/api/forwards", body, &fwd); err != nil {
		return nil, err
	}
	return &fwd, nil
}

// StopForward terminates an active TCP port forward.
func (c *Client) StopForward(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/api/forwards/"+url.PathEscape(id), nil, nil)
}

// AddFolder registers a folder to sync with a peer.
func (c *Client) AddFolder(ctx context.Context, path, peerID, name string) (*loom.Folder, error) {
	return c.AddFolderWithOptions(ctx, path, peerID, name, nil, false)
}

// AddFolderWithOptions registers a folder with granular peer access control and read-only flags.
func (c *Client) AddFolderWithOptions(ctx context.Context, path, peerID, name string, allowedPeers []string, readOnly bool) (*loom.Folder, error) {
	body := map[string]any{
		"path":         path,
		"peerId":       peerID,
		"name":         name,
		"allowedPeers": allowedPeers,
		"readOnly":     readOnly,
	}
	var folder loom.Folder
	err := c.request(ctx, http.MethodPost, "/api/folders", body, &folder)
	if err != nil {
		return nil, err
	}
	return &folder, nil
}

// SyncFolder triggers an immediate sync for a folder.
func (c *Client) SyncFolder(ctx context.Context, id string) (*loom.Folder, error) {
	var folder loom.Folder
	err := c.request(ctx, http.MethodPost, "/api/folders/"+url.PathEscape(id)+"/sync", map[string]any{}, &folder)
	if err != nil {
		return nil, err
	}
	return &folder, nil
}

// SetFolderPaused pauses or resumes sync for a folder.
func (c *Client) SetFolderPaused(ctx context.Context, id string, paused bool) (*loom.Folder, error) {
	body := map[string]bool{"paused": paused}
	var folder loom.Folder
	err := c.request(ctx, http.MethodPost, "/api/folders/"+url.PathEscape(id)+"/pause", body, &folder)
	if err != nil {
		return nil, err
	}
	return &folder, nil
}

// RemoveFolder stops tracking a folder.
func (c *Client) RemoveFolder(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/api/folders/"+url.PathEscape(id), nil, nil)
}

// SetNetworkConnected pauses or resumes mesh networking.
func (c *Client) SetNetworkConnected(ctx context.Context, connected bool) (*loom.NetworkState, error) {
	body := map[string]bool{"connected": connected}
	var netState loom.NetworkState
	err := c.request(ctx, http.MethodPost, "/api/network", body, &netState)
	if err != nil {
		return nil, err
	}
	return &netState, nil
}

// GetConflicts returns active conflicts for a folder.
func (c *Client) GetConflicts(ctx context.Context, folderID string) ([]loom.Conflict, error) {
	var list []loom.Conflict
	err := c.request(ctx, http.MethodGet, "/api/folders/"+url.PathEscape(folderID)+"/conflicts", nil, &list)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// ResolveConflict resolves a file conflict with the chosen option ("local", "remote", "both").
func (c *Client) ResolveConflict(ctx context.Context, folderID, conflictID string, resolution loom.ConflictResolution) error {
	body := map[string]string{
		"conflictId": conflictID,
		"resolution": string(resolution),
	}
	return c.request(ctx, http.MethodPost, "/api/folders/"+url.PathEscape(folderID)+"/resolve", body, nil)
}

// GetTrash returns archived items in the trash directory for a folder.
func (c *Client) GetTrash(ctx context.Context, folderID string) ([]loom.TrashEntry, error) {
	var list []loom.TrashEntry
	err := c.request(ctx, http.MethodGet, "/api/folders/"+url.PathEscape(folderID)+"/trash", nil, &list)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// ListRelays returns the list of configured relays and their connectivity status.
func (c *Client) ListRelays(ctx context.Context) ([]loom.RelayInfo, error) {
	var list []loom.RelayInfo
	if err := c.request(ctx, http.MethodGet, "/api/relays", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// AddRelay adds a new relay multiaddress.
func (c *Client) AddRelay(ctx context.Context, address string) (*loom.RelayInfo, error) {
	body := map[string]string{"address": address}
	var info loom.RelayInfo
	if err := c.request(ctx, http.MethodPost, "/api/relays", body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// RemoveRelay removes a relay by address or peer ID.
func (c *Client) RemoveRelay(ctx context.Context, idOrAddr string) error {
	return c.request(ctx, http.MethodDelete, "/api/relays/"+url.PathEscape(idOrAddr), nil, nil)
}

// BlockDevice blocks a remote peer by ID, terminating existing connections.
func (c *Client) BlockDevice(ctx context.Context, peerID string) error {
	return c.request(ctx, http.MethodPost, "/api/devices/"+url.PathEscape(peerID)+"/block", map[string]any{}, nil)
}

// UnblockDevice removes a peer ID from the blocklist.
func (c *Client) UnblockDevice(ctx context.Context, peerID string) error {
	return c.request(ctx, http.MethodPost, "/api/devices/"+url.PathEscape(peerID)+"/unblock", map[string]any{}, nil)
}

// ListBlockedDevices fetches the list of blocked peer IDs.
func (c *Client) ListBlockedDevices(ctx context.Context) ([]string, error) {
	var resp struct {
		Blocked []string `json:"blocked"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/devices/blocked", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Blocked, nil
}

// GeneratePairingCode creates a new 6-digit PIN ephemeral pairing session.
func (c *Client) GeneratePairingCode(ctx context.Context, duration time.Duration) (*loom.PairingSession, error) {
	body := map[string]any{
		"durationSeconds": int(duration.Seconds()),
	}
	var session loom.PairingSession
	if err := c.request(ctx, http.MethodPost, "/api/pairing/generate", body, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// GetPairingSession returns the active pairing session if one exists.
func (c *Client) GetPairingSession(ctx context.Context) (*loom.PairingSession, error) {
	var resp struct {
		Active  bool                 `json:"active"`
		Session *loom.PairingSession `json:"session"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/pairing", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.Active || resp.Session == nil {
		return nil, nil
	}
	return resp.Session, nil
}

// ClaimPairingCode redeems a 6-digit pairing PIN voucher.
func (c *Client) ClaimPairingCode(ctx context.Context, pin, relay string) (*loom.PairingPayload, error) {
	body := map[string]any{
		"pin":   pin,
		"relay": relay,
	}
	var payload loom.PairingPayload
	if err := c.request(ctx, http.MethodPost, "/api/pairing/claim", body, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (c *Client) request(ctx context.Context, method, endpoint string, input, output any) error {
	var bodyReader io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+endpoint, bodyReader)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return c.wrapConnError(err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBytes, &errResp) == nil && errResp.Error != "" {
			return errors.New(errResp.Error)
		}
		return fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	if output != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, output); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) wrapConnError(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("cannot connect to Loom daemon at %s (is the daemon running? Start it with 'loom daemon'): %w", c.BaseURL, err)
	}
	return err
}
