package reliability

import "fmt"

const (
	DecisionRetry = "retry"
	DecisionDLQ   = "dlq"
)

type RetryPolicy struct {
	Limit int
}

func (p RetryPolicy) Decide(attempt int) string {
	limit := p.Limit
	if limit <= 0 {
		limit = 3
	}
	if attempt >= limit {
		return DecisionDLQ
	}
	return DecisionRetry
}

type Action struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type ActionableError struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Impact      string   `json:"impact"`
	Actions     []Action `json:"actions"`
}

func BuildChannelError(channel string, affected int) ActionableError {
	return ActionableError{
		Title:       fmt.Sprintf("%s временно недоступен", channel),
		Description: fmt.Sprintf("Часть сообщений не была отправлена через %s из-за ошибки канала.", channel),
		Impact:      fmt.Sprintf("Остальные каналы продолжают отправку. Ошибка затронула %d сообщений.", affected),
		Actions: []Action{
			{Code: "retry", Label: "Повторить отправку"},
			{Code: "switch_channel", Label: "Отправить через другой канал"},
			{Code: "cancel_campaign", Label: "Отменить кампанию"},
		},
	}
}
