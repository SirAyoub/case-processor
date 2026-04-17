.PHONY: build run-init run-discover run-process run-all run-stats deps

BINARY=case-processor
CMD=./cmd/main.go

# Download dependencies
deps:
	go mod tidy
	go mod download

# Build the binary
build: deps
	go build -o $(BINARY) $(CMD)

# Initialize database (run once)
run-init: build
	./$(BINARY) init-db

# Discover new PDFs from MinIO
run-discover: build
	./$(BINARY) discover

# Process pending cases
run-process: build
	./$(BINARY) process

# Full pipeline: discover + process
run-all: build
	./$(BINARY) run

# Show statistics
run-stats: build
	./$(BINARY) stats

# Clean build artifacts and temp files
clean:
	rm -f $(BINARY)
	rm -rf /tmp/case-processor
