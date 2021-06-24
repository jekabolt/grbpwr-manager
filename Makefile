REGISTRY=dvision
IMAGE_NAME=grbpwr-pm
VERSION=1.0.0

build:
	go build -o ./bin/$(IMAGE_NAME) ./cmd/

run: build
	./bin/$(IMAGE_NAME)

local: build
	source .env && ./bin/$(IMAGE_NAME)

image:
	docker build -t $(REGISTRY)/${IMAGE_NAME}:$(VERSION) -f ./docker/Dockerfile . 

compose: image
	docker-compose up -d
