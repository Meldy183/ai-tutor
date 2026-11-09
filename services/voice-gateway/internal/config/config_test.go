package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCommittedConfigAndVoiceRevision(t *testing.T) {
	c, err := Load("../../config/dev.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.HoldTimeout("voxcpm-1"); got != 340*time.Second {
		t.Fatalf("tts hold timeout: %s", got)
	}
	if got := c.HoldTimeout("gigaam-1"); got != 40*time.Second {
		t.Fatalf("stt hold timeout: %s", got)
	}
	if got := c.HoldTimeout("missing"); got != 0 {
		t.Fatalf("unknown instance hold timeout: %s", got)
	}
	abortable := Config{TTS: Pool{Instances: []Instance{{ID: "tts", AbortOnDisconnect: true}}}}
	if !abortable.AbortOnDisconnect("tts") || abortable.AbortOnDisconnect("missing") {
		t.Fatal("abort setting looked up the wrong instance")
	}
	v := c.Voices["geek-default"]
	first := v.Revision()
	v.Seed++
	if first == v.Revision() {
		t.Fatal("seed omitted from revision")
	}
	v = c.Voices["geek-default"]
	v.Identity = "another reference"
	if first == v.Revision() {
		t.Fatal("voice identity omitted")
	}
}
func TestInvalidConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../config/dev.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"unknown":       string(body) + "unknown: true\n",
		"duplicate":     string(body) + "log_level: warn\n",
		"multi":         string(body) + "---\nlog_level: info\n",
		"bad bool":      strings.Replace(string(body), "longform: false", "longform: yes", 1),
		"string number": strings.Replace(string(body), "slots: 1", "slots: '1'", 1),
		"float slots":   strings.Replace(string(body), "slots: 1", "slots: 1.5", 1),
		"empty pool":    strings.Replace(string(body), "max_waiting: 32", "max_waiting: 0", 1),
		"no budget":     strings.Replace(string(body), "min_call_budget: 60s", "min_call_budget: 0s", 1),
		"long budget":   strings.Replace(string(body), "min_call_budget: 60s", "min_call_budget: 370s", 1),
		"no drain":      strings.Replace(string(body), "shutdown_drain: 75s", "shutdown_drain: 0s", 1),
		"bad time":      strings.Replace(string(body), "request_timeout: 370s", "request_timeout: 370", 1),
		"same topics":   strings.Replace(string(body), "voice.tts.v1", "voice.stt.v1", 1),
		"bad profile":   strings.Replace(string(body), "profiles: [geek-default]", "profiles: [absent]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = Load(path); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestShutdownDrainFitsStopGracePeriod(t *testing.T) {
	setting := func(path, name string) time.Duration {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		match := regexp.MustCompile(name + `: (\S+)`).FindSubmatch(body)
		if match == nil {
			t.Fatalf("%s has no %s", path, name)
		}
		value, err := time.ParseDuration(string(match[1]))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	grace := setting("../../docker-compose.yaml", "stop_grace_period")
	for _, path := range []string{"../../config/dev.yaml", "../../config/prod.yaml"} {
		if drain := setting(path, "shutdown_drain"); drain >= grace {
			t.Fatalf("%s: shutdown_drain %s does not fit stop_grace_period %s", path, drain, grace)
		}
	}
}

func TestReceiverTokenConfiguration(t *testing.T) {
	c, err := Load("../../config/dev.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c.STT.Instances[0].Token = "local-development-client-token-0001"
	c.TTS.Instances[0].Token = "local-development-client-token-0001"
	p := c.Provider()
	if p.STT[c.STT.Instances[0].ID].Token != c.STT.Instances[0].Token || p.TTS[c.TTS.Instances[0].ID].Token != c.TTS.Instances[0].Token {
		t.Fatal("receiver token not forwarded to provider")
	}
	c.STT.Instances[0].Token = "token\r\nInjected: header"
	if err := c.Validate(); err == nil {
		t.Fatal("unsafe token accepted")
	}
}
