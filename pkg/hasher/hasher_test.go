package hasher

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

const tinyWebPBase64 = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"

func TestCalculatePerceptualHashSupportsWebP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.webp")
	data, err := base64.StdEncoding.DecodeString(tinyWebPBase64)
	if err != nil {
		t.Fatalf("DecodeString returned error: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	hash, err := CalculatePerceptualHash(path)
	if err != nil {
		t.Fatalf("CalculatePerceptualHash returned error: %v", err)
	}
	if len(hash) != 16 {
		t.Fatalf("expected 64-bit pHash hex, got %q", hash)
	}
}
