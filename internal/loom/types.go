package loom

import "time"

const Version = "0.1.0"

type NetworkState struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Connected bool     `json:"connected"`
	PeerID    string   `json:"peerId"`
	Addresses []string `json:"addresses"`
	Version   string   `json:"version"`
}

type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Platform   string    `json:"platform"`
	Online     bool      `json:"online"`
	Self       bool      `json:"self"`
	Connection string    `json:"connection"`
	LatencyMs  int64     `json:"latencyMs"`
	Address    string    `json:"address"`
	LastSeen   time.Time `json:"lastSeen"`
}

type Service struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	DeviceID     string   `json:"deviceId"`
	DeviceName   string   `json:"deviceName"`
	Protocol     string   `json:"protocol"`
	Target       string   `json:"target"`
	Port         int      `json:"port"`
	Status       string   `json:"status"`
	URL          string   `json:"url"`
	Trusted      bool     `json:"trusted"`
	AllowedPeers []string `json:"allowedPeers,omitempty"`
}

type Folder struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Path         string            `json:"path"`
	PeerID       string            `json:"peerId"`
	PeerName     string            `json:"peerName"`
	Status       string            `json:"status"`
	Files        int               `json:"files"`
	Bytes        int64             `json:"bytes"`
	Progress     int               `json:"progress"`
	LastSync     time.Time         `json:"lastSync"`
	Paused       bool              `json:"paused"`
	Error        string            `json:"error"`
	Conflicts    int               `json:"conflicts"`
	AllowedPeers []string          `json:"allowedPeers,omitempty"`
	ReadOnly     bool              `json:"readOnly,omitempty"`
	Manifest     map[string]string `json:"-"`
}

type Activity struct {
	ID     string    `json:"id"`
	Type   string    `json:"type"`
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
	Time   time.Time `json:"time"`
}

type ReceivedFolder struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Path         string           `json:"path"`
	PeerID       string           `json:"peerId"`
	PeerName     string           `json:"peerName"`
	Files        []fileDescriptor `json:"files"`
	Bytes        int64            `json:"bytes"`
	LastReceived time.Time        `json:"lastReceived"`
}

type Stats struct {
	OnlineDevices    int   `json:"onlineDevices"`
	Services         int   `json:"services"`
	SyncedFiles      int   `json:"syncedFiles"`
	TransferredBytes int64 `json:"transferredBytes"`
}

type State struct {
	Network         NetworkState     `json:"network"`
	Devices         []Device         `json:"devices"`
	BlockedDevices  []string         `json:"blockedDevices"`
	Services        []Service        `json:"services"`
	Forwards        []*Forward       `json:"forwards"`
	Folders         []Folder         `json:"folders"`
	ReceivedFolders []ReceivedFolder `json:"receivedFolders"`
	Relays          []string         `json:"relays"`
	Activity        []Activity       `json:"activity"`
	Stats           Stats            `json:"stats"`
}

type RelayInfo struct {
	Address string `json:"address"`
	PeerID  string `json:"peerId"`
	Status  string `json:"status"` // "connected", "unreachable", "configured"
	Error   string `json:"error,omitempty"`
}

type ServiceRecord struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Target       string   `json:"target"`
	Trusted      bool     `json:"trusted"`
	AllowedPeers []string `json:"allowedPeers,omitempty"`
}

type PeerSummary struct {
	Name     string          `json:"name"`
	Platform string          `json:"platform"`
	Services []ServiceRecord `json:"services"`
}

type fileDescriptor struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	SHA256  string    `json:"sha256"`
}
