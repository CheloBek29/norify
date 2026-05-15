package channels

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"time"
)

const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

type Config struct {
	Code               string
	Enabled            bool
	SuccessProbability float64
	MinDelay           time.Duration
	MaxDelay           time.Duration
	MaxParallelism     int
	RetryLimit         int
}

type Message struct {
	RecipientID string
	Body        string
}

type Result struct {
	ChannelCode string
	Status      string
	ErrorCode   string
	Error       string
	FinishedAt  time.Time
}

type Adapter interface {
	Send(context.Context, Message) Result
}

type Registry struct {
	adapters map[string]Adapter
	configs  map[string]Config
}

func NewRegistry(configs []Config) Registry {
	registry := Registry{adapters: map[string]Adapter{}, configs: map[string]Config{}}
	for _, config := range configs {
		registry.configs[config.Code] = normalize(config)
		registry.adapters[config.Code] = StubAdapter{Config: registry.configs[config.Code]}
	}
	return registry
}

func DefaultConfigs() []Config {
	codes := []string{"email", "sms", "telegram", "whatsapp", "vk", "max", "custom_app"}
	configs := make([]Config, 0, len(codes))
	for _, code := range codes {
		configs = append(configs, Config{
			Code:               code,
			Enabled:            true,
			SuccessProbability: 0.92,
			MinDelay:           2 * time.Second,
			MaxDelay:           300 * time.Second,
			MaxParallelism:     100,
			RetryLimit:         3,
		})
	}
	return configs
}

func (r Registry) EnabledCodes() []string {
	codes := make([]string, 0, len(r.configs))
	for code, config := range r.configs {
		if config.Enabled {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}

func (r Registry) Adapter(code string) Adapter {
	return r.adapters[code]
}

type StubAdapter struct {
	Config Config
}

func (a StubAdapter) Send(ctx context.Context, message Message) Result {
	config := normalize(a.Config)
	delay := config.MinDelay
	if config.MaxDelay > config.MinDelay {
		spread := config.MaxDelay - config.MinDelay
		delay += time.Duration(hash(message.RecipientID+config.Code) % uint32(spread+1))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Result{ChannelCode: config.Code, Status: StatusFailed, ErrorCode: "context_cancelled", Error: ctx.Err().Error(), FinishedAt: time.Now().UTC()}
	case <-timer.C:
	}

	if config.SuccessProbability >= 1 || float64(hash(message.RecipientID+message.Body+config.Code)%10000)/10000 < config.SuccessProbability {
		return Result{ChannelCode: config.Code, Status: StatusSuccess, FinishedAt: time.Now().UTC()}
	}
	return Result{ChannelCode: config.Code, Status: StatusFailed, ErrorCode: "stub_delivery_failed", Error: fmt.Sprintf("%s stub failed", config.Code), FinishedAt: time.Now().UTC()}
}

func normalize(config Config) Config {
	if config.MinDelay <= 0 {
		config.MinDelay = 2 * time.Second
	}
	if config.MaxDelay < config.MinDelay {
		config.MaxDelay = config.MinDelay
	}
	if config.MaxParallelism <= 0 {
		config.MaxParallelism = 1
	}
	if config.RetryLimit <= 0 {
		config.RetryLimit = 3
	}
	return config
}

func hash(value string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return h.Sum32()
}
