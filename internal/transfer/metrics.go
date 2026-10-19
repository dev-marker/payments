package transfer

import (
	"github.com/fiston-user/payments/internal/transfer/risk"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("github.com/fiston-user/payments/internal/transfer")

var (
	transfersFinished = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "transfers_finished_total",
		Help: "Transfers that reached a final state: completed, failed or reversed.",
	}, []string{"state"})
	stepRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "transfer_step_retries_total",
		Help: "Saga steps that failed, and will be tried again, by step.",
	}, []string{"step"})
	riskDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "risk_decisions_total",
		Help: "Transfers the risk engine allowed, sent to review, or denied.",
	}, []string{"decision"})
)

// Every series starts at zero, so increase() sees the first transfer too.
func init() {
	for _, s := range []State{Completed, Failed, Reversed} {
		transfersFinished.WithLabelValues(string(s))
	}
	for _, step := range saga {
		stepRetries.WithLabelValues(step.name)
	}
	for _, d := range []risk.Decision{risk.Allow, risk.Review, risk.Deny} {
		riskDecisions.WithLabelValues(d.String())
	}
}

// finished counts t, if its saga is over.
func finished(t Transfer) {
	if t.State.Final() {
		transfersFinished.WithLabelValues(string(t.State)).Inc()
	}
}
