package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Scope string

const (
	ScopeViewer     Scope = "viewer"
	ScopeMaintainer Scope = "maintainer"
	ScopeAdmin      Scope = "admin"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not_found")
)

type Device struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      Scope      `json:"scope"`
	TokenHash  string     `json:"tokenHash,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeenAt time.Time  `json:"lastSeenAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

type PairingCode struct {
	ID              string     `json:"id"`
	Name            string     `json:"name,omitempty"`
	Scope           Scope      `json:"scope"`
	CodeHash        string     `json:"codeHash,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	DeviceExpiresAt *time.Time `json:"deviceExpiresAt,omitempty"`
	ClaimedAt       *time.Time `json:"claimedAt,omitempty"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

type state struct {
	Devices  []Device      `json:"devices"`
	Pairings []PairingCode `json:"pairings"`
}

func DefaultStorePath(logRoot string) string {
	logRoot = strings.TrimSpace(logRoot)
	if logRoot == "" {
		logRoot = "."
	}
	return filepath.Join(logRoot, "auth", "devices.json")
}

func NewStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("security store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	store := &Store{path: path}
	if _, err := store.readState(); err != nil {
		return nil, err
	}
	return store, nil
}

func NormalizeScope(scope string) (Scope, error) {
	switch Scope(strings.ToLower(strings.TrimSpace(scope))) {
	case ScopeViewer:
		return ScopeViewer, nil
	case ScopeMaintainer:
		return ScopeMaintainer, nil
	case ScopeAdmin:
		return ScopeAdmin, nil
	default:
		return "", fmt.Errorf("invalid scope %q", scope)
	}
}

func HasScope(actual Scope, required Scope) bool {
	if required == "" {
		return true
	}
	switch actual {
	case ScopeAdmin:
		return true
	case ScopeMaintainer:
		return required == ScopeMaintainer || required == ScopeViewer
	case ScopeViewer:
		return required == ScopeViewer
	default:
		return false
	}
}

func (s *Store) CreatePairingCode(ctx context.Context, name string, scope Scope, ttl time.Duration) (string, PairingCode, error) {
	return s.CreatePairingCodeWithDeviceTTL(ctx, name, scope, ttl, 0)
}

func (s *Store) CreatePairingCodeWithDeviceTTL(ctx context.Context, name string, scope Scope, ttl time.Duration, deviceTTL time.Duration) (string, PairingCode, error) {
	if err := ctx.Err(); err != nil {
		return "", PairingCode{}, err
	}
	if _, err := NormalizeScope(string(scope)); err != nil {
		return "", PairingCode{}, err
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	code, err := randomPairingCode()
	if err != nil {
		return "", PairingCode{}, err
	}
	now := time.Now().UTC()
	pairing := PairingCode{
		ID:        newID("pair"),
		Name:      strings.TrimSpace(name),
		Scope:     scope,
		CodeHash:  hashSecret(code),
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if deviceTTL > 0 {
		expiresAt := now.Add(deviceTTL)
		pairing.DeviceExpiresAt = &expiresAt
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readStateUnlocked()
	if err != nil {
		return "", PairingCode{}, err
	}
	st.Pairings = append(pruneExpiredPairings(st.Pairings, now), pairing)
	if err := s.writeStateUnlocked(st); err != nil {
		return "", PairingCode{}, err
	}
	pairing.CodeHash = ""
	return code, pairing, nil
}

func (s *Store) ClaimPairingCode(ctx context.Context, code string, deviceName string) (string, Device, error) {
	if err := ctx.Err(); err != nil {
		return "", Device{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", Device{}, ErrUnauthorized
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readStateUnlocked()
	if err != nil {
		return "", Device{}, err
	}
	st.Pairings = pruneExpiredPairings(st.Pairings, now)

	pairingIndex := -1
	codeHash := hashSecret(code)
	for i := range st.Pairings {
		pairing := st.Pairings[i]
		if pairing.ClaimedAt != nil || now.After(pairing.ExpiresAt) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(pairing.CodeHash), []byte(codeHash)) == 1 {
			pairingIndex = i
			break
		}
	}
	if pairingIndex < 0 {
		if err := s.writeStateUnlocked(st); err != nil {
			return "", Device{}, err
		}
		return "", Device{}, ErrUnauthorized
	}

	token, err := randomToken()
	if err != nil {
		return "", Device{}, err
	}
	pairing := st.Pairings[pairingIndex]
	claimedAt := now
	st.Pairings[pairingIndex].ClaimedAt = &claimedAt
	name := strings.TrimSpace(deviceName)
	if name == "" {
		name = pairing.Name
	}
	if name == "" {
		name = "paired-device"
	}
	device := Device{
		ID:        newID("dev"),
		Name:      name,
		Scope:     pairing.Scope,
		TokenHash: hashSecret(token),
		CreatedAt: now,
		ExpiresAt: pairing.DeviceExpiresAt,
	}
	st.Devices = append(st.Devices, device)
	if err := s.writeStateUnlocked(st); err != nil {
		return "", Device{}, err
	}
	device.TokenHash = ""
	return token, device, nil
}

func (s *Store) Authenticate(ctx context.Context, token string, required Scope) (Device, error) {
	if err := ctx.Err(); err != nil {
		return Device{}, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return Device{}, ErrUnauthorized
	}
	now := time.Now().UTC()
	tokenHash := hashSecret(token)

	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readStateUnlocked()
	if err != nil {
		return Device{}, err
	}
	for i := range st.Devices {
		device := st.Devices[i]
		if device.RevokedAt != nil {
			continue
		}
		if device.ExpiresAt != nil && now.After(*device.ExpiresAt) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(device.TokenHash), []byte(tokenHash)) != 1 {
			continue
		}
		if !HasScope(device.Scope, required) {
			return Device{}, ErrForbidden
		}
		st.Devices[i].LastSeenAt = now
		if err := s.writeStateUnlocked(st); err != nil {
			return Device{}, err
		}
		device.TokenHash = ""
		device.LastSeenAt = now
		return device, nil
	}
	return Device{}, ErrUnauthorized
}

func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readStateUnlocked()
	if err != nil {
		return nil, err
	}
	devices := append([]Device(nil), st.Devices...)
	for i := range devices {
		devices[i].TokenHash = ""
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].CreatedAt.After(devices[j].CreatedAt)
	})
	return devices, nil
}

func (s *Store) ListPairingCodes(ctx context.Context) ([]PairingCode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readStateUnlocked()
	if err != nil {
		return nil, err
	}
	st.Pairings = pruneExpiredPairings(st.Pairings, now)
	if err := s.writeStateUnlocked(st); err != nil {
		return nil, err
	}
	pairings := append([]PairingCode(nil), st.Pairings...)
	for i := range pairings {
		pairings[i].CodeHash = ""
	}
	sort.Slice(pairings, func(i, j int) bool {
		return pairings[i].CreatedAt.After(pairings[j].CreatedAt)
	})
	return pairings, nil
}

func (s *Store) RevokeDevice(ctx context.Context, deviceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return ErrNotFound
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readStateUnlocked()
	if err != nil {
		return err
	}
	for i := range st.Devices {
		if st.Devices[i].ID != deviceID {
			continue
		}
		st.Devices[i].RevokedAt = &now
		return s.writeStateUnlocked(st)
	}
	return ErrNotFound
}

func (s *Store) readState() (state, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readStateUnlocked()
}

func (s *Store) readStateUnlocked() (state, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return state{}, nil
		}
		return state{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return state{}, nil
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return state{}, err
	}
	return st, nil
}

func (s *Store) writeStateUnlocked(st state) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".devices-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func pruneExpiredPairings(pairings []PairingCode, now time.Time) []PairingCode {
	kept := pairings[:0]
	for _, pairing := range pairings {
		if pairing.ClaimedAt != nil {
			continue
		}
		if now.After(pairing.ExpiresAt) {
			continue
		}
		kept = append(kept, pairing)
	}
	return kept
}

func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "pm_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func randomPairingCode() (string, error) {
	data := make([]byte, 15)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data)
	parts := make([]string, 0, 4)
	for len(encoded) > 0 {
		chunk := encoded
		if len(chunk) > 5 {
			chunk = encoded[:5]
		}
		parts = append(parts, chunk)
		encoded = encoded[len(chunk):]
	}
	return strings.Join(parts, "-"), nil
}

func newID(prefix string) string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(data)
}
