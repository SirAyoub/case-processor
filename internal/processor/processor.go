package processor

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirayoub/case-processor/config"
	"github.com/sirayoub/case-processor/internal/db"
	minioclient "github.com/sirayoub/case-processor/internal/minio"
	"github.com/sirayoub/case-processor/internal/models"
	ollamaclient "github.com/sirayoub/case-processor/internal/ollama"
	pdfprocessor "github.com/sirayoub/case-processor/internal/pdf"
)

type Processor struct {
	cfg    *config.Config
	db     *db.DB
	minio  *minioclient.Client
	ollama *ollamaclient.Client
	pdf    *pdfprocessor.Processor
	logger *slog.Logger
}

func New(
	cfg *config.Config,
	database *db.DB,
	mc *minioclient.Client,
	oc *ollamaclient.Client,
) *Processor {
	return &Processor{
		cfg:    cfg,
		db:     database,
		minio:  mc,
		ollama: oc,
		pdf:    pdfprocessor.New(cfg.PDFDPI),
		logger: slog.Default(),
	}
}

// ─── Discovery Phase ──────────────────────────────────────────────────────────

// DiscoverAndRegister lists all PDFs from MinIO and registers new ones in DB
func (p *Processor) DiscoverAndRegister(ctx context.Context) (int, error) {
	files, err := p.minio.ListPDFs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list PDFs: %w", err)
	}
	fmt.Printf("Discovered %d PDF files in MinIO\n", len(files))
	registered := 0
	for _, f := range files {
		id, err := p.db.UpsertCaseFile(ctx, f)
		if err != nil {
			p.logger.Warn("failed to register file", "file", f, "err", err)
			continue
		}
		if id > 0 {
			registered++
		}
	}
	p.logger.Info("discovery complete", "total_pdfs", len(files), "newly_registered", registered)
	return registered, nil
}

// ─── Processing Phase ─────────────────────────────────────────────────────────

// ProcessPending processes pending cases with concurrency control
func (p *Processor) ProcessPending(ctx context.Context) error {
	cases, err := p.db.GetPendingCases(ctx, 100)
	if err != nil {
		return fmt.Errorf("get pending cases: %w", err)
	}

	if len(cases) == 0 {
		p.logger.Info("no pending cases to process")
		return nil
	}

	p.logger.Info("starting batch processing", "cases", len(cases), "workers", p.cfg.ConcurrentWorkers)

	sem := make(chan struct{}, p.cfg.ConcurrentWorkers)
	var wg sync.WaitGroup

	for _, c := range cases {
		wg.Add(1)
		sem <- struct{}{}
		go func(c models.Case) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := p.processCase(ctx, c); err != nil {
				p.logger.Error("case processing failed", "file", c.FileName, "err", err)
			}
		}(c)
	}

	wg.Wait()
	return nil
}

// processCase handles the full pipeline for one case
func (p *Processor) processCase(ctx context.Context, c models.Case) error {
	logger := p.logger.With("file", c.FileName, "case_id", c.ID)
	logger.Info("processing case")

	// Mark as processing
	p.db.UpdateCaseStatus(ctx, c.ID, models.StatusProcessing, "")

	// Setup temp directory for this case
	caseID := strings.TrimSuffix(filepath.Base(c.FileName), ".pdf")
	tempDir := filepath.Join(p.cfg.TempDir, caseID)
	//defer os.RemoveAll(tempDir) // Cleanup on finish

	// Step 1: Download PDF from MinIO
	pdfPath := filepath.Join(p.cfg.TempDir, caseID+".pdf")
	//defer os.Remove(pdfPath)

	logger.Info("downloading PDF")
	if err := p.minio.DownloadPDF(ctx, c.FileName, pdfPath); err != nil {
		return p.handleError(ctx, c.ID, fmt.Errorf("download: %w", err))
	}

	// Step 2: Convert PDF to images
	logger.Info("converting PDF to images")
	imgDir := filepath.Join(tempDir, "images")
	pages, err := p.pdf.PDFToImages(pdfPath, imgDir)
	if err != nil {
		return p.handleError(ctx, c.ID, fmt.Errorf("pdf to images: %w", err))
	}
	logger.Info("PDF converted", "pages", len(pages))

	// Step 3: Chunk pages if necessary
	chunks := pdfprocessor.ChunkPages(pages, p.cfg.OllamaMaxPagesChunk)
	logger.Info("processing with AI", "chunks", len(chunks))

	// Step 4: Process each chunk with Ollama
	var chunkResults []*models.CaseJSON
	for i, chunk := range chunks {
		logger.Info("processing chunk", "chunk", i+1, "of", len(chunks), "pages", len(chunk))

		result, err := p.ollama.ExtractCaseData(ctx, chunk, i+1, len(chunks))
		if err != nil {
			return p.handleError(ctx, c.ID, fmt.Errorf("ollama chunk %d: %w", i+1, err))
		}
		chunkResults = append(chunkResults, result)

		// Small delay between chunks to avoid overwhelming the model
		if i < len(chunks)-1 {
			time.Sleep(2 * time.Second)
		}
	}

	// Step 5: Merge chunks if multiple
	var finalData *models.CaseJSON
	if len(chunkResults) == 1 {
		finalData = chunkResults[0]
	} else {
		finalData = ollamaclient.MergeChunks(chunkResults)
	}

	// Validate we got something meaningful
	if len(finalData.Defendants) == 0 && finalData.CaseNumber == "" {
		return p.handleError(ctx, c.ID, fmt.Errorf("AI returned empty data"))
	}

	// Step 6: Save to database
	logger.Info("saving to database",
		"defendants", len(finalData.Defendants),
		"verdicts", len(finalData.Verdicts),
	)
	if err := p.db.SaveCaseData(ctx, c.ID, finalData); err != nil {
		return p.handleError(ctx, c.ID, fmt.Errorf("save to DB: %w", err))
	}

	logger.Info("case completed successfully",
		"case_number", finalData.CaseNumber,
		"defendants", len(finalData.Defendants),
	)
	return nil
}

func (p *Processor) handleError(ctx context.Context, caseID int64, err error) error {
	p.logger.Error("case error", "case_id", caseID, "err", err)
	p.db.IncrementRetry(ctx, caseID, err.Error())
	return err
}
