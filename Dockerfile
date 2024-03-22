FROM golang:1.21rc4-alpine3.18 as builder

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

RUN apk add --no-cache libstdc++

COPY --from=builder /usr/lib/libwebp.so.7 /usr/lib/
COPY --from=builder /usr/lib/libwebp*.so* /usr/lib/
# Adjust this line according to the actual location of libsharpyuv.so.0
COPY --from=builder /path/to/libsharpyuv.so.0 /usr/lib/
COPY --from=builder /grbpwr-manager/bin/ /grbpwr-manager/bin/

EXPOSE 8081

ENTRYPOINT ["./grbpwr-manager/bin/products-manager"]