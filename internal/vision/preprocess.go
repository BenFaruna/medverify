package vision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"

	"github.com/disintegration/imaging"
)

// Preprocess resizes a stored JPEG so its longer edge is at most maxDimension
// pixels, then re-encodes it without metadata. Images already within the limit
// are returned unchanged. It does not sharpen, crop, or alter contrast: those
// need evidence before they belong in the pipeline.
func Preprocess(data []byte, maxDimension int) ([]byte, error) {
	if maxDimension <= 0 {
		return data, nil
	}
	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("vision: decode image for preprocessing: %w", err)
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maxDimension && height <= maxDimension {
		return data, nil
	}

	if width >= height {
		decoded = imaging.Resize(decoded, maxDimension, 0, imaging.Lanczos)
	} else {
		decoded = imaging.Resize(decoded, 0, maxDimension, imaging.Lanczos)
	}

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, decoded, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("vision: encode preprocessed image: %w", err)
	}
	return encoded.Bytes(), nil
}

// Fallback tries the primary provider, then a secondary model when the primary
// is unavailable. An unreadable answer is never retried on the fallback: that
// would be an invitation to guess.
type Fallback struct {
	Primary   Provider
	Secondary Provider
}

// Extract implements Provider.
func (f Fallback) Extract(ctx context.Context, request Request) (Result, error) {
	result, err := f.Primary.Extract(ctx, request)
	if err == nil || errors.Is(err, ErrUnreadable) {
		return result, err
	}
	if f.Secondary == nil {
		return Result{}, err
	}
	return f.Secondary.Extract(ctx, request)
}
