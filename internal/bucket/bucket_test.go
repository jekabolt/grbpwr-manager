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
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
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

	mediaStoreMock *mocks.MockMedia
}

// BucketFromConfig builds a bucket wired to the REAL DigitalOcean Spaces credentials in
// config/config.toml. The three tests below are live-network integration tests: without that file
// there is nothing to talk to, and the honest verdict is "not run here", not "failed".
//
// IT RETURNS NO ERROR, AND THAT IS THE POINT.
//
// It used to return (nil, error), and all three call sites wrote `assert.NoError(t, err)` — assert,
// not require. assert MARKS the test failed and lets it run on, so on a machine without the config
// every one of them walked into `tb.mediaStoreMock` holding a nil `tb`. A nil dereference in a test
// is not a failed test: it PANICS THE WHOLE TEST BINARY, and every test the package had not reached
// yet simply never ran. `go test ./internal/bucket/` reported 12 outcomes out of 58 and called the
// difference "known redness". The gate was lying, and it was lying silently.
//
// Handing back only a usable store removes the shape in which that mistake can be written: there is
// no error to under-assert and no nil to dereference. A missing config skips (this machine cannot
// run these); a config that is present but does not work is a t.Fatalf — a real failure, still not
// a panic, and the tests after it still run.
func BucketFromConfig(t *testing.T) *testFileStore {
	t.Helper()
	skipCI(t)

	cfg, err := loadConfig("")
	if err != nil {
		t.Skipf("skipping live bucket test: config/config.toml with real S3 credentials is required "+
			"and could not be read (%v); run these from a checkout that has it", err)
	}

	mediaStoreMock := mocks.NewMockMedia(t)
	fs, err := New(cfg, mediaStoreMock)
	if err != nil {
		// Credentials ARE configured and do not work — a genuine failure worth seeing, unlike
		// their absence. Fatalf ends this test only; the binary lives.
		t.Fatalf("bucket configured but unusable: %v", err)
	}

	return &testFileStore{
		fs:             fs,
		mediaStoreMock: mediaStoreMock,
	}
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

	tb := BucketFromConfig(t)

	tb.mediaStoreMock.EXPECT().AddMedia(ctx, mock.Anything).Return(1, nil)

	jpg, err := fileToB64ByPath(jpgFilePath)
	assert.NoError(t, err)
	fmt.Println("jpg ", jpg)

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

	tb := BucketFromConfig(t)

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

	tb := BucketFromConfig(t)

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
