REGISTRY=grbpwr
IMAGE_NAME=grbpwr-pm
VERSION=1.0.0
DB_PATH=/Users/jekabolt/code/go/src/github.com/jekabolt/grbpwr-products-manager/bunt'

build:
	go build -o ./bin/$(IMAGE_NAME) ./cmd/

run: build
	./bin/$(IMAGE_NAME)

local: build
	source .env && ./bin/$(IMAGE_NAME)

image:
	docker build -t $(REGISTRY)/${IMAGE_NAME}:$(VERSION) .

image-run:
	docker run --publish 8081:8081 --env-file .env \
	--mount src=/root/bunt,target=/root/bunt,type=bind \$(REGISTRY)/${IMAGE_NAME}:$(VERSION)
