package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RunWithReconnect runs fn with a fresh AMQP connection and channel.
// When fn returns (due to channel close, connection drop, or any error),
// it reconnects with exponential backoff and calls fn again until ctx is done.
func RunWithReconnect(ctx context.Context, name string, fn func(ctx context.Context, ch *amqp.Channel) error) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		conn, ch, err := openRabbitOnce()
		if err != nil {
			slog.Error("rabbitmq unavailable, will retry", "service", name, "backoff", backoff, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				if backoff < 30*time.Second {
					backoff *= 2
				}
			}
			continue
		}
		backoff = time.Second

		fnDone := make(chan error, 1)
		go func() { fnDone <- fn(ctx, ch) }()

		connClose := conn.NotifyClose(make(chan *amqp.Error, 1))
		select {
		case <-ctx.Done():
			_ = ch.Close()
			_ = conn.Close()
			return
		case amqpErr := <-connClose:
			slog.Warn("rabbitmq connection lost, reconnecting", "service", name, "error", amqpErr)
		case err := <-fnDone:
			if err != nil {
				slog.Error("consumer exited with error, reconnecting", "service", name, "error", err)
			}
		}
		_ = ch.Close()
		_ = conn.Close()
	}
}

// QueueDepth returns the number of ready messages in a queue via the management API.
// Returns -1 if the management API is unreachable.
func QueueDepth(queueName string) int {
	rabbitURL := Env("RABBITMQ_URL", "")
	if rabbitURL == "" {
		return -1
	}
	u, err := url.Parse(rabbitURL)
	if err != nil {
		return -1
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	mgmtHost := Env("RABBITMQ_MGMT_URL", "http://"+u.Hostname()+":15672")
	apiURL := mgmtHost + "/api/queues/%2F/" + url.PathEscape(queueName)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiURL, nil)
	if err != nil {
		return -1
	}
	req.SetBasicAuth(user, pass)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()

	var result struct {
		Messages int `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return -1
	}
	return result.Messages
}
