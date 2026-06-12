package thumbnailer

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"

	"github.com/disintegration/imaging"
)

func CreateBase64(srcImage image.Image, width, height int) (string, error) {
	thumbImage := imaging.Thumbnail(srcImage, width, height, imaging.Lanczos)

	buf := new(bytes.Buffer)

	err := jpeg.Encode(buf, thumbImage, &jpeg.Options{Quality: 80})
	if err != nil {
		return "", err
	}

	encodedStr := base64.StdEncoding.EncodeToString(buf.Bytes())

	return "data:image/jpeg;base64," + encodedStr, nil
}
