# Go wird in Docker gebaut – lokal ist kein Go nötig.
GO_IMAGE ?= golang:1.26
VERSION ?= 0.0.0-dev
DOCKER_GO = docker run --rm \
	-v "$(CURDIR)":/src -w /src \
	-v bergfried-gomod:/go/pkg/mod -v bergfried-gobuild:/root/.cache/go-build \
	-e CGO_ENABLED=0 $(GO_IMAGE)

.PHONY: build test vet fmt
build: ## bergfriedd (macOS arm64) und bergfried-mcp (Linux arm64)
	mkdir -p bin
	$(DOCKER_GO) sh -c 'GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w -buildid= -X github.com/DevertNet/bergfried/daemon/internal/api.Version=$(VERSION)" -o bin/bergfriedd ./daemon/cmd/bergfriedd && GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -buildid=" -o bin/bergfried-mcp ./mcp/cmd/bergfried-mcp'

test:
	$(DOCKER_GO) sh -c 'go test ./...'

vet:
	$(DOCKER_GO) sh -c 'go vet ./...'

fmt:
	$(DOCKER_GO) sh -c 'gofmt -l -w .'

.PHONY: proto
proto: ## Protobuf/gRPC-Code für Go erzeugen (in Docker)
	docker build -q -t bergfried-protogen -f proto/Dockerfile proto
	docker run --rm -v "$(CURDIR)":/src -w /src bergfried-protogen sh -c \
		'protoc -I proto --go_out=proto/gen --go_opt=paths=source_relative --go-grpc_out=proto/gen --go-grpc_opt=paths=source_relative proto/bergfried/v1/bergfried.proto'

.PHONY: fakegithub
fakegithub: ## lokaler GitHub-Ersatz für Entwicklung/E2E-Tests (macOS arm64) -> bin/fakegithub
	mkdir -p bin
	$(DOCKER_GO) sh -c 'GOOS=darwin GOARCH=arm64 go build -o bin/fakegithub ./tools/fakegithub'
