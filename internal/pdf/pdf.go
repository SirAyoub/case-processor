package pdf

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Processor struct {
	dpi int
}

func New(dpi int) *Processor {
	return &Processor{dpi: dpi}
}

// PageImage holds base64-encoded image data for one page
type PageImage struct {
	PageNum int
	Base64  string
	MimeType string // "image/jpeg"
}

// PDFToImages converts a PDF file to a slice of base64-encoded JPEG images
// One image per page, stored in outDir
func (p *Processor) PDFToImages(pdfPath, outDir string) ([]PageImage, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	// pdftoppm -jpeg -r 200 input.pdf outDir/page
	// Output files: outDir/page-001.jpg, page-002.jpg, ...
	prefix := filepath.Join(outDir, "page")
	cmd := exec.Command("pdftoppm",
		"-jpeg",
		"-r", fmt.Sprintf("%d", p.dpi),
		pdfPath,
		prefix,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w\nOutput: %s", err, string(out))
	}

	// Collect generated image files
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, fmt.Errorf("read output dir: %w", err)
	}

	var jpgFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
			jpgFiles = append(jpgFiles, filepath.Join(outDir, e.Name()))
		}
	}
	sort.Strings(jpgFiles) // Ensure correct page order

	if len(jpgFiles) == 0 {
		return nil, fmt.Errorf("no images generated from PDF")
	}

	// Encode each image to base64
	pages := make([]PageImage, 0, len(jpgFiles))
	for i, imgPath := range jpgFiles {
		data, err := os.ReadFile(imgPath)
		if err != nil {
			return nil, fmt.Errorf("read image %s: %w", imgPath, err)
		}
		pages = append(pages, PageImage{
			PageNum:  i + 1,
			Base64:   base64.StdEncoding.EncodeToString(data),
			MimeType: "image/jpeg",
		})
	}

	return pages, nil
}

// CountPages returns total page count without converting
func (p *Processor) CountPages(pdfPath string) (int, error) {
	cmd := exec.Command("pdfinfo", pdfPath)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("pdfinfo failed: %w", err)
	}
	// Parse "Pages:          42" from pdfinfo output
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			var count int
			fmt.Sscanf(strings.TrimPrefix(line, "Pages:"), "%d", &count)
			return count, nil
		}
	}
	return 0, fmt.Errorf("could not parse page count from pdfinfo")
}

// ChunkPages splits pages into chunks of maxSize
func ChunkPages(pages []PageImage, maxSize int) [][]PageImage {
	if len(pages) <= maxSize {
		return [][]PageImage{pages}
	}
	var chunks [][]PageImage
	for i := 0; i < len(pages); i += maxSize {
		end := i + maxSize
		if end > len(pages) {
			end = len(pages)
		}
		chunks = append(chunks, pages[i:end])
	}
	return chunks
}
