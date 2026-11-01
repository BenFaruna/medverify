package registry

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRunner struct {
	outputs map[string][]byte
	err     error
	calls   [][]string
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return nil, f.err
	}
	if out, ok := f.outputs[name]; ok {
		return out, nil
	}
	return []byte("Pages:          1\n"), nil
}

func TestPDFExtractorReadsTextLayer(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"pdfinfo":   []byte("Pages:          3\n"),
		"pdftotext": []byte("1. NAME OF THE MEDICINAL PRODUCT\nExample\f2. COMPOSITION\nMore\fThird page\f"),
	}}
	extractor := NewPDFExtractor("pdftotext", "pdfinfo", 10)
	extractor.runner = runner

	got, err := extractor.Extract(context.Background(), "/tmp/test.pdf")
	require.NoError(t, err)
	assert.Equal(t, "pdftotext", got.Method)
	assert.Equal(t, 3, got.Pages)
	assert.False(t, got.Scanned)
	assert.Contains(t, got.Text, "NAME OF THE MEDICINAL PRODUCT")

	require.Len(t, runner.calls, 2)
	assert.Equal(t, []string{"pdfinfo", "/tmp/test.pdf"}, runner.calls[0])
	assert.Equal(t, []string{"pdftotext", "-layout", "-enc", "UTF-8", "/tmp/test.pdf", "-"}, runner.calls[1])
}

func TestPDFExtractorMarksEmptyTextAsScanned(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"pdfinfo":   []byte("Pages:          1\n"),
		"pdftotext": []byte("\f"),
	}}
	extractor := NewPDFExtractor("pdftotext", "pdfinfo", 10)
	extractor.runner = runner

	got, err := extractor.Extract(context.Background(), "/tmp/scan.pdf")
	require.NoError(t, err)
	assert.True(t, got.Scanned)
}

func TestPDFExtractorRejectsTooManyPages(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"pdfinfo": []byte("Pages:          101\n"),
	}}
	extractor := NewPDFExtractor("pdftotext", "pdfinfo", 100)
	extractor.runner = runner

	_, err := extractor.Extract(context.Background(), "/tmp/big.pdf")
	require.ErrorIs(t, err, ErrTooManyPages)
	require.Len(t, runner.calls, 1, "pdftotext must not run for an oversized document")
}

func TestPDFExtractorWrapsCommandFailure(t *testing.T) {
	extractor := NewPDFExtractor("pdftotext", "pdfinfo", 10)
	extractor.runner = &fakeRunner{
		outputs: map[string][]byte{"pdfinfo": []byte("Pages: 1\n")},
		err:     errors.New("boom"),
	}
	_, err := extractor.Extract(context.Background(), "/tmp/bad.pdf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read pdf page count")
}

func TestPDFExtractorAgainstRealFixtures(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	if _, err := exec.LookPath("pdfinfo"); err != nil {
		t.Skip("pdfinfo not installed")
	}

	extractor := NewPDFExtractor("", "", 10)
	base := filepath.Join("..", "..", "testdata", "greenbook", "leaflets")

	got, err := extractor.Extract(context.Background(), filepath.Join(base, "structured.pdf"))
	require.NoError(t, err)
	assert.False(t, got.Scanned)
	assert.Equal(t, 2, got.Pages)
	assert.Contains(t, got.Text, "4.2 Posology and method of administration")

	scanned, err := extractor.Extract(context.Background(), filepath.Join(base, "scanned.pdf"))
	require.NoError(t, err)
	assert.True(t, scanned.Scanned)
}
