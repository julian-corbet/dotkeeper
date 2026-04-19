BINARY = dotkeeper
BUILD_DIR = .
# noassets: skip Syncthing web GUI (we use the REST API only)
TAGS = noassets

# Inject version from git. Falls back to the default in cmd/dotkeeper/main.go
# if git isn't available (e.g. building from a source tarball).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS = -s -w
ifneq ($(VERSION),)
  LDFLAGS += -X main.version=$(VERSION) -X main.commit=$(COMMIT)
endif

.PHONY: build build-debug test cover clean install

build:
	go build -tags $(TAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/dotkeeper

build-debug:
	go build -tags $(TAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/dotkeeper

test:
	go test -tags $(TAGS) ./...

# Run tests with coverage profile, then summarise. Writes:
#   coverage.out — raw profile for tooling (go tool cover, codecov, etc.)
#   coverage.html — human-readable browser view
cover:
	go test -tags $(TAGS) -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html
	@echo "→ open coverage.html in a browser for the line-by-line view"

clean:
	rm -f $(BUILD_DIR)/$(BINARY) coverage.out coverage.html

install: build
	install -Dm755 $(BUILD_DIR)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
