FROM golang:1.24.0-alpine3.20 AS builder

# Install build dependencies including libwebp-dev
RUN apk add --no-cache git libgit2-dev alpine-sdk libwebp-dev

# Copy buf tool (cache this layer separately)
COPY --from=bufbuild/buf:latest /usr/local/bin/buf /usr/local/go/bin/

ENV PATH="/usr/local/go/bin:${PATH}"

# Build argument for commit hash (can be passed from CI/CD)
ARG COMMIT_HASH

WORKDIR /grbpwr-manager

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Install Go tools (cache this layer - tools rarely change)
RUN --mount=type=cache,target=/go/pkg/mod \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.21.0 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.4.0 && \
    go install github.com/grpc-ecosystem/grpc-gateway/protoc-gen-swagger@latest && \
    go install golang.org/x/text/cmd/gotext@latest && \
    go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@latest && \
    go install github.com/vektra/mockery/v2@v2.52.2 && \
    go install github.com/deepmap/oapi-codegen/cmd/oapi-codegen@latest


COPY proto/ ./proto/
COPY buf.gen.yaml buf.work.yaml ./


RUN buf generate

COPY openapi/ ./openapi/

RUN mkdir -p openapi/gen/resend && \
    oapi-codegen -package resend -generate types,client -o openapi/gen/resend/resend.gen.go openapi/resend/openapi.yaml

COPY ./ ./

RUN --mount=type=cache,target=/go/pkg/mod \
    mockery && \
    find internal/dependency/mocks -name "*.go" -type f -exec sed -i 's/^package dependency$$/package mocks/' {} \; && \
    go generate ./...

RUN find proto/swagger -type f -name "*.json" -exec cp {} proto/swagger \; && \
    GOOS="" GOARCH="" go run proto/swagger/main.go proto/swagger > internal/api/http/static/swagger/api.swagger.json && \
    find proto/swagger -type f -name "*.json" -exec mv {} internal/api/http/static/swagger \;

# Get commit hash and build
RUN COMMIT_HASH_VALUE="$COMMIT_HASH"; \
    if [ -z "$COMMIT_HASH_VALUE" ]; then \
      COMMIT_HASH_VALUE=`git rev-parse --short HEAD 2>/dev/null || echo "unknown"`; \
    fi && \
    export COMMIT_HASH="$COMMIT_HASH_VALUE" && \
    VERSION=$(git describe --tags --always --long |sed -e "s/^v//") && \
    mkdir -p bin && \
    go build -ldflags "-s -w -X main.version=$VERSION -X main.commitHash=$COMMIT_HASH" -o bin/products-manager ./cmd/*.go

FROM alpine:latest

# Install runtime dependencies including libwebp
RUN apk add --no-cache libstdc++ libwebp ca-certificates

COPY --from=builder /grbpwr-manager/bin/products-manager /usr/local/bin/products-manager

# Ensure the binary is executable
RUN chmod +x /usr/local/bin/products-manager

# Create certs directory for backward compatibility (file-based certs)
# Note: DigitalOcean App Platform provides db.CA_CERT env var automatically,
# so cert files are optional. Directory is created in case file-based certs are used.
RUN mkdir -p /etc/grbpwr-products-manager/certs

WORKDIR /

EXPOSE 8081

# Use full path to binary to avoid PATH issues
ENTRYPOINT ["/usr/local/bin/products-manager"]