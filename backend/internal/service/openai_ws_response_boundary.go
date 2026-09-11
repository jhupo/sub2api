package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

var errOpenAIWSResponseBoundary = errors.New("upstream websocket response ownership is ambiguous")
var errOpenAIWSInvalidEventJSON = errors.New("upstream websocket returned malformed Responses event JSON")
var errOpenAIWSUncorrelatedError = errors.New("upstream websocket error requires a correlated terminal event")
var errOpenAIWSUnownedTurnStart = fmt.Errorf("%w: uncorrelated error before response start", errOpenAIWSResponseBoundary)

const openAIWSErrorCorrelationTimeout = 5 * time.Second

// A response ID belongs to one turn for the entire socket lifetime. Never evict
// old IDs from this set: retire the socket at the bound instead of forgetting
// which user's response a late frame belongs to.
const openAIWSResponseHistoryLimit = 4096

type openAIWSResponseBoundary struct {
	mu            sync.Mutex
	seen          map[string]struct{}
	current       string
	active        bool
	retired       bool
	pendingError  bool
	errorDeadline time.Time
}

func (b *openAIWSResponseBoundary) begin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.retired || b.active || len(b.seen) >= openAIWSResponseHistoryLimit {
		return errOpenAIWSResponseBoundary
	}
	b.current = ""
	b.active = true
	return nil
}

// Called before model observation, usage accounting or downstream delivery.
// ID-less deltas are valid within an established response, not at a reused
// socket's turn boundary. An uncorrelated error on a reused socket cannot be
// attributed to the new request or used to penalize its account/model.
func (b *openAIWSResponseBoundary) observe(message []byte) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer func() {
		if err != nil && !errors.Is(err, errOpenAIWSUncorrelatedError) {
			b.retired = true
			b.pendingError = false
		}
	}()
	if !b.active || (b.retired && !b.pendingError) {
		return errOpenAIWSResponseBoundary
	}
	if !json.Valid(message) {
		return errOpenAIWSInvalidEventJSON
	}
	eventType, responseID, _ := parseOpenAIWSEventEnvelope(message)
	if eventType == "" {
		return errOpenAIWSResponseBoundary
	}
	if b.pendingError && eventType != "response.failed" && eventType != "error" {
		return errOpenAIWSResponseBoundary
	}
	if responseID != "" {
		if b.current == "" {
			if _, stale := b.seen[responseID]; stale {
				return errOpenAIWSResponseBoundary
			}
			b.current = responseID
		} else if b.current != responseID {
			return errOpenAIWSResponseBoundary
		}
	} else if len(b.seen) > 0 {
		if eventType == "error" && b.current == "" {
			return errOpenAIWSUnownedTurnStart
		}
		if eventType == "error" && b.current != "" {
			// Do not expose or attribute this envelope. Retire the connection,
			// but allow only the current response's authoritative failed terminal.
			b.retired = true
			b.pendingError = true
			b.startErrorDeadline()
			return errOpenAIWSUncorrelatedError
		}
		if b.current == "" || isOpenAIWSTerminalEvent(eventType) {
			return errOpenAIWSResponseBoundary
		}
	}
	if eventType == "error" {
		// Never reuse this socket, but let the dedicated relay read the same
		// turn's authoritative response.failed usage after a bare error.
		b.retired = true
		b.pendingError = true
		b.startErrorDeadline()
		return nil
	}
	if isOpenAIWSTerminalEvent(eventType) {
		b.active = false
		b.pendingError = false
		if b.current != "" {
			if b.seen == nil {
				b.seen = make(map[string]struct{})
			}
			b.seen[b.current] = struct{}{}
		}
		b.retired = eventType != "response.completed" || b.current == "" || len(b.seen) >= openAIWSResponseHistoryLimit
	}
	return nil
}

// Called under mu. Repeated errors must not extend the drain budget.
func (b *openAIWSResponseBoundary) startErrorDeadline() {
	if b.errorDeadline.IsZero() {
		b.errorDeadline = time.Now().Add(openAIWSErrorCorrelationTimeout)
	}
}

func (b *openAIWSResponseBoundary) readDeadline() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.errorDeadline
}

func (b *openAIWSResponseBoundary) retire() {
	b.mu.Lock()
	b.retired = true
	b.pendingError = false
	b.mu.Unlock()
}

func (b *openAIWSResponseBoundary) canRead() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.retired || b.pendingError
}

func (b *openAIWSResponseBoundary) reusable() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.active && !b.retired
}

func isOpenAIWSResponseCreate(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		return v["type"] == "response.create"
	case []byte:
		return gjson.GetBytes(v, "type").String() == "response.create"
	case json.RawMessage:
		return gjson.GetBytes(v, "type").String() == "response.create"
	}
	return false
}
