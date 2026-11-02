package scan_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/scan"
)

func TestPrepareImageStripsMetadataAndReencodesJPEG(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"normal.jpg", "gps.jpg", "rotated.jpg"} {
		raw, err := os.ReadFile("../../testdata/images/" + name)
		require.NoError(t, err)

		prepared, err := scan.PrepareImage(raw, scan.ImageLimits{MaxBytes: 1 << 20, MaxPixels: 1 << 20})
		require.NoError(t, err, name)

		assert.Equal(t, "image/jpeg", prepared.ContentType)
		assert.NotContains(t, string(prepared.Data), "Exif", "%s must not keep an EXIF segment", name)
		assert.NotContains(t, string(prepared.Data), "GPS", "%s must not keep GPS tags", name)
		assert.NotEmpty(t, prepared.SHA256)
		assert.Positive(t, prepared.Width)
		assert.Positive(t, prepared.Height)
	}
}

func TestPrepareImageAppliesEXIFOrientation(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../testdata/images/rotated.jpg")
	require.NoError(t, err)

	prepared, err := scan.PrepareImage(raw, scan.ImageLimits{MaxBytes: 1 << 20, MaxPixels: 1 << 20})
	require.NoError(t, err)

	// The fixture is 60x40 with orientation 6. Auto-orientation turns it into
	// a 40x60 portrait image before the JPEG is written.
	assert.Equal(t, 40, prepared.Width)
	assert.Equal(t, 60, prepared.Height)
}

func TestPrepareImageRejectsRenamedText(t *testing.T) {
	t.Parallel()

	_, err := scan.PrepareImage([]byte("this is not an image"), scan.ImageLimits{MaxBytes: 1 << 20})
	assert.ErrorIs(t, err, scan.ErrUnsupportedFormat)
}

func TestPrepareImageRejectsCorruptJPEG(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../testdata/images/normal.jpg")
	require.NoError(t, err)

	// Keep the JPEG signature so the type sniffs correctly, then cut the
	// payload short so decoding fails cleanly.
	corrupt := raw[:3]
	_, err = scan.PrepareImage(corrupt, scan.ImageLimits{MaxBytes: 1 << 20})
	assert.True(t, errors.Is(err, scan.ErrCorruptImage), "got %v", err)
}

func TestPrepareImageRejectsOversizedBytes(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../testdata/images/normal.jpg")
	require.NoError(t, err)

	_, err = scan.PrepareImage(raw, scan.ImageLimits{MaxBytes: int64(len(raw)) - 1})
	assert.ErrorIs(t, err, scan.ErrImageTooLarge)
}

func TestPrepareImageRejectsHugeDimensionsBeforeDecode(t *testing.T) {
	t.Parallel()

	// A valid PNG header with 40,000 x 40,000 dimensions and no pixel data.
	// DecodeConfig can read it, so the pixel guard rejects it without ever
	// allocating the decoded image.
	_, err := scan.PrepareImage(hugePNG(40_000, 40_000), scan.ImageLimits{MaxBytes: 1 << 20, MaxPixels: 40_000_000})
	assert.ErrorIs(t, err, scan.ErrImageTooLarge)
}

func TestObjectKeyDoesNotUseClientFilename(t *testing.T) {
	t.Parallel()

	key := scan.ObjectKey(uuid.UUID{1}, "random")
	assert.Contains(t, key, "scans/")
	assert.NotContains(t, key, "photo.jpg")
}

func hugePNG(width, height uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte("\x89PNG\r\n\x1a\n"))

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // truecolour

	chunk := make([]byte, 4+len(ihdr))
	copy(chunk, "IHDR")
	copy(chunk[4:], ihdr)
	buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(ihdr))))
	buf.Write(chunk)
	buf.Write(binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(chunk)))
	return buf.Bytes()
}
