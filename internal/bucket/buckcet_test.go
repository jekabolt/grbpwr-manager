package bucket

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func loadConfig(cfgFile string) (*Config, error) {
	viper.SetConfigType("toml")
	viper.SetConfigFile(cfgFile)
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("../../config")
		viper.AddConfigPath("/usr/local/config")
	}

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %v", err)
	}

	var config Config

	err := viper.UnmarshalKey("bucket", &config)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config into struct: %v", err)
	}

	fmt.Printf("conf---- %+v", config)
	return &config, nil
}

const (
	jpgFilePath  = "files/test.jpg"
	mp4FilePath  = "files/test.mp4"
	webmFilePath = "files/test.webm"
)

func skipCI(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}
}

type testFileStore struct {
	fs dependency.FileStore

	mediaStoreMock *mocks.Media
}

func BucketFromConfig(t *testing.T) (*testFileStore, error) {
	skipCI(t)
	cfg, err := loadConfig("")
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	mediaStoreMock := mocks.NewMedia(t)
	fs, err := New(cfg, mediaStoreMock)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MinIO client: %w", err)
	}

	return &testFileStore{
		fs:             fs,
		mediaStoreMock: mediaStoreMock,
	}, nil
}

func fileToBytes(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	fileStat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	fmt.Println("fileStat ", fileStat.Size())

	return io.ReadAll(file)
}

func TestUploadContentImage(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	tb, err := BucketFromConfig(t)
	assert.NoError(t, err)

	tb.mediaStoreMock.EXPECT().AddMedia(ctx, mock.Anything).Return(1, nil)

	jpg, err := fileToB64ByPath(jpgFilePath)
	assert.NoError(t, err)

	i, err := tb.fs.UploadContentImage(ctx, jpg, "test", "test")
	assert.NoError(t, err)
	t.Logf("%+v", i)

	// err = tb.fs.DeleteFromBucket(ctx, i.ObjectIds)
	assert.NoError(t, err)
}

func fileToB64ByPath(filePath string) (string, error) {
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	var base64Encoding string

	// Determine the content type of the file
	mimeType := http.DetectContentType(bytes)

	base64Encoding += fmt.Sprintf("data:%s;base64,", mimeType)

	// Append the base64 encoded output
	base64Encoding += base64.StdEncoding.EncodeToString(bytes)

	return base64Encoding, nil
}

func TestUploadContentVideoMP4(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	tb, err := BucketFromConfig(t)
	assert.NoError(t, err)

	tb.mediaStoreMock.EXPECT().AddMedia(ctx, mock.Anything).Return(1, nil)

	mp4, err := fileToBytes(mp4FilePath)
	assert.NoError(t, err)

	media, err := tb.fs.UploadContentVideo(ctx, mp4, "test", "test", string(contentTypeMP4))
	assert.NoError(t, err)
	fmt.Printf("----- %+v", media)

	// err = tb.fs.DeleteFromBucket(ctx, i.ObjectIds)
	assert.NoError(t, err)
}

func TestUploadContentVideoWEBM(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	tb, err := BucketFromConfig(t)
	assert.NoError(t, err)

	tb.mediaStoreMock.EXPECT().AddMedia(ctx, mock.Anything).Return(1, nil)

	mp4, err := fileToBytes(webmFilePath)
	assert.NoError(t, err)

	media, err := tb.fs.UploadContentVideo(ctx, mp4, "test", "test", string(contentTypeWEBM))
	assert.NoError(t, err)
	fmt.Printf("----- %+v", media)

	// err = tb.fs.DeleteFromBucket(ctx, i.ObjectIds)
	assert.NoError(t, err)
}

// func TestGetB64FromUrl(t *testing.T) {
// 	skipCI(t)
// 	url := "https://grbpwr.fra1.digitaloceanspaces.com/grbpwr-com/2022/April/1650908019115367000-og.jpg"

// 	rawImage, err := getMediaB64(url)
// 	assert.NoError(t, err)

// 	fmt.Println("--- b64", rawImage.B64Image)
// 	fmt.Println("--- ext", rawImage.Extension)

// }
