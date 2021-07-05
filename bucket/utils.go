package bucket

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

type FileType struct {
	Extension string
	MIMEType  string
}

func B64ToImage(b64Image string) (*bytes.Reader, *FileType, error) {
	coI := strings.Index(b64Image, ",")
	rawImage := b64Image[coI+1:]

	decoded, err := base64.StdEncoding.DecodeString(rawImage)
	if err != nil {
		return nil, nil, err
	}

	ft := &FileType{}

	switch b64Image[:coI+1] {
	case "data:image/jpeg;base64,":
		ft.Extension = ".jpg"
		ft.MIMEType = "image/jpeg"
	case "data:image/png;base64,":
		ft.Extension = ".png"
		ft.MIMEType = "image/png"
	}

	return bytes.NewReader(decoded), ft, nil
}

func getImageFullPath(filenameExtension string) string {
	now := time.Now()
	return fmt.Sprintf("/%d/%s/%d.%s", now.Year(), now.Month().String(), now.Unix(), filenameExtension)
}
