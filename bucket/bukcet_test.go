package bucket

import (
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/minio/minio-go"
)

const DOAccessKey = "*"
const DOSecretAccessKey = "*"
const DOEndpoint = "fra1.digitaloceanspaces.com"
const bucketName = "grbpwr"
const objectName = "gbpwr-com/test.png"
const filePath = "./test.png"
const contentType = "image/png"

const b64Image = ""

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

	// _, err = client.FPutObject(bucketName, objectName, filePath, minio.PutObjectOptions{ContentType: contentType})
	// if err != nil {
	// 	log.Fatalln(err)
	// }

	r := strings.NewReader(b64Image)

	_, err = client.PutObject(bucketName, objectName, r, r.Size(), minio.PutObjectOptions{ContentType: contentType})

}
