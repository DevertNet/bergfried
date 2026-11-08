# Go wird in Docker gebaut – lokal ist kein Go nötig.
GO_IMAGE ?= golang:1.26
DOCKER_GO = docker run --rm \
	-v "$(CURDIR)":/src -w /src \
	-v bergfried-gomod:/go/pkg/mod -v bergfried-gobuild:/root/.cache/go-build \
	-e CGO_ENABLED=0 $(GO_IMAGE)

.PHONY: build test vet fmt
build: ## bergfriedd (macOS arm64) und bergfried-mcp (Linux arm64)
	mkdir -p bin
	$(DOCKER_GO) sh -c 'cd daemon && GOOS=darwin GOARCH=arm64 go build -o ../bin/bergfriedd ./cmd/bergfriedd'

test:
	$(DOCKER_GO) sh -c 'cd daemon && go test ./...'

vet:
	$(DOCKER_GO) sh -c 'cd daemon && go vet ./...'

fmt:
	$(DOCKER_GO) sh -c 'cd daemon && gofmt -l -w .'
