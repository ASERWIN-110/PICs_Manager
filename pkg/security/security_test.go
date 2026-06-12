package security

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPairingCodeCanIssueExpiringDeviceToken(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	code, pairing, err := store.CreatePairingCodeWithDeviceTTL(context.Background(), "phone-a", ScopeViewer, time.Hour, time.Millisecond)
	if err != nil {
		t.Fatalf("CreatePairingCodeWithDeviceTTL returned error: %v", err)
	}
	if pairing.DeviceExpiresAt == nil {
		t.Fatal("expected pairing to carry device token expiry")
	}
	token, device, err := store.ClaimPairingCode(context.Background(), code, "phone-a")
	if err != nil {
		t.Fatalf("ClaimPairingCode returned error: %v", err)
	}
	if device.ExpiresAt == nil {
		t.Fatal("expected claimed device to have expiry")
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := store.Authenticate(context.Background(), token, ScopeViewer); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected expired token to be unauthorized, got %v", err)
	}
}

func TestPairingCodeDefaultsToNonExpiringDeviceToken(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	code, pairing, err := store.CreatePairingCode(context.Background(), "laptop-b", ScopeMaintainer, time.Hour)
	if err != nil {
		t.Fatalf("CreatePairingCode returned error: %v", err)
	}
	if pairing.DeviceExpiresAt != nil {
		t.Fatalf("expected default device token to not expire, got %v", pairing.DeviceExpiresAt)
	}
	token, device, err := store.ClaimPairingCode(context.Background(), code, "laptop-b")
	if err != nil {
		t.Fatalf("ClaimPairingCode returned error: %v", err)
	}
	if device.ExpiresAt != nil {
		t.Fatalf("expected default device token to not expire, got %v", device.ExpiresAt)
	}
	if _, err := store.Authenticate(context.Background(), token, ScopeViewer); err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
}
