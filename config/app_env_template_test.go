package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type appTemplateEnv struct {
	Key   string `yaml:"key"`
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
}

type appTemplateSpec struct {
	Envs     []appTemplateEnv `yaml:"envs"`
	Services []struct {
		GitHub struct {
			Branch       string `yaml:"branch"`
			DeployOnPush bool   `yaml:"deploy_on_push"`
		} `yaml:"github"`
	} `yaml:"services"`
}

var secretTemplateValue = regexp.MustCompile(`^\$\{SECRET:([A-Z0-9_]+)\}$`)

func TestAppEnvironmentTemplateIsSafeYAML(t *testing.T) {
	body, err := os.ReadFile("../.do/app-env.template.yaml")
	if err != nil {
		t.Fatalf("read environment app template: %v", err)
	}
	raw := string(body)
	for _, forbidden := range []string{"EV[", "sk_live", "whsec_", "doadmin"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("template contains forbidden literal %q", forbidden)
		}
	}

	var spec appTemplateSpec
	if err := yaml.Unmarshal(body, &spec); err != nil {
		t.Fatalf("parse environment app template as YAML: %v", err)
	}
	if len(spec.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(spec.Services))
	}
	if got := spec.Services[0].GitHub.Branch; got != "${ENV}" {
		t.Errorf("github branch = %q, want ${ENV}", got)
	}
	if spec.Services[0].GitHub.DeployOnPush {
		t.Error("deploy_on_push must start false")
	}

	header, _, ok := strings.Cut(raw, "alerts:")
	if !ok {
		t.Fatal("template has no alerts section")
	}
	seen := map[string]bool{}
	for _, env := range spec.Envs {
		if env.Type != "SECRET" || env.Value == "" {
			continue
		}
		match := secretTemplateValue.FindStringSubmatch(env.Value)
		if match == nil || match[1] != env.Key {
			t.Errorf("SECRET %s value = %q, want ${SECRET:%s}", env.Key, env.Value, env.Key)
			continue
		}
		if seen[env.Key] {
			t.Errorf("duplicate secret placeholder for %s", env.Key)
		}
		seen[env.Key] = true
		if !strings.Contains(header, fmt.Sprintf("${SECRET:%s}", env.Key)) {
			t.Errorf("header does not list secret placeholder for %s", env.Key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("template contains no non-empty secret placeholders")
	}
}
