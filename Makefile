APP_NAME    := versa-deployer
MODULE      := github.com/mihailvovk/versa-proxmox-deployer
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME  := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS     := -s -w -X 'main.Version=$(VERSION)' -X 'main.BuildTime=$(BUILD_TIME)'
DIST_DIR    := dist

# Platforms to build: os/arch pairs
PLATFORMS := \
	darwin/amd64 \
	darwin/arm64 \
	linux/amd64 \
	linux/arm64 \
	windows/amd64

.PHONY: build clean release all dev help

## build: Build for current platform
build:
	go build -ldflags "$(LDFLAGS)" -o $(APP_NAME) .

## dev: Build and run (current platform)
dev: build
	./$(APP_NAME)

## release: Cross-compile for all platforms into dist/
release: clean
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		output="$(DIST_DIR)/$(APP_NAME)-$$os-$$arch$$ext"; \
		echo "Building $$os/$$arch → $$output"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o "$$output" . || exit 1; \
	done
	@echo ""
	@echo "Built binaries:"
	@ls -lh $(DIST_DIR)/

## checksums: Generate SHA256 checksums for release binaries
checksums: release
	@cd $(DIST_DIR) && shasum -a 256 * > checksums.txt
	@echo "Checksums written to $(DIST_DIR)/checksums.txt"

## clean: Remove build artifacts
clean:
	@rm -rf $(DIST_DIR)
	@rm -f $(APP_NAME)

## help: Show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'
