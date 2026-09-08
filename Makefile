BINARY := dist/otbr-insight

.PHONY: build test clean linux-amd64 linux-arm64 release

build:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/otbr-insight

test:
	go test ./...

linux-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/otbr-insight-linux-amd64 ./cmd/otbr-insight

linux-arm64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/otbr-insight-linux-arm64 ./cmd/otbr-insight

release: linux-amd64 linux-arm64

clean:
	rm -rf dist

