# Go wird in Docker gebaut – lokal ist kein Go nötig.
GO_IMAGE ?= golang:1.26
DOCKER_GO = docker run --rm \
	-v "$(CURDIR)":/src -w /src \
	-v bergfried-gomod:/go/pkg/mod -v bergfried-gobuild:/root/.cache/go-build \
	-e CGO_ENABLED=0 $(GO_IMAGE)

.PHONY: build test vet fmt
build: ## bergfriedd (macOS arm64) und bergfried-mcp (Linux arm64)
	mkdir -p bin
	$(DOCKER_GO) sh -c 'GOOS=darwin GOARCH=arm64 go build -o bin/bergfriedd ./daemon/cmd/bergfriedd && GOOS=linux GOARCH=arm64 go build -o bin/bergfried-mcp ./mcp/cmd/bergfried-mcp'

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
