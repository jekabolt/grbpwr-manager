FROM golang:1.23.0-alpine3.20 as builder

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

# COPY other necessary libwebp related shared libraries from the builder stage
COPY --from=builder /usr/lib/libwebp.so.7 /usr/lib/
COPY --from=builder /usr/lib/libwebp*.so* /usr/lib/
# Correctly copy libsharpyuv.so.0 from the builder stage to the final stage
COPY --from=builder /usr/lib/libsharpyuv.so.0 /usr/lib/

COPY --from=builder /grbpwr-manager/bin/ /grbpwr-manager/bin/

EXPOSE 8081

ENTRYPOINT ["./grbpwr-manager/bin/products-manager"]