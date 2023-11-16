DEFAULT_GOAL := up
VERSION := $(shell git describe --tags --always --long |sed -e "s/^v//")
GO_LINT_VERSION := v1.53.3

.PHONY: generate internal/statics proto

init: clean install proto generate
	
generate:
	go generate ./...

proto: format-proto
	buf generate 

format-proto:
	find ./proto -name '*.proto' -exec buf format {} -o {} \;

lint-proto:
	buf lint

build: format-proto proto generate internal/statics build-only

build-only:
	mkdir -p bin
	go build $(GO_EXTRA_BUILD_ARGS) -ldflags "-s -w -X main.version=$(VERSION)" -o bin/products-manager ./cmd/*.go

run: build
	source .env && ./bin/products-manager
	
internal/statics:
	@echo "Generating combined Swagger JSON"
	@find proto/swagger -type f -name "*.json" -exec cp {} proto/swagger \;
	@GOOS="" GOARCH="" go run proto/swagger/main.go proto/swagger > internal/api/http/static/swagger/api.swagger.json
	@find proto/swagger -type f -name "*.json" -exec mv {} internal/api/http/static/swagger \;

clean:
	find proto -type f \( -name "*.pb.go" -o -name "*.gw.go" \) -delete
	find . -type f \( -name "*.swagger.json" \) -delete
	rm -rf bin
	rm -rf `find . -type d -name mocks`

lint:
	golangci-lint run ./internal/...

cov:
	go test -cover -coverprofile coverage.out -coverpkg ./internal/... ./...
	# IMPORTANT: required coverage can only be increased
	go tool cover -func coverage.out | \
		awk 'END { print "Coverage: " $$3; if ($$3+0 < 0) { print "Insufficient coverage"; exit 1; } }'

golangci-lint:
	docker pull golangci/golangci-lint:$(GO_LINT_VERSION)
	docker run --rm -v $$(pwd):/app -v ~/.netrc:/root/.netrc -w /app golangci/golangci-lint:$(GO_LINT_VERSION) golangci-lint run ./...

install:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway@latest
	go install github.com/grpc-ecosystem/grpc-gateway/protoc-gen-swagger@latest
	go install golang.org/x/text/cmd/gotext@latest
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@latest
	go install github.com/vektra/mockery/v2@latest 


REGISTRY=grbpwr
IMAGE_NAME=grbpwr-pm
VERSION=master

image:
	docker build -t $(REGISTRY)/${IMAGE_NAME}:$(VERSION) .

image-run:
	docker rm -f product_manager &>/dev/null && echo 'Removed old container'
	docker run -d --rm --name product_manager\
		-v ${PWD}/config:/config \
		-p 8081:8081 \
		grbpwr/grbpwr-pm:master