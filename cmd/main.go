package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/sirayoub/case-processor/config"
	"github.com/sirayoub/case-processor/internal/db"
	minioclient "github.com/sirayoub/case-processor/internal/minio"
	ollamaclient "github.com/sirayoub/case-processor/internal/ollama"
	"github.com/sirayoub/case-processor/internal/processor"
)

func main() {
	// Setup structured logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Ensure temp dir exists
	if err := os.MkdirAll(cfg.TempDir, 0755); err != nil {
		slog.Error("failed to create temp dir", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()
	command := os.Args[1]

	switch command {
	case "init-db":
		runInitDB(ctx, cfg)
	case "discover":
		runDiscover(ctx, cfg)
	case "process":
		runProcess(ctx, cfg)
	case "run":
		// discover + process in one step
		runDiscover(ctx, cfg)
		runProcess(ctx, cfg)
	case "stats":
		runStats(ctx, cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`Case Processor - Legal Document AI Extractor

Usage:
  case-processor <command>

Commands:
  init-db    Create database tables (run once on first setup)
  discover   Scan MinIO bucket and register new PDFs in database
  process    Process all pending cases through AI pipeline
  run        Run discover + process together
  stats      Show processing statistics

Environment:
  Copy .env.example to .env and configure before running.
`)
}

// ─── Commands ─────────────────────────────────────────────────────────────────

func runInitDB(ctx context.Context, cfg *config.Config) {
	slog.Info("initializing database schema...")
	database, err := db.New(cfg.DBConnectionString())
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := database.CreateSchema(ctx); err != nil {
		slog.Error("schema creation failed", "err", err)
		os.Exit(1)
	}
	slog.Info("database schema created successfully")
}

func runDiscover(ctx context.Context, cfg *config.Config) {
	database, mc := mustConnect(cfg)
	defer database.Close()

	p := processor.New(cfg, database, mc, nil)
	n, err := p.DiscoverAndRegister(ctx)
	if err != nil {
		slog.Error("discovery failed", "err", err)
		os.Exit(1)
	}
	slog.Info("discovery complete", "new_cases", n)
}

func runProcess(ctx context.Context, cfg *config.Config) {
	database, mc := mustConnect(cfg)
	defer database.Close()

	oc := ollamaclient.New(cfg.OllamaEndpoint, cfg.OllamaModel)
	p := processor.New(cfg, database, mc, oc)

	if err := p.ProcessPending(ctx); err != nil {
		slog.Error("processing failed", "err", err)
		os.Exit(1)
	}
	slog.Info("processing complete")
}

func runStats(ctx context.Context, cfg *config.Config) {
	database, err := db.New(cfg.DBConnectionString())
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	stats, err := database.Stats(ctx)
	if err != nil {
		slog.Error("stats failed", "err", err)
		os.Exit(1)
	}

	fmt.Println("\n=== Processing Statistics ===")
	total := 0
	for status, count := range stats {
		fmt.Printf("  %-20s: %d\n", status, count)
		total += count
	}
	fmt.Printf("  %-20s: %d\n", "TOTAL", total)
	fmt.Println()
}

func mustConnect(cfg *config.Config) (*db.DB, *minioclient.Client) {
	database, err := db.New(cfg.DBConnectionString())
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}

	mc, err := minioclient.New(cfg)
	if err != nil {
		slog.Error("minio connect failed", "err", err)
		os.Exit(1)
	}

	return database, mc
}
