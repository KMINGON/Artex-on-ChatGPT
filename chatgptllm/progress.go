package chatgptllm

import (
	"context"
	"sync/atomic"
)

// StreamProgress contains only local lifecycle information, never model output,
// tool arguments, tokens, credentials, or upstream identifiers.
type StreamProgress struct {
	CallID uint64
	Phase  string // waiting, receiving, finished
}

type streamProgressKey struct{}

var streamCallID atomic.Uint64

func WithStreamProgressObserver(ctx context.Context, observer func(StreamProgress)) context.Context {
	previous, _ := ctx.Value(streamProgressKey{}).(func(StreamProgress))
	return context.WithValue(ctx, streamProgressKey{}, func(p StreamProgress) {
		if previous != nil {
			previous(p)
		}
		if observer != nil {
			observer(p)
		}
	})
}

func observeStreamProgress(ctx context.Context, id uint64, phase string) {
	if observe, ok := ctx.Value(streamProgressKey{}).(func(StreamProgress)); ok {
		observe(StreamProgress{CallID: id, Phase: phase})
	}
}
