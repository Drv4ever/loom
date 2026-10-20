package loom

import (
	"strings"
	"testing"
	"time"
)

func TestPINFormatAndClean(t *testing.T) {
	pin, err := GeneratePIN()
	if err != nil {
		t.Fatalf("generate PIN: %v", err)
	}
	if len(pin) != 6 {
		t.Fatalf("expected 6-digit PIN, got %q", pin)
	}

	formatted := FormatPIN(pin)
	if len(formatted) != 7 || formatted[3] != '-' {
		t.Fatalf("unexpected formatted PIN: %q", formatted)
	}

	cleaned, err := CleanPIN(formatted)
	if err != nil {
		t.Fatalf("clean formatted PIN: %v", err)
	}
	if cleaned != pin {
		t.Fatalf("cleaned %q != original %q", cleaned, pin)
	}

	// Test with spaces
	withSpaces := " " + pin[:2] + " - " + pin[2:] + " "
	cleanedSpaces, err := CleanPIN(withSpaces)
	if err != nil {
		t.Fatalf("clean with spaces: %v", err)
	}
	if cleanedSpaces != pin {
		t.Fatalf("cleanedSpaces %q != original %q", cleanedSpaces, pin)
	}

	// Invalid characters
	if _, err := CleanPIN("12a456"); err == nil {
		t.Fatalf("expected error on non-digit")
	}

	// Wrong length
	if _, err := CleanPIN("12345"); err == nil {
		t.Fatalf("expected error on 5-digit PIN")
	}
	if _, err := CleanPIN("1234567"); err == nil {
		t.Fatalf("expected error on 7-digit PIN")
	}
}

func TestPairingTopicDeterministic(t *testing.T) {
	topic1, err := PairingTopic("482-910")
	if err != nil {
		t.Fatalf("topic1: %v", err)
	}
	topic2, err := PairingTopic("482910")
	if err != nil {
		t.Fatalf("topic2: %v", err)
	}
	if topic1 != topic2 {
		t.Fatalf("expected deterministic topics: %q != %q", topic1, topic2)
	}
	if len(topic1) != 32 { // 16 bytes = 32 hex chars
		t.Fatalf("expected 32-char hex topic, got %d chars (%q)", len(topic1), topic1)
	}
}

func TestPairingPayloadEncryptionDecryption(t *testing.T) {
	pin := "739102"
	payload := PairingPayload{
		NetworkKey:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		NetworkID:   "loom-test-123",
		NetworkName: "Test Network",
		IssuedBy:    "12D3KooWAuthorizerPeerID",
		ExpiresAt:   time.Now().Add(5 * time.Minute).Unix(),
	}

	ciphertext, err := EncryptPairingPayload(payload, pin)
	if err != nil {
		t.Fatalf("encrypt payload: %v", err)
	}
	if len(ciphertext) <= 12 {
		t.Fatalf("ciphertext too short")
	}

	// Successful decryption with correct PIN
	decrypted, err := DecryptPairingPayload(ciphertext, "739-102")
	if err != nil {
		t.Fatalf("decrypt payload: %v", err)
	}
	if decrypted.NetworkKey != payload.NetworkKey || decrypted.NetworkID != payload.NetworkID {
		t.Fatalf("decrypted payload mismatch: %+v vs %+v", decrypted, payload)
	}

	// Failed decryption with wrong PIN
	_, err = DecryptPairingPayload(ciphertext, "999999")
	if err == nil {
		t.Fatalf("expected error decrypting with wrong PIN")
	}
	if !strings.Contains(err.Error(), "invalid pairing PIN") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Expired payload
	expiredPayload := payload
	expiredPayload.ExpiresAt = time.Now().Add(-1 * time.Minute).Unix()
	expiredCiphertext, err := EncryptPairingPayload(expiredPayload, pin)
	if err != nil {
		t.Fatalf("encrypt expired payload: %v", err)
	}
	_, err = DecryptPairingPayload(expiredCiphertext, pin)
	if err == nil {
		t.Fatalf("expected error for expired payload")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPairingManagerLifecycle(t *testing.T) {
	pm := NewPairingManager()

	if sess := pm.CurrentSession(); sess != nil {
		t.Fatalf("expected no active session initially")
	}

	key := []byte("0123456789abcdef0123456789abcdef")
	sess, err := pm.CreateSession(key, "loom-test", "Workspace", "Peer123", 5*time.Minute)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.PIN == "" || len(sess.PIN) != 6 {
		t.Fatalf("invalid session PIN: %q", sess.PIN)
	}

	current := pm.CurrentSession()
	if current == nil || current.PIN != sess.PIN {
		t.Fatalf("expected current session to match created session")
	}

	// Failed claim attempts
	for i := 1; i <= 5; i++ {
		_, err := pm.Claim("000000")
		if err == nil {
			t.Fatalf("expected failure for incorrect PIN")
		}
	}

	// 6th attempt should invalidate the session due to rate limiting
	_, err = pm.Claim("000000")
	if err == nil || !strings.Contains(err.Error(), "too many failed attempts") {
		t.Fatalf("expected rate limit invalidation, got: %v", err)
	}

	if sess := pm.CurrentSession(); sess != nil {
		t.Fatalf("expected session to be cleared after rate limit exceeded")
	}

	// Create new session and claim successfully
	sess2, err := pm.CreateSession(key, "loom-test", "Workspace", "Peer123", 5*time.Minute)
	if err != nil {
		t.Fatalf("create session 2: %v", err)
	}
	claimed, err := pm.Claim(sess2.PIN)
	if err != nil {
		t.Fatalf("successful claim: %v", err)
	}
	if claimed.NetworkID != "loom-test" {
		t.Fatalf("claimed payload mismatch: %+v", claimed)
	}

	// Single-use: session should now be consumed
	if sess := pm.CurrentSession(); sess != nil {
		t.Fatalf("expected session to be consumed after claim")
	}
	if _, err := pm.Claim(sess2.PIN); err == nil {
		t.Fatalf("expected error claiming already consumed session")
	}
}
