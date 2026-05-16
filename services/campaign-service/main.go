package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	ID                 string                        `json:"id"`
	Name               string                        `json:"name"`
	TemplateID         string                        `json:"template_id"`
	TemplateName       string                        `json:"template_name"`
	Status             string                        `json:"status"`
	Filters            json.RawMessage               `json:"filters"`
	SelectedChannels   []string                      `json:"selected_channels"`
	SpecificRecipients []contracts.CampaignRecipient `json:"-"`
	TotalRecipients    int                           `json:"total_recipients"`
	TotalMessages      int                           `json:"total_messages"`
	SentCount          int                           `json:"sent_count"`
	SuccessCount       int                           `json:"success_count"`
	FailedCount        int                           `json:"failed_count"`
	CancelledCount     int                           `json:"cancelled_count"`
	P95DispatchMs      int                           `json:"p95_dispatch_ms"`
	CreatedAt          time.Time                     `json:"created_at"`
	StartedAt          *time.Time                    `json:"started_at,omitempty"`
	FinishedAt         *time.Time                    `json:"finished_at,omitempty"`
	ArchivedAt         *time.Time                    `json:"archived_at,omitempty"`
	Snapshot           campaigns.Snapshot            `json:"snapshot"`
}

type createCampaignRequest struct {
	Name               string                        `json:"name"`
	TemplateID         string                        `json:"template_id"`
	Filters            json.RawMessage               `json:"filters"`
	SelectedChannels   []string                      `json:"selected_channels"`
	SpecificRecipients []contracts.CampaignRecipient `json:"specific_recipients,omitempty"`
	TotalRecipients    int                           `json:"total_recipients"`
}

type switchChannelRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type errorGroupActionRequest struct {
	ToChannel string `json:"to_channel"`
}

var db *pgxpool.Pool

var (
	mq   *amqp.Channel
	mqMu sync.RWMutex
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("campaign-service postgres", err)
	if db != nil {
		appruntime.LogStartup("campaign-service schema", ensureSchema(ctx))
	}
	startCampaignPublisher(ctx)

	mux := httpapi.NewMux(httpapi.Service{Name: "campaign-service", Version: "0.2.0", Ready: func() bool { return db != nil && publisherAvailable() }})
	mux.HandleFunc("/campaigns", campaignsCollection)
	mux.HandleFunc("/campaigns/", campaignAction)
	_ = httpapi.Listen("campaign-service", mux)
}

func startCampaignPublisher(ctx context.Context) {
	go appruntime.RunWithReconnect(ctx, "campaign-service-publisher", func(ctx context.Context, channel *amqp.Channel) error {
		setPublisher(channel)
		slog.Info("rabbitmq publisher connected", "service", "campaign-service")
		defer clearPublisher(channel)

		closed := channel.NotifyClose(make(chan *amqp.Error, 1))
		select {
		case <-ctx.Done():
			return nil
		case err := <-closed:
			if err == nil {
				return errors.New("rabbitmq publisher channel closed")
			}
			return fmt.Errorf("rabbitmq publisher channel closed: %w", err)
		}
	})
}

func setPublisher(channel *amqp.Channel) {
	mqMu.Lock()
	defer mqMu.Unlock()
	mq = channel
}

func clearPublisher(channel *amqp.Channel) {
	mqMu.Lock()
	defer mqMu.Unlock()
	if mq == channel {
		mq = nil
	}
}

func currentPublisher() *amqp.Channel {
	mqMu.RLock()
	defer mqMu.RUnlock()
	return mq
}

func publisherAvailable() bool {
	return currentPublisher() != nil
}

func resetPublisher(channel *amqp.Channel) {
	clearPublisher(channel)
	_ = channel.Close()
}

func publishWithPublisher(ctx context.Context, operation string, publish func(*amqp.Channel) error) error {
	channel := currentPublisher()
	if channel == nil {
		return errors.New("rabbitmq_unavailable")
	}
	if err := publish(channel); err != nil {
		slog.Warn("rabbitmq publish failed; publisher will reconnect", "service", "campaign-service", "operation", operation, "error", err)
		resetPublisher(channel)
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func publishJSON(ctx context.Context, operation, exchange, routingKey string, payload any) error {
	return publishJSONPriority(ctx, operation, exchange, routingKey, 0, payload)
}

func publishJSONPriority(ctx context.Context, operation, exchange, routingKey string, priority uint8, payload any) error {
	return publishWithPublisher(ctx, operation, func(channel *amqp.Channel) error {
		return appruntime.PublishJSONPriority(ctx, channel, exchange, routingKey, priority, payload)
	})
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
		SELECT c.id, c.name, c.template_id, COALESCE(t.name, ''), c.status, c.filters, c.selected_channels, c.specific_recipients,
		       c.total_recipients, c.total_messages, c.sent_count, c.success_count, c.failed_count, c.cancelled_count,
		       c.p95_dispatch_ms, c.created_at, c.started_at, c.finished_at, c.archived_at
		FROM campaigns c
		LEFT JOIN templates t ON t.id = c.template_id
		ORDER BY COALESCE(c.archived_at, c.created_at) DESC, c.created_at DESC
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
	specificRecipients, specificTotalRecipients, specificTotalMessages := prepareSpecificRecipients(req)
	if len(specificRecipients) > 0 {
		req.TotalRecipients = specificTotalRecipients
	} else if req.TotalRecipients <= 0 {
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
	specific, _ := json.Marshal(specificRecipients)
	totalMessages := campaigns.TotalMessages(req.TotalRecipients, req.SelectedChannels)
	if len(specificRecipients) > 0 {
		totalMessages = specificTotalMessages
	}
	_, err := db.Exec(r.Context(), `
		INSERT INTO campaigns (id, name, template_id, status, filters, selected_channels, specific_recipients, total_recipients, total_messages)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, req.Name, req.TemplateID, campaigns.StatusCreated, req.Filters, selected, specific, req.TotalRecipients, totalMessages)
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
	case "archive":
		archiveCampaign(w, r, id)
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
		SET p95_dispatch_ms = GREATEST(p95_dispatch_ms, $2), updated_at = now()
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
	_ = publishCampaignProgress(r.Context(), campaign)
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func startCampaign(w http.ResponseWriter, r *http.Request, id string) {
	now := time.Now().UTC()
	campaign, rollback, started, err := transitionCampaignToRunning(r.Context(), id, now)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if !started {
		httpapi.WriteJSON(w, http.StatusConflict, map[string]string{"error": "campaign_not_startable"})
		return
	}
	if err := publishDispatch(r.Context(), campaign); err != nil {
		_, _ = db.Exec(r.Context(), `UPDATE campaigns SET status = $2, started_at = $3, updated_at = now() WHERE id = $1 AND status = $4`, id, rollback.Status, rollback.StartedAt, campaigns.StatusRunning)
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	campaign, _ = getCampaign(r.Context(), id)
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func transitionCampaignToRunning(ctx context.Context, id string, startedAt time.Time) (Campaign, rollbackStartState, bool, error) {
	row := db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id, status, started_at
			FROM campaigns
			WHERE id = $1
			  AND status IN ($4, $5)
			FOR UPDATE
		),
		updated AS (
			UPDATE campaigns c
			SET status = $2,
			    started_at = COALESCE(c.started_at, $3),
			    updated_at = now()
			FROM candidate
			WHERE c.id = candidate.id
			RETURNING candidate.status AS previous_status, candidate.started_at AS previous_started_at,
			          c.id, c.name, c.template_id, c.status, c.filters, c.selected_channels, c.specific_recipients,
			          c.total_recipients, c.total_messages, c.sent_count, c.success_count,
			          c.failed_count, c.cancelled_count, c.p95_dispatch_ms, c.created_at,
			          c.started_at, c.finished_at, c.archived_at
		)
		SELECT u.previous_status, u.previous_started_at,
		       u.id, u.name, u.template_id, COALESCE(t.name, ''), u.status, u.filters, u.selected_channels, u.specific_recipients,
		       u.total_recipients, u.total_messages, u.sent_count, u.success_count, u.failed_count, u.cancelled_count,
		       u.p95_dispatch_ms, u.created_at, u.started_at, u.finished_at, u.archived_at
		FROM updated u
		LEFT JOIN templates t ON t.id = u.template_id`,
		id, campaigns.StatusRunning, startedAt, campaigns.StatusCreated, campaigns.StatusStopped)
	var previous rollbackStartState
	var campaign Campaign
	var selected []byte
	var specific []byte
	err := row.Scan(
		&previous.Status, &previous.StartedAt,
		&campaign.ID, &campaign.Name, &campaign.TemplateID, &campaign.TemplateName, &campaign.Status,
		&campaign.Filters, &selected, &specific, &campaign.TotalRecipients, &campaign.TotalMessages,
		&campaign.SentCount, &campaign.SuccessCount, &campaign.FailedCount, &campaign.CancelledCount,
		&campaign.P95DispatchMs, &campaign.CreatedAt, &campaign.StartedAt, &campaign.FinishedAt, &campaign.ArchivedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, lookupErr := getCampaign(ctx, id); lookupErr != nil {
			return Campaign{}, rollbackStartState{}, false, lookupErr
		}
		return Campaign{}, rollbackStartState{}, false, nil
	}
	if err != nil {
		return Campaign{}, rollbackStartState{}, false, err
	}
	_ = json.Unmarshal(selected, &campaign.SelectedChannels)
	_ = json.Unmarshal(specific, &campaign.SpecificRecipients)
	progress := campaigns.Progress{
		CampaignID: campaign.ID, TotalMessages: campaign.TotalMessages, Success: campaign.SuccessCount,
		Failed: campaign.FailedCount, Cancelled: campaign.CancelledCount, IsCancelled: campaign.Status == campaigns.StatusCancelled,
	}
	campaign.Snapshot = progress.Snapshot()
	return campaign, previous, true, nil
}

type rollbackStartState struct {
	Status    string
	StartedAt *time.Time
}

func canStartCampaign(status string) bool {
	return status == campaigns.StatusCreated || status == campaigns.StatusStopped
}

func rollbackStateAfterStartFailure(campaign Campaign) rollbackStartState {
	return rollbackStartState{Status: campaign.Status, StartedAt: campaign.StartedAt}
}

func prepareSpecificRecipients(req createCampaignRequest) ([]contracts.CampaignRecipient, int, int) {
	if len(req.SpecificRecipients) == 0 {
		return nil, 0, 0
	}
	seen := map[string]bool{}
	recipients := make([]contracts.CampaignRecipient, 0, len(req.SpecificRecipients))
	totalMessages := 0
	for _, recipient := range req.SpecificRecipients {
		userID := strings.TrimSpace(recipient.UserID)
		if userID == "" || seen[userID] {
			continue
		}
		seen[userID] = true
		channels := unique(recipient.Channels)
		if len(channels) == 0 {
			channels = unique(req.SelectedChannels)
		}
		recipients = append(recipients, contracts.CampaignRecipient{UserID: userID, Channels: channels})
		totalMessages += len(channels)
	}
	return recipients, len(recipients), totalMessages
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
	_ = publishCampaignProgress(r.Context(), campaign)
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
	_ = publishCampaignProgress(r.Context(), campaign)
	httpapi.WriteJSON(w, http.StatusOK, campaign)
}

func archiveCampaign(w http.ResponseWriter, r *http.Request, id string) {
	_, err := db.Exec(r.Context(), `
		UPDATE campaigns
		SET archived_at = COALESCE(archived_at, now()),
		    updated_at = now()
		WHERE id = $1`, id)
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
	rows, err := claimFailedRowsForCampaign(r.Context(), id, 5000)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		campaign, err := getCampaign(r.Context(), id)
		if err != nil {
			writeLookupError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, campaign)
		return
	}
	if _, err := publishClaimedRetryRows(r.Context(), id, rows); err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	updated, _ := getCampaign(r.Context(), id)
	httpapi.WriteJSON(w, http.StatusOK, updated)
}

func switchChannel(w http.ResponseWriter, r *http.Request, id string) {
	httpapi.WriteJSON(w, http.StatusConflict, map[string]string{
		"error":   "campaign_switch_channel_disabled",
		"message": "Смена канала для всей кампании отключена в демо: требуется точная маршрутизация по конкретным ошибочным доставкам.",
	})
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
	rows, err := claimFailedRowsForGroup(ctx, campaignID, groupID, 5000)
	if err != nil {
		return 0, err
	}
	return publishClaimedRetryRows(ctx, campaignID, rows)
}

func switchErrorGroup(ctx context.Context, campaignID, groupID, toChannel string) (int, error) {
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
		if err := publishJSONPriority(ctx, "switch-error-group", "", appruntime.QueueMessageSend, 9, req); err != nil {
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

func claimFailedRowsForCampaign(ctx context.Context, campaignID string, limit int) ([]failedDeliveryRow, error) {
	return claimFailedRows(ctx, campaignID, "", limit)
}

func claimFailedRowsForGroup(ctx context.Context, campaignID, groupID string, limit int) ([]failedDeliveryRow, error) {
	return claimFailedRows(ctx, campaignID, groupID, limit)
}

func claimFailedRows(ctx context.Context, campaignID, groupID string, limit int) ([]failedDeliveryRow, error) {
	if limit <= 0 {
		limit = 5000
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	groupFilter := ""
	args := []any{campaignID, limit}
	if groupID != "" {
		groupFilter = "AND md5(channel_code || ':' || COALESCE(error_code, '') || ':' || COALESCE(error_message, '')) = $3"
		args = append(args, groupID)
	}
	rows, err := tx.Query(ctx, `
		WITH candidate AS (
			SELECT id
			FROM message_deliveries
			WHERE campaign_id = $1
			  AND status IN ('failed', 'error')
			  `+groupFilter+`
			ORDER BY finished_at ASC NULLS LAST, updated_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		),
		claimed AS (
			UPDATE message_deliveries md
			SET status = 'queued',
			    updated_at = now()
			FROM candidate
			WHERE md.id = candidate.id
			  AND md.status IN ('failed', 'error')
			RETURNING md.id, md.user_id, md.channel_code, md.message_body, md.attempt, md.idempotency_key
		)
		SELECT id, user_id, channel_code, message_body, attempt, idempotency_key
		FROM claimed`, args...)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns
			SET status = $2,
			    sent_count = GREATEST(sent_count - $3, 0),
			    failed_count = GREATEST(failed_count - $3, 0),
			    finished_at = NULL,
			    updated_at = now()
			WHERE id = $1`, campaignID, campaigns.StatusRunning, len(out)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func publishClaimedRetryRows(ctx context.Context, campaignID string, rows []failedDeliveryRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.IdempotencyKey)
	}
	for _, row := range rows {
		req := contracts.SendMessageRequest{
			CampaignID: campaignID, UserID: row.UserID, ChannelCode: row.ChannelCode, MessageBody: row.MessageBody,
			Attempt: row.Attempt + 1, IdempotencyKey: row.IdempotencyKey,
		}
		if err := publishJSONPriority(ctx, "retry-failed-delivery", "", appruntime.QueueMessageSend, 8, req); err != nil {
			_ = restoreQueuedRowsFailed(ctx, campaignID, keys)
			return 0, err
		}
	}
	if err := publishGroupStatusEvents(ctx, campaignID, rows, "queued"); err != nil {
		slog.Warn("publish retry status events failed", "campaign_id", campaignID, "error", err)
	}
	return len(rows), nil
}

func restoreQueuedRowsFailed(ctx context.Context, campaignID string, keys []string) error {
	_, err := db.Exec(ctx, `
		UPDATE message_deliveries
		SET status = 'failed', updated_at = now()
		WHERE campaign_id = $1 AND idempotency_key = ANY($2) AND status = 'queued'`, campaignID, keys)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		UPDATE campaigns
		SET sent_count = sent_count + $2,
		    failed_count = failed_count + $2,
		    status = CASE WHEN sent_count + $2 >= total_messages THEN $3 ELSE status END,
		    finished_at = CASE WHEN sent_count + $2 >= total_messages THEN now() ELSE finished_at END,
		    updated_at = now()
		WHERE id = $1`, campaignID, len(keys), campaigns.StatusFinished)
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
	if !publisherAvailable() {
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
		if err := publishJSON(ctx, "publish-group-status", appruntime.ExchangeMessageStatus, eventType, event); err != nil {
			return err
		}
	}
	return nil
}

func publishCampaignProgress(ctx context.Context, campaign Campaign) error {
	if !publisherAvailable() {
		return nil
	}
	processed := campaign.SuccessCount + campaign.FailedCount + campaign.CancelledCount
	progress := 0.0
	if campaign.TotalMessages > 0 {
		progress = float64(processed) / float64(campaign.TotalMessages) * 100
		if progress > 100 {
			progress = 100
		}
	}
	event := contracts.CampaignProgressEvent{
		Type:            "campaign.progress",
		CampaignID:      campaign.ID,
		Status:          campaign.Status,
		TotalMessages:   campaign.TotalMessages,
		Processed:       processed,
		Success:         campaign.SuccessCount,
		Failed:          campaign.FailedCount,
		Cancelled:       campaign.CancelledCount,
		P95DispatchMs:   campaign.P95DispatchMs,
		ProgressPercent: progress,
		UpdatedAt:       time.Now().UTC(),
	}
	return publishJSON(ctx, "publish-campaign-progress", appruntime.ExchangeCampaignStatus, "campaign.progress", event)
}

func campaignTotalMessages(ctx context.Context, campaignID string) int {
	var total int
	_ = db.QueryRow(ctx, `SELECT total_messages FROM campaigns WHERE id = $1`, campaignID).Scan(&total)
	return total
}

func publishDispatch(ctx context.Context, campaign Campaign) error {
	body := ""
	_ = db.QueryRow(ctx, `SELECT body FROM templates WHERE id = $1`, campaign.TemplateID).Scan(&body)
	req := contracts.CampaignDispatchRequest{
		CampaignID:         campaign.ID,
		TemplateID:         campaign.TemplateID,
		MessageBody:        body,
		TotalRecipients:    campaign.TotalRecipients,
		SelectedChannels:   campaign.SelectedChannels,
		SpecificRecipients: campaign.SpecificRecipients,
		BatchSize:          appruntime.EnvInt("DISPATCH_BATCH_SIZE", campaigns.DefaultDispatchBatchSize),
		RequestedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	return publishJSON(ctx, "publish-campaign-dispatch", "", appruntime.QueueCampaignDispatch, req)
}

func getCampaign(ctx context.Context, id string) (Campaign, error) {
	row := db.QueryRow(ctx, `
		SELECT c.id, c.name, c.template_id, COALESCE(t.name, ''), c.status, c.filters, c.selected_channels, c.specific_recipients,
		       c.total_recipients, c.total_messages, c.sent_count, c.success_count, c.failed_count, c.cancelled_count,
		       c.p95_dispatch_ms, c.created_at, c.started_at, c.finished_at, c.archived_at
		FROM campaigns c
		LEFT JOIN templates t ON t.id = c.template_id
		WHERE c.id = $1`, id)
	return scanCampaign(row)
}

func scanCampaign(row pgx.Row) (Campaign, error) {
	var campaign Campaign
	var selected []byte
	var specific []byte
	if err := row.Scan(
		&campaign.ID, &campaign.Name, &campaign.TemplateID, &campaign.TemplateName, &campaign.Status,
		&campaign.Filters, &selected, &specific, &campaign.TotalRecipients, &campaign.TotalMessages,
		&campaign.SentCount, &campaign.SuccessCount, &campaign.FailedCount, &campaign.CancelledCount,
		&campaign.P95DispatchMs, &campaign.CreatedAt, &campaign.StartedAt, &campaign.FinishedAt, &campaign.ArchivedAt,
	); err != nil {
		return Campaign{}, err
	}
	_ = json.Unmarshal(selected, &campaign.SelectedChannels)
	_ = json.Unmarshal(specific, &campaign.SpecificRecipients)
	progress := campaigns.Progress{
		CampaignID: campaign.ID, TotalMessages: campaign.TotalMessages, Success: campaign.SuccessCount,
		Failed: campaign.FailedCount, Cancelled: campaign.CancelledCount, IsCancelled: campaign.Status == campaigns.StatusCancelled,
	}
	campaign.Snapshot = progress.Snapshot()
	if campaign.Status == campaigns.StatusCreated {
		campaign.Snapshot.Status = campaigns.StatusCreated
	} else if campaign.Status == campaigns.StatusRetrying {
		campaign.Snapshot.Status = campaigns.StatusRetrying
	} else if campaign.Status == campaigns.StatusStopped {
		campaign.Snapshot.Status = campaigns.StatusStopped
	}
	return campaign, nil
}

func ensureSchema(ctx context.Context) error {
	_, err := db.Exec(ctx, `
		ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS p95_dispatch_ms int NOT NULL DEFAULT 0;
		ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS archived_at timestamptz;
		ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS specific_recipients jsonb NOT NULL DEFAULT '[]';
		CREATE INDEX IF NOT EXISTS idx_campaigns_active_created_at ON campaigns(created_at DESC) WHERE archived_at IS NULL;
		ALTER TABLE campaigns DROP CONSTRAINT IF EXISTS campaigns_template_id_fkey;`)
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
