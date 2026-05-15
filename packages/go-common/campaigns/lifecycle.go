package campaigns

import (
	"math"
	"sort"
	"time"
)

const (
	StatusCreated   = "created"
	StatusRunning   = "running"
	StatusRetrying  = "retrying"
	StatusStopped   = "stopped"
	StatusCancelled = "cancelled"
	StatusFinished  = "finished"
)

type Progress struct {
	CampaignID    string
	TotalMessages int
	Success       int
	Failed        int
	Cancelled     int
	IsCancelled   bool
}

type Snapshot struct {
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

func (p Progress) Snapshot() Snapshot {
	processed := p.Success + p.Failed + p.Cancelled
	status := StatusRunning
	if p.IsCancelled {
		status = StatusCancelled
	} else if p.TotalMessages > 0 && processed >= p.TotalMessages {
		status = StatusFinished
	}
	percent := 0.0
	if p.TotalMessages > 0 {
		percent = float64(processed) / float64(p.TotalMessages) * 100
		if percent > 100 {
			percent = 100
		}
	}
	return Snapshot{
		Type:            "campaign.progress",
		CampaignID:      p.CampaignID,
		Status:          status,
		TotalMessages:   p.TotalMessages,
		Processed:       processed,
		Success:         p.Success,
		Failed:          p.Failed,
		Cancelled:       p.Cancelled,
		ProgressPercent: percent,
		UpdatedAt:       time.Now().UTC(),
	}
}

func (p *Progress) RetryFailed() {
	if p.Failed == 0 {
		return
	}
	p.TotalMessages += p.Failed
	p.Failed = 0
	p.IsCancelled = false
}

func (p *Progress) Cancel() {
	p.IsCancelled = true
	processed := p.Success + p.Failed + p.Cancelled
	if p.TotalMessages > processed {
		p.Cancelled += p.TotalMessages - processed
	}
}

func DispatchP95Milliseconds(samples []time.Duration) int {
	if len(samples) == 0 {
		return 0
	}
	values := make([]int, 0, len(samples))
	for _, sample := range samples {
		if sample <= 0 {
			values = append(values, 0)
			continue
		}
		values = append(values, int((sample+time.Millisecond-1)/time.Millisecond))
	}
	sort.Ints(values)
	index := int(math.Ceil(float64(len(values))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	return values[index]
}
