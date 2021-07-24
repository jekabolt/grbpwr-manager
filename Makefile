REGISTRY=grbpwr
IMAGE_NAME=grbpwr-pm
VERSION=1.0.0

build:
	go build -o ./bin/$(IMAGE_NAME) ./cmd/

run: build
	./bin/$(IMAGE_NAME)

local: build
	source .env && ./bin/$(IMAGE_NAME)

image:
	docker build -t $(REGISTRY)/${IMAGE_NAME}:$(VERSION) .

image-run:
	docker run --publish 8081:8081 --env-file .env $(REGISTRY)/${IMAGE_NAME}:$(VERSION)
