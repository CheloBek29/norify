package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/norify/platform/packages/contracts"
	"github.com/norify/platform/packages/go-common/channels"
	"github.com/norify/platform/packages/go-common/httpapi"
	appruntime "github.com/norify/platform/packages/go-common/runtime"
)

var channelState = struct {
	sync.Mutex
	configs map[string]channels.Config
}{configs: map[string]channels.Config{}}

type channelResponse struct {
	Code                string   `json:"code"`
	Name                string   `json:"name"`
	Enabled             bool     `json:"enabled"`
	SuccessProbability  float64  `json:"success_probability"`
	MinDelaySeconds     int      `json:"min_delay_seconds"`
	MaxDelaySeconds     int      `json:"max_delay_seconds"`
	MaxParallelism      int      `json:"max_parallelism"`
	RetryLimit          int      `json:"retry_limit"`
	DeliveryTotal       int      `json:"delivery_total"`
	DeliverySent        int      `json:"delivery_sent"`
	DeliveryFailed      int      `json:"delivery_failed"`
	DeliveryQueued      int      `json:"delivery_queued"`
	DeliveryCancelled   int      `json:"delivery_cancelled"`
	DeliverySuccessRate *float64 `json:"delivery_success_rate"`
	AverageAttempt      *float64 `json:"average_attempt"`
}

var db *pgxpool.Pool

func init() {
	for _, config := range channels.DefaultConfigs() {
		channelState.configs[config.Code] = config
	}
}

func main() {
	ctx := context.Background()
	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("channel-service postgres", err)
	mux := httpapi.NewMux(httpapi.Service{Name: "channel-service", Version: "0.2.0", Ready: func() bool { return db != nil }})
	mux.HandleFunc("/channels", listChannels)
	mux.HandleFunc("/channels/", mutateChannel)
	_ = httpapi.Listen("channel-service", mux)
}

func listChannels(w http.ResponseWriter, r *http.Request) {
	if db != nil {
		out, err := listChannelsFromDB(r.Context())
		if err != nil {
			httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, out)
		return
	}
	channelState.Lock()
	defer channelState.Unlock()
	out := make([]channelResponse, 0, len(channelState.configs))
	for _, config := range channelState.configs {
		out = append(out, responseFromConfig(config))
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func mutateChannel(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/channels/"), "/")
	code := parts[0]
	if db != nil {
		mutateChannelInDB(w, r, code, parts)
		return
	}
	channelState.Lock()
	defer channelState.Unlock()
	config, ok := channelState.configs[code]
	if !ok {
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "channel_not_found"})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		switch parts[1] {
		case "enable":
			config.Enabled = true
		case "disable":
			config.Enabled = false
		}
		channelState.configs[code] = config
		httpapi.WriteJSON(w, http.StatusOK, config)
		return
	}
	if r.Method == http.MethodPatch {
		var patch channels.Config
		if err := httpapi.ReadJSON(r, &patch); err != nil {
			httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
			return
		}
		patch.Code = code
		channelState.configs[code] = patch
		httpapi.WriteJSON(w, http.StatusOK, patch)
		return
	}
	httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
}

func listChannelsFromDB(ctx context.Context) ([]channelResponse, error) {
	rows, err := db.Query(ctx, `
		SELECT
			c.code,
			c.name,
			c.enabled,
			c.success_probability::float8,
			c.min_delay_seconds,
			c.max_delay_seconds,
			c.max_parallelism,
			c.retry_limit,
			count(md.id)::int AS delivery_total,
			count(md.id) FILTER (WHERE md.status = 'sent')::int AS delivery_sent,
			count(md.id) FILTER (WHERE md.status = 'failed')::int AS delivery_failed,
			count(md.id) FILTER (WHERE md.status = 'queued')::int AS delivery_queued,
			count(md.id) FILTER (WHERE md.status = 'cancelled')::int AS delivery_cancelled,
			CASE WHEN count(md.id) FILTER (WHERE md.status IN ('sent', 'failed', 'cancelled')) > 0
			     THEN (count(md.id) FILTER (WHERE md.status = 'sent'))::float8 / (count(md.id) FILTER (WHERE md.status IN ('sent', 'failed', 'cancelled')))::float8
			     ELSE NULL END AS delivery_success_rate,
			avg(md.attempt)::float8 AS average_attempt
		FROM channels c
		LEFT JOIN message_deliveries md ON md.channel_code = c.code
		GROUP BY c.code, c.name, c.enabled, c.success_probability, c.min_delay_seconds, c.max_delay_seconds, c.max_parallelism, c.retry_limit
		ORDER BY c.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []channelResponse{}
	for rows.Next() {
		var item channelResponse
		if err := rows.Scan(
			&item.Code,
			&item.Name,
			&item.Enabled,
			&item.SuccessProbability,
			&item.MinDelaySeconds,
			&item.MaxDelaySeconds,
			&item.MaxParallelism,
			&item.RetryLimit,
			&item.DeliveryTotal,
			&item.DeliverySent,
			&item.DeliveryFailed,
			&item.DeliveryQueued,
			&item.DeliveryCancelled,
			&item.DeliverySuccessRate,
			&item.AverageAttempt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func mutateChannelInDB(w http.ResponseWriter, r *http.Request, code string, parts []string) {
	if len(parts) == 2 && r.Method == http.MethodPost {
		switch parts[1] {
		case "enable":
			_, _ = db.Exec(r.Context(), `UPDATE channels SET enabled = true, updated_at = now() WHERE code = $1`, code)
		case "disable":
			_, _ = db.Exec(r.Context(), `UPDATE channels SET enabled = false, updated_at = now() WHERE code = $1`, code)
		default:
			httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_action"})
			return
		}
		writeChannelByCode(w, r, code)
		return
	}
	if r.Method == http.MethodPatch {
		var patch channelResponse
		if err := httpapi.ReadJSON(r, &patch); err != nil {
			httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
			return
		}
		_, err := db.Exec(r.Context(), `
			UPDATE channels
			SET enabled = $2,
			    success_probability = $3,
			    min_delay_seconds = $4,
			    max_delay_seconds = $5,
			    max_parallelism = $6,
			    retry_limit = $7,
			    updated_at = now()
			WHERE code = $1`,
			code, patch.Enabled, patch.SuccessProbability, patch.MinDelaySeconds, patch.MaxDelaySeconds, patch.MaxParallelism, patch.RetryLimit)
		if err != nil {
			httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeChannelByCode(w, r, code)
		return
	}
	httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
}

func writeChannelByCode(w http.ResponseWriter, r *http.Request, code string) {
	items, err := listChannelsFromDB(r.Context())
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, item := range items {
		if item.Code == code {
			cacheWorkerChannelConfig(r.Context(), item)
			httpapi.WriteJSON(w, http.StatusOK, item)
			return
		}
	}
	httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "channel_not_found"})
}

func cacheWorkerChannelConfig(ctx context.Context, item channelResponse) {
	client, err := appruntime.NewRedisClientFromEnv()
	if err != nil {
		return
	}
	key := "channel-config:" + item.Code
	if !item.Enabled {
		_ = client.Del(ctx, key)
		return
	}
	body, err := json.Marshal(workerCachePayloadFromChannel(item))
	if err != nil {
		return
	}
	_ = client.SetEX(ctx, key, channelConfigRedisTTL(), string(body))
}

func workerCachePayloadFromChannel(item channelResponse) contracts.WorkerChannelConfig {
	return contracts.WorkerChannelConfig{
		Code:               item.Code,
		Enabled:            item.Enabled,
		SuccessProbability: item.SuccessProbability,
		MinDelaySeconds:    item.MinDelaySeconds,
		MaxDelaySeconds:    item.MaxDelaySeconds,
		MaxParallelism:     item.MaxParallelism,
		RetryLimit:         item.RetryLimit,
		Source:             "channel-service",
	}
}

func channelConfigRedisTTL() time.Duration {
	seconds := appruntime.EnvInt("CHANNEL_CONFIG_CACHE_TTL_SECONDS", 60)
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func responseFromConfig(config channels.Config) channelResponse {
	return channelResponse{
		Code:               config.Code,
		Name:               config.Code,
		Enabled:            config.Enabled,
		SuccessProbability: config.SuccessProbability,
		MinDelaySeconds:    int(config.MinDelay / time.Second),
		MaxDelaySeconds:    int(config.MaxDelay / time.Second),
		MaxParallelism:     config.MaxParallelism,
		RetryLimit:         config.RetryLimit,
	}
}
