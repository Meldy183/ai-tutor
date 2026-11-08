package main

import "testing"

func TestConfigArgumentAndCommands(t *testing.T) {
	t.Setenv("VOICE_GATEWAY_CONFIG", "ignored.yaml")
	for _, test := range []struct {
		args  []string
		path  string
		count int
	}{
		{nil, "config/dev.yaml", 0},
		{[]string{"--config", "/config/prod.yaml"}, "/config/prod.yaml", 0},
		{[]string{"--config", "/config/prod.yaml", "healthcheck"}, "/config/prod.yaml", 1},
		{[]string{"--config", "/config/prod.yaml", "release-held", "gigaam-1", "--upstream-stopped"}, "/config/prod.yaml", 3},
	} {
		path, args, err := commandLine(test.args)
		if err != nil || path != test.path || len(args) != test.count {
			t.Fatalf("arguments %v: path %q command %v error %v", test.args, path, args, err)
		}
	}
	for _, args := range [][]string{{"--config"}, {"unknown"}, {"healthcheck", "extra"}, {"release-held", "gigaam-1"}} {
		if _, _, err := commandLine(args); err == nil {
			t.Fatalf("invalid invocation accepted: %v", args)
		}
	}
}
