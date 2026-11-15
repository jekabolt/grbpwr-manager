package toolchainpin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCodeGeneratorsAreVersionPinned(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, name := range []string{"Dockerfile", "Makefile", "buf.gen.yaml"} {
		contents, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(contents)
		for _, movingVersion := range []string{":latest", "@latest"} {
			if strings.Contains(text, movingVersion) {
				t.Errorf("%s contains moving version %q", name, movingVersion)
			}
		}
	}

	contents, err := os.ReadFile(filepath.Join(repoRoot, "buf.gen.yaml"))
	if err != nil {
		t.Fatalf("read buf.gen.yaml: %v", err)
	}
	remotePlugin := regexp.MustCompile(`(?m)^\s*- plugin:\s*(buf\.build/\S+)\s*$`)
	matches := remotePlugin.FindAllStringSubmatch(string(contents), -1)
	if len(matches) == 0 {
		t.Fatal("buf.gen.yaml contains no remote plugins")
	}
	for _, match := range matches {
		plugin := match[1]
		if strings.LastIndex(plugin, ":") < strings.LastIndex(plugin, "/") {
			t.Errorf("remote plugin is not version pinned: %s", plugin)
		}
	}
}
