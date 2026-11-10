package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

type Config struct {
	LogLevel                 string              `json:"log_level"`
	ListenAddress            string              `json:"listen_address"`
	GRPCListenAddress        string              `json:"grpc_listen_address"`
	GRPCMaxReceiveBytes      int                 `json:"grpc_max_receive_bytes"`
	GRPCMaxSendBytes         int                 `json:"grpc_max_send_bytes"`
	GRPCMaxConcurrentStreams int                 `json:"grpc_max_concurrent_streams"`
	KafkaBrokers             []string            `json:"kafka_brokers"`
	RequestsTopic            string              `json:"requests_topic"`
	StateTopic               string              `json:"state_topic"`
	ConsumerGroup            string              `json:"consumer_group"`
	TransactionalID          string              `json:"transactional_id"`
	MaxBodyBytes             int64               `json:"max_body_bytes"`
	MaxPending               int                 `json:"max_pending"`
	MaxPendingBytes          int64               `json:"max_pending_bytes"`
	MaxConnections           int                 `json:"max_connections"`
	RequestDeadline          time.Duration       `json:"request_deadline"`
	UpstreamTimeout          time.Duration       `json:"upstream_timeout"`
	Retention                time.Duration       `json:"retention"`
	MaxInputLag              time.Duration       `json:"max_input_lag"`
	GracePeriod              time.Duration       `json:"grace_period"`
	Pools                    map[string][]string `json:"pools"`
	Instances                []InstanceConfig    `json:"instances"`
}

type InstanceConfig struct {
	ID                       string        `json:"id"`
	BaseURL                  string        `json:"base_url"`
	Model                    string        `json:"model"`
	Token                    string        `json:"token"`
	MaxInFlight              int           `json:"max_in_flight"`
	SupportedParameters      []string      `json:"supported_parameters"`
	SupportedResponseFormats []string      `json:"supported_response_formats"`
	ContextWindow            int           `json:"context_window"`
	Timeout                  time.Duration `json:"timeout"`
	// AbortOnDisconnect allows releasing an uncertain slot once the upstream
	// HTTP connection is closed, by either side. Set only after verifying this
	// endpoint's behavior.
	AbortOnDisconnect bool `json:"abort_on_disconnect"`
	// ServerExecutionLimit is a verified upstream bound, including its queue.
	// Zero leaves uncertain slots reserved until completion is confirmed.
	ServerExecutionLimit time.Duration `json:"server_execution_limit"`
	// ReasoningEffortBody adds upstream request fields for a requested
	// reasoning_effort, for a model that honours another knob: Qwen3 turns its
	// thinking off with chat_template_kwargs.enable_thinking, not the effort.
	ReasoningEffortBody map[string]map[string]json.RawMessage `json:"reasoning_effort_body"`
}

func (i *InstanceConfig) UnmarshalJSON(data []byte) error {
	type plain InstanceConfig
	var raw struct {
		*plain
		Timeout              string `json:"timeout"`
		ServerExecutionLimit string `json:"server_execution_limit"`
	}
	raw.plain = (*plain)(i)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, value := range []struct {
		name, raw string
		out       *time.Duration
	}{
		{"timeout", raw.Timeout, &i.Timeout},
		{"server_execution_limit", raw.ServerExecutionLimit, &i.ServerExecutionLimit},
	} {
		if value.raw != "" {
			d, err := time.ParseDuration(value.raw)
			if err != nil {
				return fmt.Errorf("%s: %w", value.name, err)
			}
			*value.out = d
		}
	}

	return nil
}

// ModelRules describe upstream behavior by served model. Instance-specific
// values override model defaults at the same reasoning-effort level.
type ModelRules struct {
	Models map[string]struct {
		ReasoningEffortBody map[string]map[string]json.RawMessage `json:"reasoning_effort_body"`
	} `json:"models"`
}

func (c *Config) applyModelRules(rules ModelRules) {
	for i := range c.Instances {
		in := &c.Instances[i]
		for level, body := range rules.Models[in.Model].ReasoningEffortBody {
			if _, set := in.ReasoningEffortBody[level]; set {
				continue
			}
			if in.ReasoningEffortBody == nil {
				in.ReasoningEffortBody = map[string]map[string]json.RawMessage{}
			}
			in.ReasoningEffortBody[level] = body
		}
	}
}

func LoadConfig(path string) (Config, error) { return loadYAMLConfig(path) }

func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var raw struct {
		*plain
		RequestDeadline string `json:"request_deadline"`
		UpstreamTimeout string `json:"upstream_timeout"`
		Retention       string `json:"retention"`
		MaxInputLag     string `json:"max_input_lag"`
		GracePeriod     string `json:"grace_period"`
	}
	raw.plain = (*plain)(c)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, value := range []struct {
		name, raw string
		out       *time.Duration
	}{
		{"request_deadline", raw.RequestDeadline, &c.RequestDeadline}, {"upstream_timeout", raw.UpstreamTimeout, &c.UpstreamTimeout},
		{"retention", raw.Retention, &c.Retention}, {"max_input_lag", raw.MaxInputLag, &c.MaxInputLag}, {"grace_period", raw.GracePeriod, &c.GracePeriod},
	} {
		if value.raw != "" {
			parsed, err := time.ParseDuration(value.raw)
			if err != nil {
				return fmt.Errorf("%s: %w", value.name, err)
			}
			*value.out = parsed
		}
	}
	return nil
}

func (c *Config) defaults() {
	if c.GRPCListenAddress == "" {
		c.GRPCListenAddress = ":8011"
	}
	if c.GRPCMaxReceiveBytes == 0 {
		c.GRPCMaxReceiveBytes = 2 << 20
	}
	if c.GRPCMaxSendBytes == 0 {
		c.GRPCMaxSendBytes = 40 << 20
	}
	if c.GRPCMaxConcurrentStreams == 0 {
		c.GRPCMaxConcurrentStreams = 128
	}
	if c.ListenAddress == "" {
		c.ListenAddress = ":8010"
	}
	if c.RequestsTopic == "" {
		c.RequestsTopic = "llm.requests.v1"
	}
	if c.StateTopic == "" {
		c.StateTopic = "llm.state.v1"
	}
	if c.ConsumerGroup == "" {
		c.ConsumerGroup = "llm-gateway-v1"
	}
	if c.TransactionalID == "" {
		c.TransactionalID = "llm-gateway-v1"
	}
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = 1 << 20
	}
	if c.MaxPending == 0 {
		c.MaxPending = 1000
	}
	if c.MaxPendingBytes == 0 {
		c.MaxPendingBytes = 256 << 20
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 1024
	}
	if c.RequestDeadline == 0 {
		c.RequestDeadline = 180 * time.Second
	}
	if c.UpstreamTimeout == 0 {
		c.UpstreamTimeout = 120 * time.Second
	}
	if c.Retention == 0 {
		c.Retention = 24 * time.Hour
	}
	if c.MaxInputLag == 0 {
		c.MaxInputLag = 24 * time.Hour
	}
	if c.GracePeriod == 0 {
		c.GracePeriod = 30 * time.Second
	}
	for i := range c.Instances {
		if c.Instances[i].Timeout == 0 {
			c.Instances[i].Timeout = c.UpstreamTimeout
		}
	}
}

func (c Config) Validate() error {
	if c.LogLevel != "" {
		if _, err := ParseLogLevel(c.LogLevel); err != nil {
			return err
		}
	}
	if c.GRPCListenAddress == c.ListenAddress || c.GRPCMaxReceiveBytes <= 0 || c.GRPCMaxSendBytes <= 0 || c.GRPCMaxConcurrentStreams <= 0 {
		return errors.New("invalid gRPC listener or limits")
	}
	if len(c.KafkaBrokers) == 0 {
		return errors.New("kafka_brokers must not be empty")
	}
	if c.RequestDeadline <= 0 || c.UpstreamTimeout <= 0 || c.UpstreamTimeout > 120*time.Second || c.Retention <= 0 || c.MaxInputLag <= 0 || c.GracePeriod <= 0 {
		return errors.New("request_deadline and upstream_timeout must be positive; upstream_timeout cannot exceed 120s")
	}
	if c.MaxBodyBytes <= 0 || c.MaxPending <= 0 || c.MaxPendingBytes <= 0 || c.MaxConnections <= 0 {
		return errors.New("gateway limits must be positive")
	}
	instances := make(map[string]InstanceConfig, len(c.Instances))
	for _, in := range c.Instances {
		if in.ServerExecutionLimit < 0 {
			return fmt.Errorf("instance %q: server_execution_limit must not be negative", in.ID)
		}
		if !validInstanceID(in.ID) || in.Model == "" || in.MaxInFlight < 1 || in.ContextWindow < 1 {
			return fmt.Errorf("instance %q has invalid id, model, max_in_flight, or context_window", in.ID)
		}
		u, err := url.Parse(in.BaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("instance %q has invalid base_url", in.ID)
		}
		if _, exists := instances[in.ID]; exists {
			return fmt.Errorf("duplicate instance id %q", in.ID)
		}
		for level, body := range in.ReasoningEffortBody {
			if level != "low" && level != "medium" && level != "high" {
				return fmt.Errorf("instance %q: reasoning_effort_body.%s is not low, medium, or high", in.ID, level)
			}
			for field := range body {
				// The extra fields never replace what the request decides, nor
				// carry the gateway's own fields, which never go upstream.
				if reservedRequestFields[field] || allowedParameters[field] {
					return fmt.Errorf("instance %q: reasoning_effort_body.%s cannot set %q", in.ID, level, field)
				}
			}
		}
		instances[in.ID] = in
	}
	for _, level := range []string{"low", "medium", "high"} {
		pool := c.Pools[level]
		if len(pool) == 0 {
			return fmt.Errorf("pools.%s must not be empty", level)
		}
		for _, id := range pool {
			if _, ok := instances[id]; !ok {
				return fmt.Errorf("pools.%s references unknown instance %q", level, id)
			}
		}
	}
	return nil
}

// reservedRequestFields are the request fields the gateway owns: the routing
// metadata it strips and the ones it sets itself for the upstream.
var reservedRequestFields = map[string]bool{"model": true, "messages": true, "stream": true, "complexity": true, "urgent": true}

func validInstanceID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
