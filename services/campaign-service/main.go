package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/norify/platform/packages/contracts"
	"github.com/norify/platform/packages/go-common/campaigns"
	"github.com/norify/platform/packages/go-common/httpapi"
	appruntime "github.com/norify/platform/packages/go-common/runtime"
	amqp "github.com/rabbitmq/amqp091-go"
)

type Campaign struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	TemplateID       string             `json:"template_id"`
	TemplateName     string             `json:"template_name"`
	Status           string             `json:"status"`
	Filters          json.RawMessage    `json:"filters"`
	SelectedChannels []string           `json:"selected_channels"`
	TotalRecipients  int                `json:"total_recipients"`
	TotalMessages    int                `json:"total_messages"`
	SentCount        int                `json:"sent_count"`
	SuccessCount     int                `json:"success_count"`
	FailedCount      int                `json:"failed_count"`
	CancelledCount   int                `json:"cancelled_count"`
	P95DispatchMs    int                `json:"p95_dispatch_ms"`
	CreatedAt        time.Time          `json:"created_at"`
	StartedAt        *time.Time         `json:"started_at,omitempty"`
	FinishedAt       *time.Time         `json:"finished_at,omitempty"`
	Snapshot         campaigns.Snapshot `json:"snapshot"`
}

type createCampaignRequest struct {
	Name             string          `json:"name"`
	TemplateID       string          `json:"template_id"`
	Filters          json.RawMessage `json:"filters"`
	SelectedChannels []string        `json:"selected_channels"`
	TotalRecipients  int             `json:"total_recipients"`
}

type switchChannelRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type errorGroupActionRequest struct {
	ToChannel string `json:"to_channel"`
}

var db *pgxpool.Pool
var mq *amqp.Channel

func main() {
	ctx := context.Background()
	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("campaign-service postgres", err)
	if db != nil {
		appruntime.LogStartup("campaign-service schema", ensureSchema(ctx))
	}
	if conn, channel, qErr := appruntime.OpenRabbit(); qErr == nil {
		defer conn.Close()
		defer channel.Close()
		mq = channel
	} else {
		appruntime.LogStartup("campaign-service rabbitmq", qErr)
	}

	mux := httpapi.NewMux(httpapi.Service{Name: "campaign-service", Version: "0.2.0", Ready: func() bool { return db != nil && mq != nil }})
	mux.HandleFunc("/campaigns", campaignsCollection)
	mux.HandleFunc("/campaigns/", campaignAction)
	_ = httpapi.Listen("campaign-service", mux)
}

func campaignsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listCampaigns(w, r)
	case http.MethodPost:
		createCampaign(w, r)
	default:
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func listCampaigns(w http.ResponseWriter, r *http.Request) {
	if db == nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "postgres_unavailable"})
		return
	}
	rows, err := db.Query(r.Context(), `
		SELECT c.id, c.name, c.template_id, COALESCE(t.name, ''), c.status, c.filters, c.selected_channels,
		       c.total_recipients, c.total_messages, c.sent_count, c.success_count, c.failed_count, c.cancelled_count,
		       c.p95_dispatch_ms, c.created_at, c.started_at, c.finished_at
		FROM campaigns c
		LEFT JOIN templates t ON t.id = c.template_id
		ORDER BY c.created_at DESC
		LIMIT 100`)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		campaign, err := scanCampaign(rows)
		if err != nil {
			httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out = append(out, campaign)
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func createCampaign(w http.ResponseWriter, r *http.Request) {
	if db == nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "postgres_unavailable"})
		return
	}
	var req createCampaignRequest
	if err := httpapi.ReadJSON(r, &req); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if req.Name == "" || req.TemplateID == "" || len(req.SelectedChannels) == 0 {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "name_template_and_channels_required"})
		return
	}
	if req.TotalRecipients <= 0 {
		count, err := countAudience(r.Context(), req.Filters)
		if err != nil {
			httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		req.TotalRecipients = count
	}
	if len(req.Filters) == 0 {
		req.Filters = json.RawMessage(`{}`)
	}
	id := newID("cmp")
	selected, _ := json.Marshal(req.SelectedChannels)
	_, err := db.Exec(r.Context(), `
		INSERT INTO campaigns (id, name, template_id, status, filters, selected_channels, total_recipients, total_messages)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, req.Name, req.TemplateID, campaigns.StatusCreated, req.Filters, selected, req.TotalRecipients, campaigns.TotalMessages(req.TotalRecipients, req.SelectedChannels))
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, campaign)
}

func campaignAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/campaigns/"), "/")
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		campaign, err := getCampaign(r.Context(), id)
		if err != nil {
			writeLookupError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, campaign)
		return
	}
	if len(parts) == 4 && parts[1] == "error-groups" {
		handleErrorGroupAction(w, r, id, parts[2], parts[3])
		return
	}
	if len(parts) != 2 {
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	switch parts[1] {
	case "start":
		startCampaign(w, r, id)
	case "stop":
		stopCampaign(w, r, id)
	case "cancel":
		cancelCampaign(w, r, id)
	case "retry-failed":
		retryFailed(w, r, id)
	case "switch-channel":
		switchChannel(w, r, id)
	case "stats":
		stats(w, r, id)
	case "deliveries":
		deliveries(w, r, id)
	case "error-groups":
		errorGroups(w, r, id)
	case "dispatch-metrics":
		dispatchMetrics(w, r, id)
	default:
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_action"})
	}
}

func dispatchMetrics(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	var metrics contracts.CampaignDispatchMetrics
	if err := httpapi.ReadJSON(r, &metrics); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if metrics.P95DispatchMs <= 0 {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "p95_dispatch_ms_required"})
		return
	}
	_, err := db.Exec(r.Context(), `
		UPDATE campaigns
		SET p95_dispatch_ms = $2, updated_at = now()
		WHERE id = $1`, id, metrics.P95DispatchMs)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func startCampaign(w http.ResponseWriter, r *http.Request, id string) {
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	now := time.Now().UTC()
	_, err = db.Exec(r.Context(), `UPDATE campaigns SET status = $2, started_at = COALESCE(started_at, $3), updated_at = now() WHERE id = $1`, id, campaigns.StatusRunning, now)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := publishDispatch(r.Context(), campaign); err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	campaign, _ = getCampaign(r.Context(), id)
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func stopCampaign(w http.ResponseWriter, r *http.Request, id string) {
	_, err := db.Exec(r.Context(), `
		UPDATE campaigns
		SET status = $2,
		    updated_at = now()
		WHERE id = $1
		  AND status IN ($3, $4)`, id, campaigns.StatusStopped, campaigns.StatusRunning, campaigns.StatusRetrying)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func cancelCampaign(w http.ResponseWriter, r *http.Request, id string) {
	_, err := db.Exec(r.Context(), `
		UPDATE campaigns
		SET status = $2,
		    cancelled_count = GREATEST(total_messages - sent_count, 0),
		    sent_count = total_messages,
		    finished_at = now(),
		    updated_at = now()
		WHERE id = $1`, id, campaigns.StatusCancelled)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func retryFailed(w http.ResponseWriter, r *http.Request, id string) {
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	retryCount := campaign.FailedCount
	if retryCount <= 0 {
		httpapi.WriteJSON(w, http.StatusOK, campaign)
		return
	}
	_, err = db.Exec(r.Context(), `
		UPDATE campaigns
		SET status = $2, total_messages = total_messages + failed_count, failed_count = 0, updated_at = now()
		WHERE id = $1`, id, campaigns.StatusRetrying)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	campaign.TotalRecipients = retryCount
	campaign.TotalMessages = retryCount
	campaign.FailedCount = 0
	if err := publishDispatch(r.Context(), campaign); err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	updated, _ := getCampaign(r.Context(), id)
	httpapi.WriteJSON(w, http.StatusOK, updated)
}

func switchChannel(w http.ResponseWriter, r *http.Request, id string) {
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	var req switchChannelRequest
	_ = httpapi.ReadJSON(r, &req)
	if req.To == "" {
		req.To = "email"
	}
	next := make([]string, 0, len(campaign.SelectedChannels))
	replaced := false
	for _, channel := range campaign.SelectedChannels {
		if channel == req.From || channel == "telegram" {
			next = append(next, req.To)
			replaced = true
			continue
		}
		next = append(next, channel)
	}
	if !replaced {
		next = append(next, req.To)
	}
	selected, _ := json.Marshal(unique(next))
	_, err = db.Exec(r.Context(), `UPDATE campaigns SET selected_channels = $2, status = $3, failed_count = 0, updated_at = now() WHERE id = $1`, id, selected, campaigns.StatusRetrying)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	campaign, _ = getCampaign(r.Context(), id)
	if err := publishDispatch(r.Context(), campaign); err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func stats(w http.ResponseWriter, r *http.Request, id string) {
	campaign, err := getCampaign(r.Context(), id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, campaign.Snapshot)
}

func deliveries(w http.ResponseWriter, r *http.Request, id string) {
	rows, err := db.Query(r.Context(), `
		SELECT id, campaign_id, user_id, channel_code, status, error_code, error_message, attempt, finished_at
		FROM message_deliveries
		WHERE campaign_id = $1
		ORDER BY created_at DESC
		LIMIT 500`, id)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var row struct {
			ID, CampaignID, UserID, ChannelCode, Status string
			ErrorCode, ErrorMessage                     *string
			Attempt                                     int
			FinishedAt                                  *time.Time
		}
		if err := rows.Scan(&row.ID, &row.CampaignID, &row.UserID, &row.ChannelCode, &row.Status, &row.ErrorCode, &row.ErrorMessage, &row.Attempt, &row.FinishedAt); err != nil {
			httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out = append(out, map[string]any{
			"id": row.ID, "campaign_id": row.CampaignID, "user_id": row.UserID, "channel_code": row.ChannelCode,
			"status": row.Status, "error_code": row.ErrorCode, "error_message": row.ErrorMessage, "attempt": row.Attempt, "finished_at": row.FinishedAt,
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func errorGroups(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	groups, err := listErrorGroups(r.Context(), id)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, groups)
}

func handleErrorGroupAction(w http.ResponseWriter, r *http.Request, campaignID, groupID, action string) {
	if r.Method != http.MethodPost {
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	switch action {
	case "retry":
		count, err := requeueErrorGroup(r.Context(), campaignID, groupID)
		writeGroupActionResult(w, r, campaignID, "retry", count, err)
	case "switch-channel":
		var req errorGroupActionRequest
		_ = httpapi.ReadJSON(r, &req)
		if req.ToChannel == "" {
			httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "to_channel_required"})
			return
		}
		count, err := switchErrorGroup(r.Context(), campaignID, groupID, req.ToChannel)
		writeGroupActionResult(w, r, campaignID, "switch-channel", count, err)
	case "cancel":
		count, err := cancelErrorGroup(r.Context(), campaignID, groupID)
		writeGroupActionResult(w, r, campaignID, "cancel", count, err)
	default:
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_group_action"})
	}
}

func writeGroupActionResult(w http.ResponseWriter, r *http.Request, campaignID, action string, count int, err error) {
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	campaign, err := getCampaign(r.Context(), campaignID)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"action": action, "queued": count, "campaign": campaign})
}

func listErrorGroups(ctx context.Context, campaignID string) ([]contracts.ErrorGroup, error) {
	rows, err := db.Query(ctx, `
		SELECT
			md5(channel_code || ':' || COALESCE(error_code, '') || ':' || COALESCE(error_message, '')) AS group_id,
			channel_code,
			COALESCE(error_code, ''),
			COALESCE(error_message, ''),
			count(*)::int,
			max(attempt)::int,
			min(finished_at),
			max(finished_at)
		FROM message_deliveries
		WHERE campaign_id = $1 AND status = 'failed'
		GROUP BY group_id, channel_code, COALESCE(error_code, ''), COALESCE(error_message, '')
		ORDER BY count(*) DESC, max(finished_at) DESC`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []contracts.ErrorGroup{}
	for rows.Next() {
		var group contracts.ErrorGroup
		group.CampaignID = campaignID
		if err := rows.Scan(&group.ID, &group.ChannelCode, &group.ErrorCode, &group.ErrorMessage, &group.FailedCount, &group.MaxAttempt, &group.FirstSeenAt, &group.LastSeenAt); err != nil {
			return nil, err
		}
		group.Impact = "Затронуто " + strconv.Itoa(group.FailedCount) + " сообщений. Основная очередь продолжает обработку."
		group.RecommendedActions = []contracts.ErrorAction{
			{Code: "retry", Label: "Повторить группу"},
			{Code: "switch_channel", Label: "Вставить через другой канал"},
			{Code: "cancel_group", Label: "Закрыть группу"},
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func requeueErrorGroup(ctx context.Context, campaignID, groupID string) (int, error) {
	if mq == nil {
		return 0, errors.New("rabbitmq_unavailable")
	}
	rows, err := failedRowsForGroup(ctx, campaignID, groupID)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.IdempotencyKey)
		req := contracts.SendMessageRequest{
			CampaignID: campaignID, UserID: row.UserID, ChannelCode: row.ChannelCode, MessageBody: row.MessageBody,
			Attempt: row.Attempt + 1, IdempotencyKey: row.IdempotencyKey,
		}
		if err := appruntime.PublishJSONPriority(ctx, mq, "", appruntime.QueueMessageSend, 8, req); err != nil {
			return 0, err
		}
	}
	if err := markFailedRowsQueued(ctx, campaignID, keys); err != nil {
		return 0, err
	}
	if err := publishGroupStatusEvents(ctx, campaignID, rows, "queued"); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func switchErrorGroup(ctx context.Context, campaignID, groupID, toChannel string) (int, error) {
	if mq == nil {
		return 0, errors.New("rabbitmq_unavailable")
	}
	rows, err := failedRowsForGroup(ctx, campaignID, groupID)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, row := range rows {
		if row.ChannelCode == toChannel {
			return 0, errors.New("alternative_channel_required")
		}
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.IdempotencyKey)
		req := contracts.SendMessageRequest{
			CampaignID: campaignID, UserID: row.UserID, ChannelCode: toChannel, MessageBody: row.MessageBody,
			Attempt: 1, IdempotencyKey: campaigns.IdempotencyKey(campaignID, row.UserID, toChannel) + ":switch:" + row.ID,
		}
		if err := appruntime.PublishJSONPriority(ctx, mq, "", appruntime.QueueMessageSend, 9, req); err != nil {
			return 0, err
		}
	}
	if err := markFailedRowsSuperseded(ctx, campaignID, keys); err != nil {
		return 0, err
	}
	if err := addSelectedChannel(ctx, campaignID, toChannel); err != nil {
		return 0, err
	}
	if err := publishGroupStatusEvents(ctx, campaignID, rows, "superseded"); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func cancelErrorGroup(ctx context.Context, campaignID, groupID string) (int, error) {
	rows, err := failedRowsForGroup(ctx, campaignID, groupID)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.IdempotencyKey)
	}
	if err := markFailedRowsCancelled(ctx, campaignID, keys); err != nil {
		return 0, err
	}
	if err := publishGroupStatusEvents(ctx, campaignID, rows, "cancelled"); err != nil {
		return 0, err
	}
	return len(rows), nil
}

type failedDeliveryRow struct {
	ID             string
	UserID         string
	ChannelCode    string
	MessageBody    string
	Attempt        int
	IdempotencyKey string
}

func failedRowsForGroup(ctx context.Context, campaignID, groupID string) ([]failedDeliveryRow, error) {
	rows, err := db.Query(ctx, `
		SELECT id, user_id, channel_code, message_body, attempt, idempotency_key
		FROM message_deliveries
		WHERE campaign_id = $1
		  AND status = 'failed'
		  AND md5(channel_code || ':' || COALESCE(error_code, '') || ':' || COALESCE(error_message, '')) = $2
		ORDER BY finished_at ASC
		LIMIT 5000`, campaignID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []failedDeliveryRow{}
	for rows.Next() {
		var row failedDeliveryRow
		if err := rows.Scan(&row.ID, &row.UserID, &row.ChannelCode, &row.MessageBody, &row.Attempt, &row.IdempotencyKey); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func markFailedRowsQueued(ctx context.Context, campaignID string, keys []string) error {
	_, err := db.Exec(ctx, `
		UPDATE message_deliveries
		SET status = 'queued', error_code = NULL, error_message = NULL, updated_at = now()
		WHERE campaign_id = $1 AND idempotency_key = ANY($2)`, campaignID, keys)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		UPDATE campaigns
		SET status = $2,
		    sent_count = GREATEST(sent_count - $3, 0),
		    failed_count = GREATEST(failed_count - $3, 0),
		    finished_at = NULL,
		    updated_at = now()
		WHERE id = $1`, campaignID, campaigns.StatusRunning, len(keys))
	return err
}

func markFailedRowsCancelled(ctx context.Context, campaignID string, keys []string) error {
	_, err := db.Exec(ctx, `
		UPDATE message_deliveries
		SET status = 'cancelled', updated_at = now()
		WHERE campaign_id = $1 AND idempotency_key = ANY($2)`, campaignID, keys)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		UPDATE campaigns
		SET failed_count = GREATEST(failed_count - $2, 0),
		    cancelled_count = cancelled_count + $2,
		    updated_at = now()
		WHERE id = $1`, campaignID, len(keys))
	return err
}

func markFailedRowsSuperseded(ctx context.Context, campaignID string, keys []string) error {
	_, err := db.Exec(ctx, `
		UPDATE message_deliveries
		SET status = 'cancelled', updated_at = now()
		WHERE campaign_id = $1 AND idempotency_key = ANY($2)`, campaignID, keys)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		UPDATE campaigns
		SET status = $2,
		    sent_count = GREATEST(sent_count - $3, 0),
		    failed_count = GREATEST(failed_count - $3, 0),
		    finished_at = NULL,
		    updated_at = now()
		WHERE id = $1`, campaignID, campaigns.StatusRunning, len(keys))
	return err
}

func addSelectedChannel(ctx context.Context, campaignID, toChannel string) error {
	campaign, err := getCampaign(ctx, campaignID)
	if err != nil {
		return err
	}
	selected, _ := json.Marshal(unique(append(campaign.SelectedChannels, toChannel)))
	_, err = db.Exec(ctx, `UPDATE campaigns SET selected_channels = $2, updated_at = now() WHERE id = $1`, campaignID, selected)
	return err
}

func publishGroupStatusEvents(ctx context.Context, campaignID string, rows []failedDeliveryRow, status string) error {
	if mq == nil {
		return nil
	}
	total := campaignTotalMessages(ctx, campaignID)
	eventType := "message." + status
	for _, row := range rows {
		event := contracts.MessageStatusEvent{
			Type:           eventType,
			CampaignID:     campaignID,
			TotalMessages:  total,
			UserID:         row.UserID,
			ChannelCode:    row.ChannelCode,
			Status:         status,
			Attempt:        row.Attempt,
			IdempotencyKey: row.IdempotencyKey,
			FinishedAt:     time.Now().UTC(),
		}
		if err := appruntime.PublishJSON(ctx, mq, appruntime.ExchangeMessageStatus, eventType, event); err != nil {
			return err
		}
	}
	return nil
}

func campaignTotalMessages(ctx context.Context, campaignID string) int {
	var total int
	_ = db.QueryRow(ctx, `SELECT total_messages FROM campaigns WHERE id = $1`, campaignID).Scan(&total)
	return total
}

func publishDispatch(ctx context.Context, campaign Campaign) error {
	if mq == nil {
		return errors.New("rabbitmq_unavailable")
	}
	body := ""
	_ = db.QueryRow(ctx, `SELECT body FROM templates WHERE id = $1`, campaign.TemplateID).Scan(&body)
	req := contracts.CampaignDispatchRequest{
		CampaignID:       campaign.ID,
		TemplateID:       campaign.TemplateID,
		MessageBody:      body,
		TotalRecipients:  campaign.TotalRecipients,
		SelectedChannels: campaign.SelectedChannels,
		BatchSize:        appruntime.EnvInt("DISPATCH_BATCH_SIZE", campaigns.DefaultDispatchBatchSize),
		RequestedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	return appruntime.PublishJSON(ctx, mq, "", appruntime.QueueCampaignDispatch, req)
}

func getCampaign(ctx context.Context, id string) (Campaign, error) {
	row := db.QueryRow(ctx, `
		SELECT c.id, c.name, c.template_id, COALESCE(t.name, ''), c.status, c.filters, c.selected_channels,
		       c.total_recipients, c.total_messages, c.sent_count, c.success_count, c.failed_count, c.cancelled_count,
		       c.p95_dispatch_ms, c.created_at, c.started_at, c.finished_at
		FROM campaigns c
		LEFT JOIN templates t ON t.id = c.template_id
		WHERE c.id = $1`, id)
	return scanCampaign(row)
}

func scanCampaign(row pgx.Row) (Campaign, error) {
	var campaign Campaign
	var selected []byte
	if err := row.Scan(
		&campaign.ID, &campaign.Name, &campaign.TemplateID, &campaign.TemplateName, &campaign.Status,
		&campaign.Filters, &selected, &campaign.TotalRecipients, &campaign.TotalMessages,
		&campaign.SentCount, &campaign.SuccessCount, &campaign.FailedCount, &campaign.CancelledCount,
		&campaign.P95DispatchMs, &campaign.CreatedAt, &campaign.StartedAt, &campaign.FinishedAt,
	); err != nil {
		return Campaign{}, err
	}
	_ = json.Unmarshal(selected, &campaign.SelectedChannels)
	progress := campaigns.Progress{
		CampaignID: campaign.ID, TotalMessages: campaign.TotalMessages, Success: campaign.SuccessCount,
		Failed: campaign.FailedCount, Cancelled: campaign.CancelledCount, IsCancelled: campaign.Status == campaigns.StatusCancelled,
	}
	campaign.Snapshot = progress.Snapshot()
	if campaign.Status == campaigns.StatusCreated {
		campaign.Snapshot.Status = campaigns.StatusCreated
	} else if campaign.Status == campaigns.StatusRetrying {
		campaign.Snapshot.Status = campaigns.StatusRetrying
	}
	return campaign, nil
}

func ensureSchema(ctx context.Context) error {
	_, err := db.Exec(ctx, `ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS p95_dispatch_ms int NOT NULL DEFAULT 0`)
	return err
}

func countAudience(ctx context.Context, filters json.RawMessage) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count)
	return count, err
}

func writeLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + "-" + hex.EncodeToString(buf)
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
