package hasher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	// 匿名导入 (blank import) image解码器
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"

	"github.com/ajdnik/imghash"
)

// CalculateSHA256FromBytes 从字节切片计算 SHA-256 哈希
func CalculateSHA256FromBytes(data []byte) string {
	hashBytes := sha256.Sum256(data)
	return hex.EncodeToString(hashBytes[:])
}

// CalculatePerceptualHashFromImage 从已解码的 image.Image 对象计算感知哈希
func CalculatePerceptualHashFromImage(img image.Image) string {
	phasher := imghash.NewPHash()
	pHash := phasher.Calculate(img)
	return fmt.Sprintf("%d", pHash)
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

	// 1. Calculate()只返回一个uint64，我们将它赋给一个变量。
	pHash := phasher.Calculate(img)

	// 2. 格式化并返回。因为此过程没有错误返回，所以第二个返回值为 nil。
	return fmt.Sprintf("%d", pHash), nil
}
