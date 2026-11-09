// Package config loads the gateway's committed runtime YAML.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/provider"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/scheduler"
	"gopkg.in/yaml.v3"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag != "!!str" {
		return fmt.Errorf("duration must be a string with units")
	}
	v, err := time.ParseDuration(n.Value)
	d.Duration = v
	return err
}

type Instance struct {
	ID                string   `yaml:"id"`
	Token             string   `yaml:"token"`
	URL               string   `yaml:"url"`
	API               string   `yaml:"api"`
	Slots             int      `yaml:"slots"`
	Timeout           Duration `yaml:"timeout"`
	AbortOnDisconnect bool     `yaml:"abort_on_disconnect"`
	Model             string   `yaml:"model"`
	Longform          bool     `yaml:"longform"`
	Profiles          []string `yaml:"profiles"`
}
type Pool struct {
	MaxWaiting    int        `yaml:"max_waiting"`
	MinCallBudget Duration   `yaml:"min_call_budget"`
	Instances     []Instance `yaml:"instances"`
}
type Voice struct {
	// Identity identifies the reference voice installed on all compatible servers.
	Identity  string  `yaml:"identity"`
	Seed      int64   `yaml:"seed"`
	CFG       float64 `yaml:"cfg_value"`
	Steps     int     `yaml:"inference_timesteps"`
	Reference bool    `yaml:"use_reference"`
	Prompt    bool    `yaml:"use_prompt"`
}

func (v Voice) Options() provider.Options {
	return provider.Options{Seed: v.Seed, CFGValue: v.CFG, InferenceTimesteps: v.Steps, UseReference: v.Reference, UsePrompt: v.Prompt}
}
func (v Voice) Revision() string {
	b, _ := json.Marshal(struct {
		Voice    Voice
		Protocol string
	}{v, "voxcpm-suffix-v1"})
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

type Config struct {
	GRPCAddress     string           `yaml:"grpc_address"`
	HTTPAddress     string           `yaml:"http_address"`
	LogLevel        string           `yaml:"log_level"`
	DataDir         string           `yaml:"data_dir"`
	RequestTimeout  Duration         `yaml:"request_timeout"`
	ShutdownDrain   Duration         `yaml:"shutdown_drain"`
	Retention       Duration         `yaml:"retention"`
	MaxConnections  int              `yaml:"max_connections"`
	MaxPayloadBytes int64            `yaml:"max_payload_bytes"`
	Brokers         []string         `yaml:"brokers"`
	STTTopic        string           `yaml:"stt_topic"`
	TTSTopic        string           `yaml:"tts_topic"`
	STT             Pool             `yaml:"stt"`
	TTS             Pool             `yaml:"tts"`
	Voices          map[string]Voice `yaml:"voices"`
}

func Load(path string) (Config, error) {
	var c Config
	body, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	var root yaml.Node
	if err = yaml.Unmarshal(body, &root); err != nil {
		return c, err
	}
	if err = checkTypes(&root, reflect.TypeOf(c)); err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err = dec.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("exactly one YAML document is required")
	}
	return c, c.Validate()
}
func checkTypes(n *yaml.Node, t reflect.Type) error {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return fmt.Errorf("empty YAML")
		}
		return checkTypes(n.Content[0], t)
	}
	if t == reflect.TypeOf(Duration{}) {
		if n.Tag != "!!str" {
			return fmt.Errorf("duration must have units")
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("expected mapping")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fields[f.Tag.Get("yaml")] = f.Type
		}
		for i := 0; i < len(n.Content); i += 2 {
			if ft, ok := fields[n.Content[i].Value]; ok {
				if err := checkTypes(n.Content[i+1], ft); err != nil {
					return fmt.Errorf("%s: %w", n.Content[i].Value, err)
				}
			}
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("expected mapping")
		}
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Tag != "!!str" {
				return fmt.Errorf("mapping keys must be strings")
			}
			if err := checkTypes(n.Content[i+1], t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return fmt.Errorf("expected sequence")
		}
		for _, v := range n.Content {
			if err := checkTypes(v, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.String:
		if n.Tag != "!!str" {
			return fmt.Errorf("expected string")
		}
	case reflect.Bool:
		if n.Tag != "!!bool" {
			return fmt.Errorf("expected boolean")
		}
	case reflect.Int, reflect.Int64:
		if n.Tag != "!!int" {
			return fmt.Errorf("expected integer")
		}
	case reflect.Float64:
		if n.Tag != "!!float" && n.Tag != "!!int" {
			return fmt.Errorf("expected number")
		}
	}
	return nil
}
func (c Config) Validate() error {
	for _, pool := range []Pool{c.STT, c.TTS} {
		for _, in := range pool.Instances {
			for _, value := range in.Token {
				if value < 33 || value > 126 {
					return fmt.Errorf("instance token must contain only printable ASCII without spaces")
				}
			}
			if strings.HasPrefix(in.Token, "REPLACE_") || strings.ContainsAny(in.Token, "\r\n\t ") || (in.Token != "" && len(in.Token) < 24) || len(in.Token) > 4096 {
				return fmt.Errorf("instance token must be a single bounded header value")
			}
		}
	}
	for _, address := range []string{c.GRPCAddress, c.HTTPAddress} {
		_, port, err := net.SplitHostPort(address)
		if err != nil || port == "" {
			return fmt.Errorf("listen addresses must be host:port")
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid listen port")
		}

	}
	if c.GRPCAddress == c.HTTPAddress {
		return fmt.Errorf("gRPC and HTTP addresses must differ")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("invalid log_level")
	}
	if c.DataDir == "" || c.RequestTimeout.Duration <= 0 || c.ShutdownDrain.Duration <= 0 || c.Retention.Duration <= 0 || c.MaxConnections < 1 || c.MaxConnections > 1024 || c.MaxPayloadBytes < 1 {
		return fmt.Errorf("data_dir and positive limits are required")
	}
	if len(c.Brokers) == 0 || c.STTTopic == "" || c.TTSTopic == "" || c.STTTopic == c.TTSTopic {
		return fmt.Errorf("brokers and distinct queue topics are required")
	}
	for _, b := range c.Brokers {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("empty broker")
		}
	}
	if len(c.Voices) == 0 {
		return fmt.Errorf("voice profiles are required")
	}
	for id, v := range c.Voices {
		if id == "" || v.Identity == "" || math.IsNaN(v.CFG) || math.IsInf(v.CFG, 0) || v.CFG < 0 || v.CFG > 5 || v.Steps < 1 || v.Steps > 50 {
			return fmt.Errorf("invalid voice profile")
		}
	}
	ids := map[string]bool{}
	for op, pool := range map[string]Pool{"stt": c.STT, "tts": c.TTS} {
		if pool.MaxWaiting < 1 || len(pool.Instances) == 0 {
			return fmt.Errorf("%s requires instances and positive max_waiting", op)
		}
		if pool.MinCallBudget.Duration <= 0 || pool.MinCallBudget.Duration >= c.RequestTimeout.Duration {
			return fmt.Errorf("%s min_call_budget must be positive and below request_timeout", op)
		}
		for _, in := range pool.Instances {
			if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(in.ID) || ids[in.ID] || in.Slots < 1 || in.Timeout.Duration <= 0 {
				return fmt.Errorf("invalid or duplicate instance")
			}
			ids[in.ID] = true
			u, err := url.Parse(in.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("instance URL must be HTTP(S) without credentials or fragment")
			}
			if op == "stt" {
				if in.API != "gigaam" || len(in.Profiles) > 0 {
					return fmt.Errorf("invalid STT API or profiles")
				}
			} else {
				if in.API != "voxcpm" || len(in.Profiles) == 0 || in.Model != "" || in.Longform {
					return fmt.Errorf("invalid TTS API or settings")
				}
				seen := map[string]bool{}
				for _, p := range in.Profiles {
					if _, ok := c.Voices[p]; !ok || seen[p] {
						return fmt.Errorf("unknown or duplicate voice profile")
					}
					seen[p] = true
				}
			}
		}
	}
	for name := range c.Voices {
		found := false
		for _, in := range c.TTS.Instances {
			for _, p := range in.Profiles {
				if p == name {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("voice profile has no TTS instance")
		}
	}
	return nil
}
func (c Config) Queues() (map[string]*scheduler.Queue, error) {
	queues := map[string]*scheduler.Queue{}
	for op, pool := range map[string]Pool{"stt": c.STT, "tts": c.TTS} {
		instances := []scheduler.Instance{}
		for _, in := range pool.Instances {
			instances = append(instances, scheduler.Instance{ID: in.ID, Slots: in.Slots, Profiles: in.Profiles})
		}
		q, err := scheduler.New(instances, pool.MaxWaiting)
		if err != nil {
			return nil, err
		}
		q.RequireBudget(pool.MinCallBudget.Duration)
		queues[op] = q
	}
	return queues, nil
}
func (c Config) Provider() *provider.HTTP {
	p := &provider.HTTP{STT: map[string]provider.STT{}, TTS: map[string]provider.TTS{}}
	for _, in := range c.STT.Instances {
		p.STT[in.ID] = provider.STT{Token: in.Token, URL: in.URL, Model: in.Model, Longform: in.Longform, Timeout: in.Timeout.Duration}
	}
	for _, in := range c.TTS.Instances {
		profiles := map[string]provider.Options{}
		for _, id := range in.Profiles {
			profiles[id] = c.Voices[id].Options()
		}
		p.TTS[in.ID] = provider.TTS{Token: in.Token, URL: in.URL, Timeout: in.Timeout.Duration, Profiles: profiles}
	}
	return p
}

// HoldTimeout limits how long an uncertain call keeps the instance's slot. It is
// an estimate: nothing confirms that the model has stopped when it runs out.
func (c Config) HoldTimeout(instance string) time.Duration {
	return c.instance(instance).Timeout.Duration
}
func (c Config) AbortOnDisconnect(instance string) bool {
	return c.instance(instance).AbortOnDisconnect
}

// instance returns the configured instance, or the zero value for an unknown id.
func (c Config) instance(id string) Instance {
	for _, pool := range []Pool{c.STT, c.TTS} {
		for _, in := range pool.Instances {
			if in.ID == id {
				return in
			}
		}
	}
	return Instance{}
}
