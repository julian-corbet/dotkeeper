BINARY = dotkeeper
BUILD_DIR = .
# noassets: skip Syncthing web GUI (we use the REST API only)
TAGS = noassets
LDFLAGS = -s -w

.PHONY: build test clean install

build:
	go build -tags $(TAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/dotkeeper

build-debug:
	go build -tags $(TAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/dotkeeper

test:
	go test -tags $(TAGS) ./...

clean:
	rm -f $(BUILD_DIR)/$(BINARY)

install: build
	install -Dm755 $(BUILD_DIR)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
