package usageadjustment

import (
	"context"
	"sync"
)

type Snapshot struct {
	Percent             int   `json:"percent"`
	OriginalCacheTokens int64 `json:"original_cache_tokens"`
	ShiftedTokens       int64 `json:"shifted_tokens"`
}

type recorderKey struct{}
type recordKey struct {
	accountID  int64
	responseID string
}
type recorder struct {
	mu      sync.Mutex
	records map[recordKey]*Snapshot
}

func CopyRecorder(parent, base context.Context) context.Context {
	if r, ok := parent.Value(recorderKey{}).(*recorder); ok {
		return context.WithValue(base, recorderKey{}, r)
	}
	return base
}

// WithRecorder keeps original usage with the request, including detached billing
// contexts. Entries are scoped by account and response to isolate retries/WS turns.
func WithRecorder(ctx context.Context) context.Context {
	if _, ok := ctx.Value(recorderKey{}).(*recorder); ok {
		return ctx
	}
	return context.WithValue(ctx, recorderKey{}, &recorder{records: make(map[recordKey]*Snapshot)})
}

func Observer(ctx context.Context, accountID int64, requestID string) func(string, Snapshot) {
	r, _ := ctx.Value(recorderKey{}).(*recorder)
	if r == nil {
		return nil
	}
	return func(responseID string, snapshot Snapshot) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.records) > 256 {
			clear(r.records)
		}
		r.records[recordKey{accountID, responseID}] = &snapshot
		if requestID != "" {
			r.records[recordKey{accountID, requestID}] = &snapshot
		}
	}
}

func Lookup(ctx context.Context, accountID int64, requestID string, cacheTokens int) *Snapshot {
	r, _ := ctx.Value(recorderKey{}).(*recorder)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.records[recordKey{accountID, requestID}]
	if s == nil {
		s = r.records[recordKey{accountID, ""}]
	}
	if s == nil || s.OriginalCacheTokens-s.ShiftedTokens != int64(cacheTokens) {
		return nil
	}
	copy := *s
	return &copy
}
