
FROM golang:1.21rc4-alpine3.18 as builder

RUN apk add --no-cache git libgit2-dev alpine-sdk libwebp-dev

RUN find /usr/lib /lib -name "libwebp.so.7" -exec dirname {} \; > /libwebpdir

COPY --from=bufbuild/buf:latest /usr/local/bin/buf /usr/local/go/bin/

ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /grbpwr-manager

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY ./ ./

RUN make init

RUN make build

FROM alpine:latest

RUN apk add --no-cache libstdc++

COPY --from=builder /libwebpdir/libwebp.so.7 /usr/lib/

COPY --from=builder /grbpwr-manager/bin/ /grbpwr-manager/bin/

EXPOSE 8081

ENTRYPOINT ["./grbpwr-manager/bin/products-manager"]
