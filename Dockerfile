FROM golang:1.24.0-alpine3.20 AS builder

# Install build dependencies including libwebp-dev and potentially libsharpyuv
RUN apk add --no-cache git libgit2-dev alpine-sdk libwebp-dev
# If you know the package name that provides libsharpyuv.so.0, install it here
# RUN apk add --no-cache <package-name>

COPY --from=bufbuild/buf:latest /usr/local/bin/buf /usr/local/go/bin/

ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /grbpwr-manager

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY ./ ./

RUN make init

RUN make build

# Use the find command to locate libsharpyuv.so.0 in the builder stage
RUN find / -name "libsharpyuv.so.0"

FROM alpine:latest

RUN apk add --no-cache libstdc++ libwebp
# Correctly copy libsharpyuv.so.0 from the builder stage to the final stage
COPY --from=builder /usr/lib/libsharpyuv.so.0 /usr/lib/

COPY --from=builder /grbpwr-manager/bin/products-manager /usr/local/bin/products-manager

# Create certs directory for backward compatibility (file-based certs)
# Note: DigitalOcean App Platform provides db.CA_CERT env var automatically,
# so cert files are optional. Directory is created in case file-based certs are used.
RUN mkdir -p /etc/grbpwr-products-manager/certs

WORKDIR /

EXPOSE 8081

# Config file is optional - if not provided, app will use env vars only
# You can mount a config file or use env vars
ENTRYPOINT ["products-manager"]