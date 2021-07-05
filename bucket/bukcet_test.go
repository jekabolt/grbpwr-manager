package bucket

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"testing"

	"github.com/minio/minio-go"
)

const DOAccessKey = "C5REP77P6P2GTSNYCFUN"
const DOSecretAccessKey = "A+i5k+mOQAV/9vjY9c1e6m9xpODzAexwVYpOuptgA1k"
const DOEndpoint = "fra1.digitaloceanspaces.com"
const bucketName = "grbpwr"
const objectName = "test.png"
const filePath = "./test.png"
const contentType = "image/png"

const b64Image = ""

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

	_, ft, err := B64ToImage(i)

	fmt.Println(ft.Extension)
	fmt.Println(ft.MIMEType)

	// _, err = client.PutObject(bucketName, objectName, r, r.Size(), minio.PutObjectOptions{ContentType: contentType})

}
