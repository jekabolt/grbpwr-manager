package bucket

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"io"
	"io/ioutil"

	"github.com/minio/minio-go"
)

type Bucket struct {
	*minio.Client

	DOAccessKey       string `env:"DO_ACCESS_KEY" envDefault:"xxx"`
	DOSecretAccessKey string `env:"DO_SECRET_ACCESS_KEY" envDefault:"xxx"`
	DOEndpoint        string `env:"DO_ENDPOINT" envDefault:"fra1.digitaloceanspaces.com"`
	DOBucketName      string `env:"DO_BUCKET_NAME" envDefault:"grbpwr"`
	DOBucketLocation  string `env:"DO_BUCKET_LOCATION" envDefault:"fra-1"`
	ImageStorePrefix  string `env:"IMAGE_STORE_PREFIX" envDefault:"grbpwr-com"`
}

func (b *Bucket) InitBucket() error {
	cli, err := minio.New(b.DOEndpoint, b.DOAccessKey, b.DOSecretAccessKey, true)
	if err != nil {
		return err
	}
	b.Client = cli
	return nil
}

// func (b *Bucket) UploadToBucket(img *bufio.Reader, contentType string) (string, error) {
func (b *Bucket) UploadToBucket(img io.Reader, contentType string) (string, error) {

	fp := b.getImageFullPath(fileExtensionFromContentType(contentType))

	userMetaData := map[string]string{"x-amz-acl": "public-read"} // make it public
	cacheControl := "max-age=31536000"

	bs, _ := ioutil.ReadAll(img)
	r := bytes.NewReader(bs)

	_, err := b.PutObject(b.DOBucketName, fp, r, int64(len(bs)), minio.PutObjectOptions{ContentType: contentType, CacheControl: cacheControl, UserMetadata: userMetaData})
	if err != nil {
		return "", fmt.Errorf("PutObject:err [%v]", err.Error())
	}

	return b.GetCDNURL(fp), nil
}

func (b *Bucket) UploadImage(body []byte, contentType string) (string, error) {
	var img image.Image
	var err error

	switch contentType {
	case "image/jpeg":
		img, err = JPGFromB64(string(body))
		if err != nil {
			return "", fmt.Errorf("uploadImage:JPGFromB64: [%v]", err.Error())
		}
	case "image/png":
		img, err = PNGFromB64(string(body))
		if err != nil {
			return "", fmt.Errorf("uploadImage:PNGFromB64: [%v]", err.Error())
		}

	default:
		return "", fmt.Errorf("uploadImage:PNGFromB64: File type is not supported [%s]", contentType)
	}

	var buf bytes.Buffer
	imgWriter := bufio.NewWriter(&buf)

	err = EncodeJPG(imgWriter, img)
	if err != nil {
		return "", fmt.Errorf("uploadImage:Encode: [%v]", err.Error())
	}

	// imgReader := bufio.NewReader(&buf)
	url, err := b.UploadToBucket(io.TeeReader(&buf, imgWriter), "image/jpeg")
	if err != nil {
		return "", fmt.Errorf("uploadImage:UploadToBucket: [%v]", err.Error())
	}

	return url, nil
}
