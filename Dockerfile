FROM golang:1.26.0-alpine3.22 AS builder

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

# Ensure grpc-gateway/v2 is available before proto generation (needed by generated code)
RUN --mount=type=cache,target=/go/pkg/mod \
    go get github.com/grpc-ecosystem/grpc-gateway/v2@v2.21.0

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

# Download dependencies again after proto generation (in case new deps were needed)
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && \
    go mod tidy

# Get commit hash and build (handle missing git gracefully)
RUN COMMIT_HASH_VALUE="$COMMIT_HASH"; \
    if [ -z "$COMMIT_HASH_VALUE" ]; then \
      COMMIT_HASH_VALUE=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown"); \
    fi && \
    export COMMIT_HASH="$COMMIT_HASH_VALUE" && \
    VERSION=$(git describe --tags --always --long 2>/dev/null | sed -e "s/^v//" || echo "dev-unknown") && \
    mkdir -p bin && \
    go build -ldflags "-s -w -X main.version=$VERSION -X main.commitHash=$COMMIT_HASH" -o bin/products-manager ./cmd/*.go

FROM alpine:latest

# Install runtime dependencies including libwebp
RUN apk add --no-cache libstdc++ libwebp ca-certificates

COPY --from=builder /grbpwr-manager/bin/products-manager /usr/local/bin/products-manager

# Ensure the binary is executable
RUN chmod +x /usr/local/bin/products-manager

# Create certs directory and bake in the DigitalOcean managed-DB CA certificate.
# The CA cert is public (not a private key). Point MYSQL_TLS_CA_PATH at this path
# to get tls=custom with full server-certificate verification.
RUN mkdir -p /etc/grbpwr-products-manager/certs
COPY --from=builder /grbpwr-manager/config/certs/ca-certificate.crt /etc/grbpwr-products-manager/certs/ca-certificate.crt

WORKDIR /

# Run as an unprivileged user. The app binds :8081 (>1024), writes nothing to the
# local filesystem (media -> S3, state -> remote MySQL), and only reads the public
# CA cert, so it does not need root.
RUN addgroup -S app && adduser -S -u 10001 -G app app
USER app

EXPOSE 8081

# Use full path to binary to avoid PATH issues
ENTRYPOINT ["/usr/local/bin/products-manager"]