package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	QueueCampaignDispatch  = "campaign.dispatch.request"
	QueueMessageSend       = "message.send.request"
	QueueMessageRetry      = "message.send.retry"
	QueueMessageDLQ        = "message.send.dlq"
	QueueMessageResult     = "message.send.result"
	QueueMessageError      = "message.send.error"
	ExchangeMessageStatus  = "message.status.events"
	ExchangeCampaignStatus = "campaign.status.events"
	ExchangeMessageDLX     = "message.send.dlx"
)

type PublishOptions struct {
	MessageID     string
	CorrelationID string
	Headers       amqp.Table
	Priority      uint8
}

func Env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func EnvInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

func OpenPostgres(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := Env("POSTGRES_DSN", "")
	if dsn == "" {
		return nil, errors.New("POSTGRES_DSN is empty")
	}
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				return pool, nil
			} else {
				lastErr = pingErr
				pool.Close()
			}
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return nil, lastErr
}

func OpenRabbit() (*amqp.Connection, *amqp.Channel, error) {
	url := Env("RABBITMQ_URL", "")
	if url == "" {
		return nil, nil, errors.New("RABBITMQ_URL is empty")
	}
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		conn, err := amqp.Dial(url)
		if err == nil {
			channel, err := conn.Channel()
			if err == nil {
				if err := DeclareTopology(channel); err != nil {
					_ = channel.Close()
					_ = conn.Close()
					lastErr = err
				} else {
					return conn, channel, nil
				}
			} else {
				_ = conn.Close()
				lastErr = err
			}
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return nil, nil, lastErr
}

func DeclareTopology(channel *amqp.Channel) error {
	for _, exchange := range []struct {
		name string
		kind string
	}{{ExchangeMessageStatus, "topic"}, {ExchangeCampaignStatus, "topic"}, {ExchangeMessageDLX, "direct"}} {
		if err := channel.ExchangeDeclare(exchange.name, exchange.kind, true, false, false, false, nil); err != nil {
			return err
		}
	}
	queueArgs := map[string]amqp.Table{
		QueueMessageSend: {
			"x-dead-letter-exchange": "message.send.dlx",
			"x-max-priority":         int32(10),
		},
		QueueMessageRetry: {
			"x-dead-letter-exchange":    "",
			"x-dead-letter-routing-key": QueueMessageSend,
			"x-message-ttl":             int32(5000),
		},
	}
	queues := []string{QueueCampaignDispatch, QueueMessageSend, QueueMessageRetry, QueueMessageDLQ, QueueMessageResult, QueueMessageError, ExchangeMessageStatus, ExchangeCampaignStatus}
	for _, queue := range queues {
		if _, err := channel.QueueDeclare(queue, true, false, false, false, queueArgs[queue]); err != nil {
			return err
		}
	}
	if err := channel.QueueBind(ExchangeMessageStatus, "#", ExchangeMessageStatus, false, nil); err != nil {
		return err
	}
	if err := channel.QueueBind(ExchangeCampaignStatus, "#", ExchangeCampaignStatus, false, nil); err != nil {
		return err
	}
	return channel.QueueBind(QueueMessageDLQ, "failed", ExchangeMessageDLX, false, nil)
}

func PublishJSON(ctx context.Context, channel *amqp.Channel, exchange, routingKey string, payload any) error {
	return PublishJSONPriority(ctx, channel, exchange, routingKey, 0, payload)
}

func PublishJSONPriority(ctx context.Context, channel *amqp.Channel, exchange, routingKey string, priority uint8, payload any) error {
	return PublishJSONWithOptions(ctx, channel, exchange, routingKey, payload, PublishOptions{Priority: priority})
}

func PublishJSONWithOptions(ctx context.Context, channel *amqp.Channel, exchange, routingKey string, payload any, options PublishOptions) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return channel.PublishWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:   "application/json",
		DeliveryMode:  amqp.Persistent,
		Priority:      options.Priority,
		MessageId:     options.MessageID,
		CorrelationId: options.CorrelationID,
		Headers:       options.Headers,
		Timestamp:     time.Now().UTC(),
		Body:          body,
	})
}

func MessagePublishOptions(payload any) PublishOptions {
	campaignID := ""
	idempotencyKey := ""
	switch value := payload.(type) {
	case interface {
		Campaign() string
		Idempotency() string
	}:
		campaignID = value.Campaign()
		idempotencyKey = value.Idempotency()
	case struct {
		CampaignID     string
		IdempotencyKey string
	}:
		campaignID = value.CampaignID
		idempotencyKey = value.IdempotencyKey
	}
	if campaignID == "" || idempotencyKey == "" {
		raw, _ := json.Marshal(payload)
		var probe struct {
			CampaignID     string `json:"campaign_id"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(raw, &probe)
		campaignID = probe.CampaignID
		idempotencyKey = probe.IdempotencyKey
	}
	return PublishOptions{
		MessageID:     idempotencyKey,
		CorrelationID: campaignID,
		Headers:       amqp.Table{"x-idempotency-key": idempotencyKey},
	}
}

func DecodeJSON(delivery amqp.Delivery, dest any) error {
	return json.Unmarshal(delivery.Body, dest)
}

func LogStartup(name string, err error) {
	if err != nil {
		slog.Warn("dependency unavailable; service will run degraded", "service", name, "error", err)
	}
}
