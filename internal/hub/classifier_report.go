package hub

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// readClassifierReport summarises the predictions recorded since the
// requested time (the last 24 hours by default), read-only.
func (h *Hub) readClassifierReport(ctx context.Context, req operatorRequest, _ protocol.PageRequest) (protocol.Message, string, error) {
	since := time.Now().Add(-24 * time.Hour)
	if req.msg.ClassifierRequest != nil && !req.msg.ClassifierRequest.Since.IsZero() {
		since = req.msg.ClassifierRequest.Since
	}
	rows, err := h.store.PredictionsSince(ctx, since, protocol.ClassifierReportMax+1)
	if err != nil {
		return protocol.Message{}, "", err
	}
	report := h.classifierReport(ctx, since, rows)
	detail := fmt.Sprintf("since=%s count=%d", since.UTC().Format(time.RFC3339), report.Count)
	return protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierReport: &report}, detail, nil
}

// classifierReport counts outcomes, reasons and models, takes latency
// percentiles over the asked predictions, and compares each predicted
// agent with the reply claim's holder, a weak label.
func (h *Hub) classifierReport(ctx context.Context, since time.Time, rows []store.Prediction) protocol.ClassifierReport {
	report := protocol.ClassifierReport{Since: since, Outcomes: map[string]int{}, Reasons: map[string]int{}, Models: map[string]int{}}
	if len(rows) > protocol.ClassifierReportMax {
		rows, report.Truncated = rows[:protocol.ClassifierReportMax], true
	}
	report.Count = len(rows)
	var latencies []int64
	for _, p := range rows {
		report.Outcomes[p.Outcome]++
		if p.Reason != "" {
			report.Reasons[p.Outcome+":"+p.Reason]++
		}
		if p.Outcome != store.PredictionSkipped {
			report.Models[p.Provider+"/"+p.Model+"/"+p.ModelVersion]++
			latencies = append(latencies, p.LatencyMS)
		}
		report.Threshold = p.Threshold
	}
	report.LatencyMS = latency(latencies)
	report.Predictions, report.Agreement = h.classifierListed(ctx, rows)
	return report
}

// classifierListed lists the newest predictions with their claimant and
// counts the agreement over every predicted row.
func (h *Hub) classifierListed(ctx context.Context, rows []store.Prediction) ([]protocol.ClassifierPrediction, protocol.ClassifierAgreement) {
	var agreement protocol.ClassifierAgreement
	var listed []protocol.ClassifierPrediction
	for i := len(rows) - 1; i >= 0; i-- {
		p := rows[i]
		claimant := h.replyClaimant(ctx, p.MessageID)
		if p.Outcome == store.PredictionPredicted {
			switch {
			case claimant == "":
				agreement.Unclaimed++
			case claimant == p.AgentID:
				agreement.Compared++
				agreement.Agreed++
			default:
				agreement.Compared++
				agreement.Disagreed++
			}
		}
		if len(listed) < protocol.ClassifierReportListed {
			listed = append(listed, protocol.ClassifierPrediction{MessageID: p.MessageID, Outcome: p.Outcome, Reason: p.Reason,
				AgentID: p.AgentID, Confidence: p.Confidence, Claimant: claimant, Model: p.Provider + "/" + p.Model,
				LatencyMS: p.LatencyMS, CreatedAt: p.CreatedAt})
		}
	}
	return listed, agreement
}

// replyClaimant is the agent holding the message's reply claim as owner, or "".
func (h *Hub) replyClaimant(ctx context.Context, messageID string) string {
	set, err := h.store.Claims(ctx, messageID, "")
	if err != nil {
		return ""
	}
	for _, c := range set.Claims {
		if c.Task == protocol.TaskReply && c.Role == protocol.RoleOwner && c.FinishedAt == nil {
			return c.AgentID
		}
	}
	return ""
}

// latency is p50, p95 and max over the values; zero without any.
func latency(values []int64) protocol.ClassifierLatency {
	out := protocol.ClassifierLatency{Asked: len(values)}
	if len(values) == 0 {
		return out
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	out.P50, out.P95, out.Max = values[len(values)/2], values[(len(values)*95)/100], values[len(values)-1]
	return out
}
