# grbpwr-manager

service for upload and manage stock products and articles on [grbpwr.com](https://grbpwr.com)

# How to run

#### for local development

```shell script
    make run
```

#### Build docker image

```shell script
    make image
```

#### Run in docker

```shell script
    make image-run
```

## Env variables needed to deploy

#### router

```
PORT=8081
HOST=localhost:8080
ORIGIN=*
```

#### bucket

```
S3_ACCESS_KEY=xxx
S3_SECRET_ACCESS_KEY=xxx
S3_ENDPOINT=fra1.digitaloceanspaces.com
S3_BUCKET_NAME=grbpwr
S3_BUCKET_LOCATION=fra-1
IMAGE_STORE_PREFIX=grbpwr-com
```

#### db

Maybe I'll implement redis as storage 

```
STORAGE_TYPE=bunt
STORAGE_TYPE=redis # not implemented

```

#### auth 

```
JWT_SECRET=xxx
ADMIN_SECRET=xxx # password for access protected api routes

```


```
BUNT_DB_PRODUCTS_PATH=/root/bunt/products.db
BUNT_DB_ARTICLES_PATH=/root/bunt/articles.db
BUNT_DB_SALES_PATH=/root/bunt/sales.db

```

## Swagger for requests

can be found [here](https://github.com/jekabolt/grbpwr-manager/tree/master/doc)
