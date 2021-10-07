FROM golang:1.17.1-alpine

ENV GO111MODULE=on

RUN apk add --no-cache git libgit2-dev alpine-sdk

WORKDIR /go/src/github.com/jekabolt/grbpwr-manager

COPY go.mod .
COPY go.sum .
# install dependencies
RUN go mod download

COPY ./ ./

RUN go build -o ./bin/grbpwr-pm ./cmd/

FROM alpine:latest

WORKDIR /go/src/github.com/jekabolt/grbpwr-manager

RUN mkdir -p /root/bunt

COPY --from=0 /go/src/github.com/jekabolt/grbpwr-manager .

EXPOSE 8081

CMD ["/go/src/github.com/jekabolt/grbpwr-manager/bin/grbpwr-pm"]
