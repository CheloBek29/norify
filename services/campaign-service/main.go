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
	"github.com/jackc/pgx/v5/pgtype"
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
	startDispatchRecovery(ctx)

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
	if len(parts) == 3 && parts[1] == "dispatch" {
		handleDispatchStateAction(w, r, id, parts[2])
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

func handleDispatchStateAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	switch action {
	case "claim":
		claimDispatch(w, r, id)
	case "complete":
		completeDispatch(w, r, id)
	default:
		httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_dispatch_action"})
	}
}

func claimDispatch(w http.ResponseWriter, r *http.Request, id string) {
	claim, ok, err := claimDispatchWindow(r.Context(), id, time.Now().UTC())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, claim)
}

func completeDispatch(w http.ResponseWriter, r *http.Request, id string) {
	var req contracts.CampaignDispatchComplete
	if err := httpapi.ReadJSON(r, &req); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if req.CampaignID == "" {
		req.CampaignID = id
	}
	if req.CampaignID != id || req.LeaseStart <= 0 || req.LeaseEnd < req.LeaseStart {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_dispatch_completion"})
		return
	}
	hasMore, err := completeDispatchWindow(r.Context(), req)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	campaign, err := getCampaign(r.Context(), id)
	if err == nil {
		_ = publishCampaignProgress(r.Context(), campaign)
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"has_more": hasMore})
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
	if err := upsertDispatchState(r.Context(), id); err != nil {
		_, _ = db.Exec(r.Context(), `UPDATE campaigns SET status = $2, started_at = $3, updated_at = now() WHERE id = $1 AND status = $4`, id, rollback.Status, rollback.StartedAt, campaigns.StatusRunning)
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

type recipientWindow struct {
	Start     int
	End       int
	NextStart int
}

func dispatchRecipientWindow(req contracts.CampaignDispatchRequest, maxMessages int) recipientWindow {
	start := req.StartRecipient
	if start <= 0 {
		start = 1
	}
	totalRecipients := req.TotalRecipients
	if len(req.SpecificRecipients) > 0 {
		totalRecipients = len(req.SpecificRecipients)
	}
	channelsCount := len(req.SelectedChannels)
	for _, recipient := range req.SpecificRecipients {
		if len(recipient.Channels) > channelsCount {
			channelsCount = len(recipient.Channels)
		}
	}
	if channelsCount <= 0 {
		channelsCount = 1
	}
	if maxMessages <= 0 {
		maxMessages = 90
	}
	recipients := maxMessages / channelsCount
	if recipients <= 0 {
		recipients = 1
	}
	end := start + recipients - 1
	if end > totalRecipients {
		end = totalRecipients
	}
	nextStart := 0
	if end < totalRecipients {
		nextStart = end + 1
	}
	return recipientWindow{Start: start, End: end, NextStart: nextStart}
}

func upsertDispatchState(ctx context.Context, campaignID string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO campaign_dispatch_state (campaign_id, next_recipient, completed_at, updated_at)
		VALUES ($1, 1, NULL, now())
		ON CONFLICT (campaign_id) DO UPDATE
		SET completed_at = NULL,
		    lease_start = NULL,
		    lease_end = NULL,
		    lease_until = NULL,
		    updated_at = now()
		WHERE campaign_dispatch_state.completed_at IS NOT NULL`, campaignID)
	return err
}

func dispatchLeaseDuration() time.Duration {
	seconds := appruntime.EnvInt("DISPATCH_LEASE_SECONDS", 30)
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

func dispatchRecoveryCooldownSeconds() int {
	seconds := appruntime.EnvInt("DISPATCH_RECOVERY_STALE_SECONDS", 30)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func dispatchSendPriority(processed int) uint8 {
	if processed <= 0 {
		return 9
	}
	return 5
}

func claimDispatchWindow(ctx context.Context, campaignID string, now time.Time) (contracts.CampaignDispatchClaim, bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return contracts.CampaignDispatchClaim{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO campaign_dispatch_state (campaign_id, next_recipient)
		SELECT id, 1 FROM campaigns WHERE id = $1
		ON CONFLICT (campaign_id) DO NOTHING`, campaignID); err != nil {
		return contracts.CampaignDispatchClaim{}, false, err
	}

	row := tx.QueryRow(ctx, `
		SELECT c.id, c.template_id, COALESCE(t.body, ''), c.status, c.archived_at, c.total_recipients,
		       c.sent_count, c.failed_count, c.cancelled_count,
		       c.selected_channels, c.specific_recipients, ds.next_recipient,
		       ds.lease_until, ds.completed_at
		FROM campaigns c
		LEFT JOIN templates t ON t.id = c.template_id
		JOIN campaign_dispatch_state ds ON ds.campaign_id = c.id
		WHERE c.id = $1
		FOR UPDATE OF ds`, campaignID)

	var claim contracts.CampaignDispatchClaim
	var status string
	var archivedAt pgtype.Timestamptz
	var sentCount, failedCount, cancelledCount int
	var selected, specific []byte
	var nextRecipient int
	var leaseUntil, completedAt pgtype.Timestamptz
	if err := row.Scan(&claim.CampaignID, &claim.TemplateID, &claim.MessageBody, &status, &archivedAt, &claim.TotalRecipients, &sentCount, &failedCount, &cancelledCount, &selected, &specific, &nextRecipient, &leaseUntil, &completedAt); err != nil {
		return contracts.CampaignDispatchClaim{}, false, err
	}
	if archivedAt.Valid || (status != campaigns.StatusRunning && status != campaigns.StatusRetrying) {
		if err := tx.Commit(ctx); err != nil {
			return contracts.CampaignDispatchClaim{}, false, err
		}
		return contracts.CampaignDispatchClaim{}, false, nil
	}
	if completedAt.Valid || (leaseUntil.Valid && leaseUntil.Time.After(now)) {
		if err := tx.Commit(ctx); err != nil {
			return contracts.CampaignDispatchClaim{}, false, err
		}
		return contracts.CampaignDispatchClaim{}, false, nil
	}
	_ = json.Unmarshal(selected, &claim.SelectedChannels)
	_ = json.Unmarshal(specific, &claim.SpecificRecipients)
	if len(claim.SpecificRecipients) > 0 {
		claim.TotalRecipients = len(claim.SpecificRecipients)
	}
	if nextRecipient <= 0 {
		nextRecipient = 1
	}
	if claim.TotalRecipients <= 0 || nextRecipient > claim.TotalRecipients {
		if _, err := tx.Exec(ctx, `
			UPDATE campaign_dispatch_state
			SET completed_at = COALESCE(completed_at, $2),
			    lease_start = NULL,
			    lease_end = NULL,
			    lease_until = NULL,
			    updated_at = now()
			WHERE campaign_id = $1`, campaignID, now); err != nil {
			return contracts.CampaignDispatchClaim{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.CampaignDispatchClaim{}, false, err
		}
		return contracts.CampaignDispatchClaim{}, false, nil
	}

	window := dispatchClaimWindow(claim.TotalRecipients, claim.SelectedChannels, claim.SpecificRecipients, nextRecipient, appruntime.EnvInt("DISPATCH_MAX_MESSAGES_PER_TICK", 90))
	claim.BatchSize = appruntime.EnvInt("DISPATCH_BATCH_SIZE", campaigns.DefaultDispatchBatchSize)
	claim.LeaseStart = window.Start
	claim.LeaseEnd = window.End
	claim.HasMore = window.NextStart > 0
	claim.LeaseUntil = now.Add(dispatchLeaseDuration())
	claim.SendPriority = dispatchSendPriority(sentCount + failedCount + cancelledCount)
	if _, err := tx.Exec(ctx, `
		UPDATE campaign_dispatch_state
		SET lease_start = $2,
		    lease_end = $3,
		    lease_until = $4,
		    completed_at = NULL,
		    updated_at = now()
		WHERE campaign_id = $1`, campaignID, claim.LeaseStart, claim.LeaseEnd, claim.LeaseUntil); err != nil {
		return contracts.CampaignDispatchClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.CampaignDispatchClaim{}, false, err
	}
	return claim, true, nil
}

func completeDispatchWindow(ctx context.Context, req contracts.CampaignDispatchComplete) (bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		SELECT c.total_recipients, c.specific_recipients, ds.lease_start, ds.lease_end
		FROM campaigns c
		JOIN campaign_dispatch_state ds ON ds.campaign_id = c.id
		WHERE c.id = $1
		FOR UPDATE OF ds`, req.CampaignID)
	var totalRecipients int
	var leaseStart, leaseEnd pgtype.Int4
	var specific []byte
	if err := row.Scan(&totalRecipients, &specific, &leaseStart, &leaseEnd); err != nil {
		return false, err
	}
	var recipients []contracts.CampaignRecipient
	_ = json.Unmarshal(specific, &recipients)
	if len(recipients) > 0 {
		totalRecipients = len(recipients)
	}
	if !leaseStart.Valid || !leaseEnd.Valid || int(leaseStart.Int32) != req.LeaseStart || int(leaseEnd.Int32) != req.LeaseEnd {
		return false, errors.New("dispatch_lease_mismatch")
	}
	nextRecipient := req.LeaseEnd + 1
	completed := nextRecipient > totalRecipients
	var completedAt any
	if completed {
		completedAt = req.ReportedAt
		if req.ReportedAt.IsZero() {
			completedAt = time.Now().UTC()
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE campaign_dispatch_state
		SET next_recipient = $2,
		    lease_start = NULL,
		    lease_end = NULL,
		    lease_until = NULL,
		    completed_at = $3,
		    updated_at = now()
		WHERE campaign_id = $1`, req.CampaignID, nextRecipient, completedAt); err != nil {
		return false, err
	}
	if req.P95DispatchMs > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns
			SET p95_dispatch_ms = GREATEST(p95_dispatch_ms, $2), updated_at = now()
			WHERE id = $1`, req.CampaignID, req.P95DispatchMs); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return !completed, nil
}

func dispatchClaimWindow(totalRecipients int, selectedChannels []string, specificRecipients []contracts.CampaignRecipient, start, maxMessages int) recipientWindow {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients:    totalRecipients,
		SelectedChannels:   selectedChannels,
		SpecificRecipients: specificRecipients,
		StartRecipient:     start,
	}
	return dispatchRecipientWindow(req, maxMessages)
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

func startDispatchRecovery(ctx context.Context) {
	go func() {
		interval := time.Duration(appruntime.EnvInt("DISPATCH_RECOVERY_INTERVAL_SECONDS", 5)) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if db != nil && publisherAvailable() {
					if count, err := recoverDispatchWakeups(ctx); err != nil {
						slog.Warn("recover dispatch wakeups", "error", err)
					} else if count > 0 {
						slog.Info("recovered dispatch wakeups", "count", count)
					}
				}
				timer.Reset(interval)
			}
		}
	}()
}

func recoverDispatchWakeups(ctx context.Context) (int, error) {
	if _, err := db.Exec(ctx, `
		INSERT INTO campaign_dispatch_state (campaign_id, next_recipient)
		SELECT c.id, 1
		FROM campaigns c
		WHERE c.status IN ($1, $2)
		  AND c.archived_at IS NULL
		ON CONFLICT (campaign_id) DO NOTHING`, campaigns.StatusRunning, campaigns.StatusRetrying); err != nil {
		return 0, err
	}

	rows, err := db.Query(ctx, `
		SELECT c.id
		FROM campaigns c
		JOIN campaign_dispatch_state ds ON ds.campaign_id = c.id
		WHERE c.status IN ($1, $2)
		  AND c.archived_at IS NULL
		  AND ds.completed_at IS NULL
		  AND (ds.lease_until IS NULL OR ds.lease_until < now())
		  AND ds.updated_at < now() - make_interval(secs => $4::int)
		ORDER BY ds.updated_at ASC, c.created_at ASC
		LIMIT $3`, campaigns.StatusRunning, campaigns.StatusRetrying, appruntime.EnvInt("DISPATCH_RECOVERY_WAKEUPS", 100), dispatchRecoveryCooldownSeconds())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var campaignID string
		if err := rows.Scan(&campaignID); err != nil {
			return count, err
		}
		if err := publishDispatchWakeup(ctx, campaignID); err != nil {
			return count, err
		}
		if _, err := db.Exec(ctx, `
			UPDATE campaign_dispatch_state
			SET updated_at = now()
			WHERE campaign_id = $1
			  AND completed_at IS NULL
			  AND (lease_until IS NULL OR lease_until < now())`, campaignID); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func campaignTotalMessages(ctx context.Context, campaignID string) int {
	var total int
	_ = db.QueryRow(ctx, `SELECT total_messages FROM campaigns WHERE id = $1`, campaignID).Scan(&total)
	return total
}

func publishDispatch(ctx context.Context, campaign Campaign) error {
	return publishDispatchWakeup(ctx, campaign.ID)
}

func publishDispatchWakeup(ctx context.Context, campaignID string) error {
	return publishJSON(ctx, "publish-campaign-dispatch", "", appruntime.QueueCampaignDispatch, contracts.CampaignDispatchWakeup{CampaignID: campaignID})
}

func legacyDispatchRequest(ctx context.Context, campaign Campaign) contracts.CampaignDispatchRequest {
	body := ""
	_ = db.QueryRow(ctx, `SELECT body FROM templates WHERE id = $1`, campaign.TemplateID).Scan(&body)
	return contracts.CampaignDispatchRequest{
		CampaignID:         campaign.ID,
		TemplateID:         campaign.TemplateID,
		MessageBody:        body,
		TotalRecipients:    campaign.TotalRecipients,
		SelectedChannels:   campaign.SelectedChannels,
		SpecificRecipients: campaign.SpecificRecipients,
		BatchSize:          appruntime.EnvInt("DISPATCH_BATCH_SIZE", campaigns.DefaultDispatchBatchSize),
		RequestedAt:        time.Now().UTC().Format(time.RFC3339),
	}
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
		ALTER TABLE campaigns DROP CONSTRAINT IF EXISTS campaigns_template_id_fkey;
		CREATE TABLE IF NOT EXISTS campaign_dispatch_state (
		  campaign_id text PRIMARY KEY REFERENCES campaigns(id) ON DELETE CASCADE,
		  next_recipient int NOT NULL DEFAULT 1,
		  lease_start int,
		  lease_end int,
		  lease_until timestamptz,
		  completed_at timestamptz,
		  created_at timestamptz NOT NULL DEFAULT now(),
		  updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_campaign_dispatch_state_recoverable
		  ON campaign_dispatch_state (completed_at, lease_until, updated_at);
		INSERT INTO campaign_dispatch_state (campaign_id, next_recipient, completed_at)
		SELECT c.id, 1,
		       CASE WHEN c.status IN ('finished', 'cancelled') THEN COALESCE(c.finished_at, now()) ELSE NULL END
		FROM campaigns c
		WHERE c.status IN ('running', 'retrying', 'stopped', 'created', 'finished', 'cancelled')
		ON CONFLICT (campaign_id) DO NOTHING;`)
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
