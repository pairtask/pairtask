package protocol

import "time"

// The shadow classifier's report (#339): what the broker recorded about
// untagged operator messages, read-only, for the operator.

// TypeOperatorClassifierReport is the request and the response.
const TypeOperatorClassifierReport = "operator_classifier_report"

// ClassifierReportMax bounds the predictions one report covers.
const ClassifierReportMax = 1000

// ClassifierReportRequest asks for the predictions recorded since a time.
type ClassifierReportRequest struct {
	Since time.Time `json:"since"`
}

// ClassifierReport summarises the recorded predictions. Agreement with the
// reply claim's holder is a weak label: the fastest claimant, not the right
// answer.
type ClassifierReport struct {
	Since       time.Time              `json:"since"`
	Count       int                    `json:"count"`
	Truncated   bool                   `json:"truncated"`
	Outcomes    map[string]int         `json:"outcomes"` // predicted, abstain, error, timeout, skipped
	Reasons     map[string]int         `json:"reasons"`  // by outcome reason
	Models      map[string]int         `json:"models"`   // provider/model/version seen
	LatencyMS   ClassifierLatency      `json:"latency_ms"`
	Agreement   ClassifierAgreement    `json:"agreement"`
	Threshold   float64                `json:"threshold"`             // the last configured threshold seen; uncalibrated
	Usage       map[string]int64       `json:"usage"`                 // the providers\' usage counters summed, by their names
	Predictions []ClassifierPrediction `json:"predictions,omitempty"` // the newest, at most ClassifierReportListed
	Roles       map[string]string      `json:"roles,omitempty"`       // the roles the model now sees, by agent: declared by the agents, else configured
}

// ClassifierReportListed bounds the predictions listed in a report.
const ClassifierReportListed = 50

// ClassifierLatency is the provider latency over the asked predictions.
type ClassifierLatency struct {
	Asked int   `json:"asked"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
	Max   int64 `json:"max"`
}

// ClassifierAgreement compares predictions with the reply claim's holder.
type ClassifierAgreement struct {
	Compared  int `json:"compared"`  // predictions with a reply claim to compare against
	Agreed    int `json:"agreed"`    // the predicted agent holds the reply claim
	Disagreed int `json:"disagreed"` // another agent holds it
	Unclaimed int `json:"unclaimed"` // predicted, nobody has claimed yet
}

// ClassifierPrediction is one recorded decision as the report lists it.
type ClassifierPrediction struct {
	MessageID  string    `json:"message_id"`
	Outcome    string    `json:"outcome"`
	Reason     string    `json:"reason,omitempty"`
	AgentID    string    `json:"agent_id,omitempty"`
	Confidence float64   `json:"confidence"`
	Claimant   string    `json:"claimant,omitempty"` // who holds the reply claim, when someone does
	Model      string    `json:"model"`
	LatencyMS  int64     `json:"latency_ms"`
	CreatedAt  time.Time `json:"created_at"`
}
