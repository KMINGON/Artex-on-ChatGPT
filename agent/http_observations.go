package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
)

type HTTPObservation = db.HTTPObservation
type HTTPObservationResult = db.HTTPObservationResult
type httpObserverKey struct{}
type httpObservationFlushKey struct{}

// HTTPObservationSummary contains only registration counts and IDs. The private
// transport URL, headers and body must never enter this reporting channel.
type HTTPObservationSummary struct {
	HTTPObservationResult
	Dropped    int  `json:"dropped,omitempty"`
	HasMore    bool `json:"has_more,omitempty"`
	ReadFailed bool `json:"read_failed,omitempty"`
}

func WithHTTPObservationFlushReporter(ctx context.Context, report func(HTTPObservationSummary)) context.Context {
	return context.WithValue(ctx, httpObservationFlushKey{}, report)
}

func ReportHTTPObservationFlush(ctx context.Context, summary HTTPObservationSummary) {
	if ctx.Err() != nil {
		return
	}
	if report, _ := ctx.Value(httpObservationFlushKey{}).(func(HTTPObservationSummary)); report != nil {
		report(summary)
	}
}

func WithHTTPObserver(ctx context.Context, observer func(context.Context, []HTTPObservation) HTTPObservationResult) context.Context {
	return context.WithValue(ctx, httpObserverKey{}, observer)
}

func ObserveHTTP(ctx context.Context, observations []HTTPObservation) HTTPObservationResult {
	if ctx.Err() != nil {
		return HTTPObservationResult{Failed: len(observations)}
	}
	if observer, _ := ctx.Value(httpObserverKey{}).(func(context.Context, []HTTPObservation) HTTPObservationResult); observer != nil {
		return observer(ctx, observations)
	}
	return HTTPObservationResult{Skipped: len(observations)}
}
