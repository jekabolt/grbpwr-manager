# grbpwr-manager

service for upload and manage stock products and articles on [grbpwr.com](https://grbpwr.com)

# How to run

## for local development

```shell script
    make run
```

## Build docker image

```shell script
    make image
```

## Run in docker

```shell script
    make image-run
```

## Env variables needed to deploy

### router

```
PORT=8081
HOST=localhost:8080
ORIGIN=*
```

### bucket

```
DO_ACCESS_KEY=xxx
DO_SECRET_ACCESS_KEY=xxx
DO_ENDPOINT=fra1.digitaloceanspaces.com
DO_BUCKET_NAME=grbpwr
DO_BUCKET_LOCATION=fra-1
IMAGE_STORE_PREFIX=grbpwr-com
```

### db

BUNT_DB_PRODUCTS_PATH=/root/bunt/products.db
BUNT_DB_ARTICLES_PATH=/root/bunt/articles.db
BUNT_DB_SALES_PATH=/root/bunt/sales.db

## Swagger for requests

can be found [here](https://github.com/jekabolt/grbpwr-manager/tree/master/doc)
