package hasher

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"

	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"

	"github.com/ajdnik/imghash"
)

const pHashBucketCount = 11

// CalculatePerceptualHashFromImage 从已解码的 image.Image 对象计算感知哈希
func CalculatePerceptualHashFromImage(img image.Image) string {
	phasher := imghash.NewPHash()
	pHash := phasher.Calculate(img)
	return hex.EncodeToString([]byte(pHash))
}

// CalculateSHA256 计算并返回一个文件的SHA-256哈希值。
func CalculateSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	h := sha256.New()

	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}

	hashBytes := h.Sum(nil)
	return hex.EncodeToString(hashBytes), nil
}

// CalculatePerceptualHash 计算并返回一个图片的感知哈希(pHash)值。
func CalculatePerceptualHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return "", err
	}

	phasher := imghash.NewPHash()

	pHash := phasher.Calculate(img)

	return hex.EncodeToString([]byte(pHash)), nil
}

func BuildPerceptualHashBuckets(raw string) ([]string, error) {
	value, err := hex.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	if len(value) != 8 {
		return nil, fmt.Errorf("expected 64-bit perceptual hash, got %d bytes", len(value))
	}
	hash := binary.BigEndian.Uint64(value)
	buckets := make([]string, 0, pHashBucketCount)
	bitOffset := 0
	for idx := 0; idx < pHashBucketCount; idx++ {
		width := 64 / pHashBucketCount
		if idx < 64%pHashBucketCount {
			width++
		}
		shift := 64 - bitOffset - width
		mask := uint64(1<<width) - 1
		chunk := (hash >> shift) & mask
		buckets = append(buckets, fmt.Sprintf("%02d:%02x", idx, chunk))
		bitOffset += width
	}
	return buckets, nil
}
