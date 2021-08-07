package bucket

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/caarlos0/env/v6"
	"github.com/minio/minio-go"
)

const DOAccessKey = "xxx"
const DOSecretAccessKey = "xxx"
const DOEndpoint = "fra1.digitaloceanspaces.com"
const bucketName = "grbpwr"
const objectName = "test.png"
const filePath = "/Users/jekabolt/go/src/github.com/jekabolt/Angular-Reactive-Demo-Shop/src/img/grb-logo.png"
const contentType = "image/png"

const b64Image = ""

func imageToB64_v2(filePath string) (string, error) {
	ff, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	b := make([]byte, len(ff)*2)
	base64.StdEncoding.Encode(b, ff)
	return string(b), nil
}

func imageToB64(filePath string) (string, error) {
	bytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	var base64Encoding string

	// Determine the content type of the image file
	mimeType := http.DetectContentType(bytes)

	// Prepend the appropriate URI scheme header depending
	// on the MIME type
	switch mimeType {
	case "image/jpeg":
		base64Encoding += "data:image/jpeg;base64,"
	case "image/png":
		base64Encoding += "data:image/png;base64,"
	}

	// Append the base64 encoded output
	base64Encoding += toBase64(bytes)

	return base64Encoding, nil
}

func toBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func TestClient(t *testing.T) {
	client, err := minio.New(DOEndpoint, DOAccessKey, DOSecretAccessKey, true)
	if err != nil {
		log.Fatal(err)
	}

	spaces, err := client.ListBuckets()
	if err != nil {
		log.Fatal("list err ", err)
	}

	// err = client.MakeBucket("grbpwr-com", "fra-1")
	// if err != nil {
	// 	log.Fatal("MakeBucket err ", err)
	// }

	for _, space := range spaces {
		fmt.Println(space.Name)
	}

	i, err := imageToB64(filePath)
	if err != nil {
		log.Fatal("imageToB64 err ", err)
	}

	r, ft, err := B64ToImage(i)

	fmt.Println(ft.Extension)
	fmt.Println(ft.MIMEType)
	fmt.Printf("client %+v ", client)

	_, err = client.PutObject(bucketName, objectName, r, r.Size(), minio.PutObjectOptions{ContentType: contentType})

}

func TestUploadImage(t *testing.T) {
	b := &Bucket{}
	err := env.Parse(b)
	if err != nil {
		log.Fatal("Parse err ", err)
	}

	b.DOAccessKey = DOAccessKey
	b.DOSecretAccessKey = DOSecretAccessKey
	b.DOEndpoint = DOEndpoint

	cli, err := minio.New(b.DOEndpoint, b.DOAccessKey, b.DOSecretAccessKey, true)
	if err != nil {
		log.Fatal("InitBucket err ", err)
	}
	b.Client = cli

	i, err := imageToB64(filePath)
	if err != nil {
		log.Fatal("imageToB64 err ", err)
	}
	fmt.Printf("------ ss")

	fp, err := b.UploadImage(i)
	fmt.Println("--- ", fp)
	fmt.Println("--- ", err)
	// https://grbpwr.fra1.digitaloceanspaces.com/grbpwr-com/2021/July/1626540861.png
	// https://grbpwr.fra1.cdn.digitaloceanspaces.com/grbpwr-com/2021/July/1626540861.png
}

func TestGetCDNPath(t *testing.T) {
	b := &Bucket{}
	err := env.Parse(b)
	if err != nil {
		log.Fatal("Parse err ", err)
	}
	path := "grbpwr-com/2021/July/1626540861.png"
	fmt.Printf("%s ", b.GetCDNURL(path))
	// https://grbpwr.fra1.digitaloceanspaces.com/grbpwr-com/2021/July/1626540861.png
	// https://grbpwr.fra1.digitaloceanspaces.com/grbpwr-com/2021/July/1626540861.png
}

func TestConvert(t *testing.T) {

	i, err := imageToB64(filePath)
	if err != nil {
		log.Fatal("imageToB64 err ", err)
	}

	fmt.Println("i   ", http.DetectContentType([]byte(i)))

	return

	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(i))

	imageData, _, err := image.Decode(reader)
	if err != nil {
		log.Fatal("Decode err ", err)
	}

	var b bytes.Buffer
	imageOut := bufio.NewWriter(&b)

	opts := &jpeg.Options{
		Quality: 1,
	}

	err = jpeg.Encode(imageOut, imageData, opts)
	if err != nil {
		log.Fatal("Encode err ", err)
	}

	r := bytes.NewReader(b.Bytes())

	///

	///

	client, err := minio.New(DOEndpoint, DOAccessKey, DOSecretAccessKey, true)
	if err != nil {
		log.Fatal(err)
	}

	_, err = client.PutObject(bucketName, objectName, r, r.Size(), minio.PutObjectOptions{ContentType: contentType})

}
