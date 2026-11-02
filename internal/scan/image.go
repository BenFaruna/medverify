package scan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"

	// Decoders registered for DecodeConfig and the full decode. WebP is not in
	// the standard library, so it is registered here explicitly.
	_ "image/png"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

// Image validation errors. Each maps to a stable API problem code.
var (
	ErrUnsupportedFormat = errors.New("unsupported image format")
	ErrImageTooLarge     = errors.New("image is larger than the allowed limit")
	ErrCorruptImage      = errors.New("image could not be decoded")
)

// ImageLimits are the per-file validation limits.
type ImageLimits struct {
	// MaxBytes is the largest accepted upload.
	MaxBytes int64
	// MaxPixels rejects decompression bombs before the pixels are decoded.
	MaxPixels int64
}

// PreparedImage is a clean JPEG ready for storage. All EXIF, GPS, and embedded
// thumbnails are gone because it is re-encoded from raw pixels.
type PreparedImage struct {
	Data        []byte
	ContentType string
	Width       int
	Height      int
	SHA256      string
}

// SniffImageType identifies an allowed image from its bytes. The client's
// Content-Type header and filename are never consulted.
func SniffImageType(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

// PrepareImage validates one upload, applies EXIF orientation, and re-encodes
// it to a metadata-free JPEG. Orientation is applied before the pixels are
// rewritten; doing it afterwards would store phone photos sideways.
func PrepareImage(data []byte, limits ImageLimits) (PreparedImage, error) {
	if limits.MaxBytes > 0 && int64(len(data)) > limits.MaxBytes {
		return PreparedImage{}, ErrImageTooLarge
	}
	if SniffImageType(data) == "" {
		return PreparedImage{}, ErrUnsupportedFormat
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return PreparedImage{}, fmt.Errorf("%w: %w", ErrCorruptImage, err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return PreparedImage{}, ErrCorruptImage
	}
	if limits.MaxPixels > 0 {
		// Compare as int64 so a hostile dimension cannot overflow.
		if int64(config.Width)*int64(config.Height) > limits.MaxPixels {
			return PreparedImage{}, ErrImageTooLarge
		}
	}

	// imaging.Decode applies the EXIF orientation before returning the image.
	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return PreparedImage{}, fmt.Errorf("%w: %w", ErrCorruptImage, err)
	}

	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return PreparedImage{}, ErrCorruptImage
	}

	var encoded bytes.Buffer
	encoded.Grow(len(data))
	if err := jpeg.Encode(&encoded, flattenOnWhite(decoded), &jpeg.Options{Quality: 90}); err != nil {
		return PreparedImage{}, fmt.Errorf("%w: %w", ErrCorruptImage, err)
	}

	sum := sha256.Sum256(encoded.Bytes())
	return PreparedImage{
		Data:        encoded.Bytes(),
		ContentType: "image/jpeg",
		Width:       width,
		Height:      height,
		SHA256:      hex.EncodeToString(sum[:]),
	}, nil
}

// flattenOnWhite composites transparent pixels over white. JPEG has no alpha
// channel, and the encoder would otherwise turn transparency black.
func flattenOnWhite(img image.Image) image.Image {
	type opacity interface {
		Opaque() bool
	}
	if opaque, ok := img.(opacity); ok && opaque.Opaque() {
		return img
	}

	bounds := img.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, bounds.Min, draw.Over)
	return canvas
}
