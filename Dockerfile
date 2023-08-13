FROM golang:1.21rc4-alpine3.18

RUN apk add --no-cache git libgit2-dev alpine-sdk

COPY --from=bufbuild/buf:latest /usr/local/bin/buf /usr/local/go/bin/

ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /grbpwr-manager

COPY go.mod .
COPY go.sum .
# install dependencies
RUN go mod download

COPY ./ ./

RUN make init

RUN make build

FROM alpine:latest

COPY --from=0 /grbpwr-manager/bin/ /grbpwr-manager/bin/

EXPOSE 8081

ENTRYPOINT ["./grbpwr-manager/bin/products-manager"]
