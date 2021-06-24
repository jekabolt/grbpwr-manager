FROM golang:1.14.3-alpine

ENV GO111MODULE=on

RUN apk add --no-cache git libgit2-dev alpine-sdk

WORKDIR /go/src/github.com/jekabolt/grbpwr-manager
COPY .gitconfig /root/.gitconfig
COPY go.mod .
COPY go.sum .
ENV GOPRIVATE=gitlab.com/dvision,gitlab.com/miapago
# install dependencies
RUN go mod download

COPY ./cmd/ ./cmd/
COPY ./store/ ./store/
COPY ./app/ ./app/

RUN go build -o ./bin/grbpwr-pm ./cmd/

FROM alpine:latest

WORKDIR /go/src/github.com/jekabolt/grbpwr-manager
# RUN apk update && apk add ca-certificates && rm -rf /var/cache/apk/*
# RUN apk add --no-cache git libgit2-dev alpine-sdk
# RUN apk --no-cache add curl

COPY --from=0 /go/src/github.com/jekabolt/grbpwr-manager .

EXPOSE 80

CMD ["/go/src/github.com/jekabolt/grbpwr-manager/bin/grbpwr-pm"]
