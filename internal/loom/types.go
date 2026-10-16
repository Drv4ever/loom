package loom

import "time"

const Version = "0.1.0"

type NetworkState struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Connected bool     `json:"connected"`
	Demo      bool     `json:"demo"`
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
	ID         string `json:"id"`
	Name       string `json:"name"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Protocol   string `json:"protocol"`
	Target     string `json:"target"`
	Port       int    `json:"port"`
	Status     string `json:"status"`
	URL        string `json:"url"`
}

type Folder struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Path     string            `json:"path"`
	PeerID   string            `json:"peerId"`
	PeerName string            `json:"peerName"`
	Status   string            `json:"status"`
	Files    int               `json:"files"`
	Bytes    int64             `json:"bytes"`
	Progress int               `json:"progress"`
	LastSync time.Time         `json:"lastSync"`
	Paused   bool              `json:"paused"`
	Error    string            `json:"error"`
	Manifest map[string]string `json:"-"`
}

type Activity struct {
	ID     string    `json:"id"`
	Type   string    `json:"type"`
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
	Time   time.Time `json:"time"`
}

type Stats struct {
	OnlineDevices    int   `json:"onlineDevices"`
	Services         int   `json:"services"`
	SyncedFiles      int   `json:"syncedFiles"`
	TransferredBytes int64 `json:"transferredBytes"`
}

type State struct {
	Network  NetworkState `json:"network"`
	Devices  []Device     `json:"devices"`
	Services []Service    `json:"services"`
	Folders  []Folder     `json:"folders"`
	Activity []Activity   `json:"activity"`
	Stats    Stats        `json:"stats"`
}

type ServiceRecord struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Target string `json:"target"`
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
