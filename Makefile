# Development builds. Releases come from goreleaser (.goreleaser.yaml).
BIN ?= $(HOME)/.local/bin
VERSION ?= v0.0.0-dev+$(shell date +%Y%m%d)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: install build test vet

install:
	mkdir -p $(BIN)
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/rox ./cmd/rox
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/rix ./cmd/rix
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/rux ./cmd/rux
	# The Linux rux that rux serve hands to guests, beside rux.
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)/rux-linux-arm64 ./cmd/rux
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)/rux-linux-amd64 ./cmd/rux

test:
	go test ./...

vet:
	go vet ./...
