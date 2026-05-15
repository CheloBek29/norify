package contracts

import "time"

type CampaignProgressEvent struct {
	Type            string    `json:"type"`
	CampaignID      string    `json:"campaign_id"`
	Status          string    `json:"status"`
	TotalMessages   int       `json:"total_messages"`
	Processed       int       `json:"processed"`
	Success         int       `json:"success"`
	Failed          int       `json:"failed"`
	Cancelled       int       `json:"cancelled"`
	ProgressPercent float64   `json:"progress_percent"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type CampaignDispatchRequest struct {
	CampaignID       string   `json:"campaign_id"`
	TemplateID       string   `json:"template_id"`
	MessageBody      string   `json:"message_body"`
	TotalRecipients  int      `json:"total_recipients"`
	SelectedChannels []string `json:"selected_channels"`
	BatchSize        int      `json:"batch_size"`
	RequestedAt      string   `json:"requested_at"`
}

type CampaignDispatchMetrics struct {
	CampaignID    string    `json:"campaign_id"`
	TotalMessages int       `json:"total_messages"`
	BatchCount    int       `json:"batch_count"`
	DurationMs    int       `json:"duration_ms"`
	P95DispatchMs int       `json:"p95_dispatch_ms"`
	ReportedAt    time.Time `json:"reported_at"`
}

type SendMessageRequest struct {
	CampaignID     string `json:"campaign_id"`
	UserID         string `json:"user_id"`
	ChannelCode    string `json:"channel_code"`
	MessageBody    string `json:"message_body"`
	Attempt        int    `json:"attempt"`
	IdempotencyKey string `json:"idempotency_key"`
}

type MessageStatusEvent struct {
	Type           string    `json:"type"`
	CampaignID     string    `json:"campaign_id"`
	TotalMessages  int       `json:"total_messages"`
	UserID         string    `json:"user_id"`
	ChannelCode    string    `json:"channel_code"`
	Status         string    `json:"status"`
	ErrorCode      string    `json:"error_code,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	Attempt        int       `json:"attempt"`
	IdempotencyKey string    `json:"idempotency_key"`
	FinishedAt     time.Time `json:"finished_at"`
}

type ErrorAction struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type ErrorGroup struct {
	ID                 string        `json:"id"`
	CampaignID         string        `json:"campaign_id"`
	ChannelCode        string        `json:"channel_code"`
	ErrorCode          string        `json:"error_code"`
	ErrorMessage       string        `json:"error_message"`
	FailedCount        int           `json:"failed_count"`
	MaxAttempt         int           `json:"max_attempt"`
	FirstSeenAt        time.Time     `json:"first_seen_at"`
	LastSeenAt         time.Time     `json:"last_seen_at"`
	RecommendedActions []ErrorAction `json:"recommended_actions"`
	Impact             string        `json:"impact"`
}
