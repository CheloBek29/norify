package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/norify/platform/packages/go-common/httpapi"
	appruntime "github.com/norify/platform/packages/go-common/runtime"
	amqp "github.com/rabbitmq/amqp091-go"
)

type statsSnapshot struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Source      string          `json:"source"`
	Totals      totalsSnapshot  `json:"totals"`
	Channels    []channelStat   `json:"channels"`
	Realtime    []realtimePoint `json:"realtime"`
}

type totalsSnapshot struct {
	Messages      int      `json:"messages"`
	Processed     int      `json:"processed"`
	Success       int      `json:"success"`
	Failed        int      `json:"failed"`
	Cancelled     int      `json:"cancelled"`
	Pending       int      `json:"pending"`
	Active        int      `json:"active"`
	QueueDepth    int      `json:"queue_depth"`
	SuccessRate   *float64 `json:"success_rate"`
	FailedRate    *float64 `json:"failed_rate"`
	P95DispatchMs int      `json:"p95_dispatch_ms"`
}

type channelStat struct {
	Code             string   `json:"code"`
	Total            int      `json:"total"`
	Sent             int      `json:"sent"`
	Failed           int      `json:"failed"`
	Queued           int      `json:"queued"`
	Cancelled        int      `json:"cancelled"`
	SuccessRate      *float64 `json:"success_rate"`
	FailureRate      *float64 `json:"failure_rate"`
	AverageAttempt   *float64 `json:"average_attempt"`
	AverageLatencyMs *float64 `json:"average_latency_ms"`
}

type realtimePoint struct {
	Bucket string `json:"bucket"`
	Sent   int    `json:"sent"`
	Failed int    `json:"failed"`
}

type campaignAggregateRow struct {
	TotalMessages int
	Processed     int
	Success       int
	Failed        int
	Cancelled     int
	Active        int
	P95DispatchMs int
}

type channelAggregateRow struct {
	Code             string
	Total            int
	Sent             int
	Failed           int
	Queued           int
	Cancelled        int
	AverageAttempt   *float64
	AverageLatencyMs *float64
}

type realtimeAggregateRow struct {
	Bucket string
	Sent   int
	Failed int
}

type snapshotLoader func(context.Context) (statsSnapshot, error)

type statsHub struct {
	load        snapshotLoader
	notify      chan struct{}
	mu          sync.Mutex
	latest      statsSnapshot
	hasLatest   bool
	subscribers map[chan statsSnapshot]struct{}
}

var db *pgxpool.Pool

func main() {
	ctx := context.Background()
	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("stats-service postgres", err)

	hub := newStatsHub(func(ctx context.Context) (statsSnapshot, error) {
		if db == nil {
			return statsSnapshot{}, errors.New("postgres_unavailable")
		}
		return loadSnapshot(ctx, db)
	})
	go hub.run(ctx)
	go consumeStatusEvents(ctx, hub)

	mux := httpapi.NewMux(httpapi.Service{Name: "stats-service", Version: "0.1.0", Ready: func() bool { return db != nil }})
	mux.HandleFunc("/stats/overview", hub.handleOverview)
	mux.HandleFunc("/stats/stream", hub.handleStream)
	_ = httpapi.Listen("stats-service", mux)
}

func newStatsHub(load snapshotLoader) *statsHub {
	return &statsHub{
		load:        load,
		notify:      make(chan struct{}, 1),
		subscribers: map[chan statsSnapshot]struct{}{},
	}
}

func (h *statsHub) run(ctx context.Context) {
	h.refresh(ctx)
	ticker := time.NewTicker(time.Duration(appruntime.EnvInt("STATS_REFRESH_SECONDS", 5)) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.refresh(ctx)
		case <-h.notify:
			h.refresh(ctx)
		}
	}
}

func (h *statsHub) refresh(ctx context.Context) {
	snapshot, err := h.load(ctx)
	if err != nil {
		slog.Warn("stats snapshot unavailable", "error", err)
		return
	}
	h.mu.Lock()
	h.latest = snapshot
	h.hasLatest = true
	for subscriber := range h.subscribers {
		select {
		case subscriber <- snapshot:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *statsHub) signal() {
	select {
	case h.notify <- struct{}{}:
	default:
	}
}

func (h *statsHub) current(ctx context.Context) (statsSnapshot, error) {
	h.mu.Lock()
	if h.hasLatest {
		snapshot := h.latest
		h.mu.Unlock()
		return snapshot, nil
	}
	h.mu.Unlock()
	return h.load(ctx)
}

func (h *statsHub) subscribe() (chan statsSnapshot, func()) {
	ch := make(chan statsSnapshot, 2)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	if h.hasLatest {
		ch <- h.latest
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers, ch)
		close(ch)
		h.mu.Unlock()
	}
}

func (h *statsHub) handleOverview(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.current(r.Context())
	if err != nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, snapshot)
}

func (h *statsHub) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming_unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsubscribe := h.subscribe()
	defer unsubscribe()
	if snapshot, err := h.current(r.Context()); err == nil {
		writeSSE(w, "snapshot", snapshot)
		flusher.Flush()
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case snapshot := <-ch:
			writeSSE(w, "snapshot", snapshot)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}

func loadSnapshot(ctx context.Context, pool *pgxpool.Pool) (statsSnapshot, error) {
	campaigns, err := loadCampaignAggregates(ctx, pool)
	if err != nil {
		return statsSnapshot{}, err
	}
	channels, err := loadChannelAggregates(ctx, pool)
	if err != nil {
		return statsSnapshot{}, err
	}
	realtime, err := loadRealtimeAggregates(ctx, pool)
	if err != nil {
		return statsSnapshot{}, err
	}
	queueDepth := appruntime.QueueDepth(appruntime.QueueMessageSend)
	return buildSnapshot(campaigns, channels, realtime, queueDepth), nil
}

func loadCampaignAggregates(ctx context.Context, pool *pgxpool.Pool) ([]campaignAggregateRow, error) {
	var row campaignAggregateRow
	err := pool.QueryRow(ctx, `
		SELECT
			COALESCE(sum(total_messages), 0)::int,
			COALESCE(sum(GREATEST(sent_count, success_count + failed_count + cancelled_count)), 0)::int,
			COALESCE(sum(success_count), 0)::int,
			COALESCE(sum(failed_count), 0)::int,
			COALESCE(sum(cancelled_count), 0)::int,
			COALESCE(count(*) FILTER (WHERE status IN ('running', 'retrying')), 0)::int,
			COALESCE(max(p95_dispatch_ms), 0)::int
		FROM campaigns
		WHERE archived_at IS NULL`).Scan(
		&row.TotalMessages,
		&row.Processed,
		&row.Success,
		&row.Failed,
		&row.Cancelled,
		&row.Active,
		&row.P95DispatchMs,
	)
	if err != nil {
		return nil, err
	}
	return []campaignAggregateRow{row}, nil
}

func loadChannelAggregates(ctx context.Context, pool *pgxpool.Pool) ([]channelAggregateRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			c.code,
			count(md.id)::int AS total,
			count(md.id) FILTER (WHERE md.status = 'sent')::int AS sent,
			count(md.id) FILTER (WHERE md.status = 'failed')::int AS failed,
			count(md.id) FILTER (WHERE md.status = 'queued')::int AS queued,
			count(md.id) FILTER (WHERE md.status = 'cancelled')::int AS cancelled,
			avg(md.attempt)::float8 AS average_attempt,
			(avg(EXTRACT(EPOCH FROM (md.finished_at - md.queued_at)) * 1000)
				FILTER (WHERE md.finished_at IS NOT NULL AND md.queued_at IS NOT NULL))::float8 AS average_latency_ms
		FROM channels c
		LEFT JOIN message_deliveries md ON md.channel_code = c.code
		GROUP BY c.code
		ORDER BY c.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []channelAggregateRow{}
	for rows.Next() {
		var item channelAggregateRow
		var averageAttempt sql.NullFloat64
		var averageLatencyMs sql.NullFloat64
		if err := rows.Scan(
			&item.Code,
			&item.Total,
			&item.Sent,
			&item.Failed,
			&item.Queued,
			&item.Cancelled,
			&averageAttempt,
			&averageLatencyMs,
		); err != nil {
			return nil, err
		}
		if averageAttempt.Valid {
			item.AverageAttempt = &averageAttempt.Float64
		}
		if averageLatencyMs.Valid {
			item.AverageLatencyMs = &averageLatencyMs.Float64
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadRealtimeAggregates(ctx context.Context, pool *pgxpool.Pool) ([]realtimeAggregateRow, error) {
	rows, err := pool.Query(ctx, `
		WITH buckets AS (
			SELECT generate_series(
				date_trunc('minute', now()) - interval '15 minutes',
				date_trunc('minute', now()),
				interval '1 minute'
			) AS bucket
		)
		SELECT
			to_char(b.bucket, 'HH24:MI') AS bucket,
			count(md.id) FILTER (WHERE md.status = 'sent')::int AS sent,
			count(md.id) FILTER (WHERE md.status = 'failed')::int AS failed
		FROM buckets b
		LEFT JOIN message_deliveries md
			ON date_trunc('minute', md.finished_at) = b.bucket
			AND md.status IN ('sent', 'failed')
		GROUP BY b.bucket
		ORDER BY b.bucket`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []realtimeAggregateRow{}
	for rows.Next() {
		var item realtimeAggregateRow
		if err := rows.Scan(&item.Bucket, &item.Sent, &item.Failed); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func buildSnapshot(campaigns []campaignAggregateRow, channels []channelAggregateRow, realtime []realtimeAggregateRow, queueDepth int) statsSnapshot {
	totals := totalsSnapshot{QueueDepth: queueDepth}
	for _, row := range campaigns {
		totals.Messages += row.TotalMessages
		totals.Processed += row.Processed
		totals.Success += row.Success
		totals.Failed += row.Failed
		totals.Cancelled += row.Cancelled
		totals.Active += row.Active
		if row.P95DispatchMs > totals.P95DispatchMs {
			totals.P95DispatchMs = row.P95DispatchMs
		}
	}
	totals.Pending = max(0, totals.Messages-totals.Processed)
	resolved := totals.Success + totals.Failed + totals.Cancelled
	totals.SuccessRate = ratioPtr(totals.Success, resolved)
	totals.FailedRate = ratioPtr(totals.Failed, resolved)

	channelStats := make([]channelStat, 0, len(channels))
	for _, row := range channels {
		resolved := row.Sent + row.Failed + row.Cancelled
		channelStats = append(channelStats, channelStat{
			Code:             row.Code,
			Total:            row.Total,
			Sent:             row.Sent,
			Failed:           row.Failed,
			Queued:           row.Queued,
			Cancelled:        row.Cancelled,
			SuccessRate:      ratioPtr(row.Sent, resolved),
			FailureRate:      ratioPtr(row.Failed, resolved),
			AverageAttempt:   row.AverageAttempt,
			AverageLatencyMs: row.AverageLatencyMs,
		})
	}

	points := make([]realtimePoint, 0, len(realtime))
	for _, row := range realtime {
		points = append(points, realtimePoint{Bucket: row.Bucket, Sent: row.Sent, Failed: row.Failed})
	}
	return statsSnapshot{
		GeneratedAt: time.Now().UTC(),
		Source:      "postgres",
		Totals:      totals,
		Channels:    channelStats,
		Realtime:    points,
	}
}

func ratioPtr(numerator, denominator int) *float64 {
	if denominator <= 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func consumeStatusEvents(ctx context.Context, hub *statsHub) {
	appruntime.RunWithReconnect(ctx, "stats-service-events", func(ctx context.Context, ch *amqp.Channel) error {
		queue, err := ch.QueueDeclare("", false, true, true, false, nil)
		if err != nil {
			return err
		}
		for _, exchange := range []string{appruntime.ExchangeMessageStatus, appruntime.ExchangeCampaignStatus} {
			if err := ch.QueueBind(queue.Name, "#", exchange, false, nil); err != nil {
				return err
			}
		}
		deliveries, err := ch.Consume(queue.Name, "", true, true, false, false, nil)
		if err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case _, ok := <-deliveries:
				if !ok {
					return pgx.ErrNoRows
				}
				hub.signal()
			}
		}
	})
}
