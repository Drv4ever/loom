package loom

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

var (
	pairingSalt = []byte("loom-pairing-salt-v1")
	pairingInfo = []byte("loom-pairing-key-v1")
)

// PairingPayload is the secure network credential voucher exchanged during device pairing.
type PairingPayload struct {
	NetworkKey     string   `json:"networkKey"`               // 64-character hex string
	NetworkID      string   `json:"networkId"`                // e.g. loom-abc123
	NetworkName    string   `json:"networkName"`              // e.g. Personal workspace
	IssuedBy       string   `json:"issuedBy"`                 // Peer ID of the authorizer
	RelayAddresses []string `json:"relayAddresses,omitempty"` // Configured relays
	PeerAddresses  []string `json:"peerAddresses,omitempty"`  // Direct peer multiaddresses
	ExpiresAt      int64    `json:"expiresAt"`                // Unix timestamp (seconds)
}

// PairingSession represents an active ephemeral pairing session on an authorized device.
type PairingSession struct {
	PIN        string         `json:"pin"`
	Formatted  string         `json:"formatted"`
	Topic      string         `json:"topic"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	Payload    PairingPayload `json:"payload"`
	Ciphertext []byte         `json:"ciphertext"`
	Attempts   int            `json:"attempts"`
}

// CleanPIN normalizes a PIN by stripping whitespace, hyphens, and validating it contains 6 digits.
func CleanPIN(pin string) (string, error) {
	var cleaned strings.Builder
	for _, r := range pin {
		if r >= '0' && r <= '9' {
			cleaned.WriteRune(r)
		} else if r == '-' || r == ' ' {
			continue
		} else {
			return "", fmt.Errorf("invalid character in pairing PIN: %c", r)
		}
	}
	result := cleaned.String()
	if len(result) != 6 {
		return "", fmt.Errorf("pairing PIN must contain exactly 6 digits (got %d)", len(result))
	}
	return result, nil
}

// FormatPIN formats a 6-digit PIN into a human-friendly "123-456" representation.
func FormatPIN(pin string) string {
	if len(pin) != 6 {
		return pin
	}
	return pin[:3] + "-" + pin[3:]
}

// GeneratePIN creates a cryptographically secure random 6-digit numeric PIN.
func GeneratePIN() (string, error) {
	var b [3]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", fmt.Errorf("generate random PIN: %w", err)
	}
	// 24 bits -> modulo 1,000,000 ensures uniform 6-digit range [000000, 999999]
	val := (uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])) % 1000000
	return fmt.Sprintf("%06d", val), nil
}

// PairingTopic computes the blinded 16-byte rendezvous topic for a pairing PIN.
func PairingTopic(pin string) (string, error) {
	clean, err := CleanPIN(pin)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte("loom-pairing-topic-v1:" + clean))
	return hex.EncodeToString(hash[:16]), nil
}

// derivePairingKey derives a 32-byte AES key from the 6-digit PIN via HKDF-SHA256.
func derivePairingKey(cleanPIN string) ([]byte, error) {
	kdf := hkdf.New(sha256.New, []byte(cleanPIN), pairingSalt, pairingInfo)
	key := make([]byte, 32)
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	return key, nil
}

// EncryptPairingPayload encrypts the pairing payload using AES-256-GCM keyed with the derived PIN key.
func EncryptPairingPayload(payload PairingPayload, pin string) ([]byte, error) {
	clean, err := CleanPIN(pin)
	if err != nil {
		return nil, err
	}
	key, err := derivePairingKey(clean)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptPairingPayload decrypts and validates a pairing payload using the provided PIN.
func DecryptPairingPayload(data []byte, pin string) (*PairingPayload, error) {
	clean, err := CleanPIN(pin)
	if err != nil {
		return nil, err
	}
	key, err := derivePairingKey(clean)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize+gcm.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("invalid pairing PIN or corrupted voucher")
	}
	var payload PairingPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	if payload.ExpiresAt > 0 && time.Now().Unix() > payload.ExpiresAt {
		return nil, errors.New("pairing PIN has expired")
	}
	return &payload, nil
}

// PairingManager coordinates ephemeral pairing generation, local claims, and rate limiting.
type PairingManager struct {
	mu      sync.Mutex
	session *PairingSession
}

// NewPairingManager creates a new PairingManager.
func NewPairingManager() *PairingManager {
	return &PairingManager{}
}

// CreateSession generates a new 6-digit PIN and encrypted voucher valid for the specified duration.
func (pm *PairingManager) CreateSession(netKey []byte, netID, netName, issuedBy string, duration time.Duration) (*PairingSession, error) {
	return pm.CreateSessionWithOptions(netKey, netID, netName, issuedBy, nil, nil, duration)
}

// CreateSessionWithOptions generates a new 6-digit PIN and encrypted voucher with optional relays and peer addresses.
func (pm *PairingManager) CreateSessionWithOptions(netKey []byte, netID, netName, issuedBy string, relays, peerAddrs []string, duration time.Duration) (*PairingSession, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pin, err := GeneratePIN()
	if err != nil {
		return nil, err
	}
	topic, err := PairingTopic(pin)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(duration)
	payload := PairingPayload{
		NetworkKey:     hex.EncodeToString(netKey),
		NetworkID:      netID,
		NetworkName:    netName,
		IssuedBy:       issuedBy,
		RelayAddresses: relays,
		PeerAddresses:  peerAddrs,
		ExpiresAt:      expiresAt.Unix(),
	}
	ciphertext, err := EncryptPairingPayload(payload, pin)
	if err != nil {
		return nil, err
	}

	session := &PairingSession{
		PIN:        pin,
		Formatted:  FormatPIN(pin),
		Topic:      topic,
		ExpiresAt:  expiresAt,
		Payload:    payload,
		Ciphertext: ciphertext,
		Attempts:   0,
	}
	pm.session = session
	return session, nil
}

// CurrentSession returns the active session if not expired, or nil.
func (pm *PairingManager) CurrentSession() *PairingSession {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.session == nil {
		return nil
	}
	if time.Now().After(pm.session.ExpiresAt) {
		pm.session = nil
		return nil
	}
	cp := *pm.session
	return &cp
}

// GetSession is an alias for CurrentSession.
func (pm *PairingManager) GetSession() *PairingSession {
	return pm.CurrentSession()
}

// ClearSession clears any active pairing session.
func (pm *PairingManager) ClearSession() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.session = nil
}

// Claim attempts to claim the pairing session locally with the provided PIN.
func (pm *PairingManager) Claim(pin string) (*PairingPayload, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.session == nil || time.Now().After(pm.session.ExpiresAt) {
		pm.session = nil
		return nil, errors.New("no active pairing session")
	}

	pm.session.Attempts++
	if pm.session.Attempts > 5 {
		pm.session = nil
		return nil, errors.New("too many failed attempts; pairing session invalidated")
	}

	clean, err := CleanPIN(pin)
	if err != nil {
		return nil, err
	}

	if clean != pm.session.PIN {
		return nil, errors.New("incorrect pairing PIN")
	}

	payload := pm.session.Payload
	pm.session = nil // Single-use consumption
	return &payload, nil
}
