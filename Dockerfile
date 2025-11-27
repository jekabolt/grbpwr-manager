FROM golang:1.24.0-alpine3.20 AS builder

# Install build dependencies including libwebp-dev
RUN apk add --no-cache git libgit2-dev alpine-sdk libwebp-dev

COPY --from=bufbuild/buf:latest /usr/local/bin/buf /usr/local/go/bin/

ENV PATH="/usr/local/go/bin:${PATH}"

# Build argument for commit hash (can be passed from CI/CD)
ARG COMMIT_HASH

WORKDIR /grbpwr-manager

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY ./ ./

RUN make init

# Get commit hash: use build arg if provided, otherwise try git, fallback to "unknown"
# Compute it explicitly and pass to make build
RUN if [ -n "$$COMMIT_HASH" ]; then \
      export COMMIT_HASH="$$COMMIT_HASH"; \
    else \
      export COMMIT_HASH=$$(git rev-parse --short HEAD 2>/dev/null || echo "unknown"); \
    fi && \
    make build

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