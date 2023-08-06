FROM golang:1.21rc4-alpine3.18

RUN apk add --no-cache git libgit2-dev alpine-sdk

COPY --from=bufbuild/buf:latest /usr/local/bin/buf /usr/local/go/bin/

ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /go/src/github.com/jekabolt/grbpwr-manager

COPY go.mod .
COPY go.sum .
# install dependencies
RUN go mod download

COPY ./ ./

RUN make init

RUN make build

FROM alpine:latest

WORKDIR /go/src/github.com/jekabolt/grbpwr-manager

COPY --from=0 /go/src/github.com/jekabolt/grbpwr-manager .

EXPOSE 8081

CMD ["/go/src/github.com/jekabolt/grbpwr-manager/bin/products-manager"]
