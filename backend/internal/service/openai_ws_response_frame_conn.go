package service

import (
	"context"
	"encoding/json"
	"errors"

	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
)

// Dedicated passthrough sockets do not use the pool, but still carry multiple
// turns. Validate ownership below the relay's usage and account-health hooks.
type openAIWSResponseFrameConn struct {
	inner    openaiwsv2.FrameConn
	boundary openAIWSResponseBoundary
	pending  [][]byte
}

func (c *openAIWSResponseFrameConn) WriteFrame(ctx context.Context, kind coderws.MessageType, payload []byte) error {
	if (kind == coderws.MessageText || kind == coderws.MessageBinary) && isOpenAIWSResponseCreate(payload) {
		if err := c.boundary.begin(); err != nil {
			return err
		}
	}
	return c.inner.WriteFrame(ctx, kind, payload)
}

func (c *openAIWSResponseFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	for {
		readCtx := ctx
		var cancel context.CancelFunc
		if deadline := c.boundary.readDeadline(); !deadline.IsZero() {
			readCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		kind, payload, err := c.readFrame(readCtx)
		if cancel != nil {
			cancel()
		}
		if !errors.Is(err, errOpenAIWSUncorrelatedError) {
			if err != nil {
				c.boundary.retire()
			}
			return kind, payload, err
		}
	}
}

func (c *openAIWSResponseFrameConn) readFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if !c.boundary.canRead() {
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	if err := ctx.Err(); err != nil {
		return coderws.MessageText, nil, err
	}
	kind := coderws.MessageText
	var payload []byte
	if len(c.pending) > 0 {
		payload, c.pending = c.pending[0], c.pending[1:]
	} else {
		var err error
		kind, payload, err = c.inner.ReadFrame(ctx)
		if err != nil {
			return kind, payload, err
		}
		if documents, split := splitOpenAIConcatenatedJSONDocuments(payload); split {
			payload, c.pending = documents[0], documents[1:]
		}
	}
	if kind != coderws.MessageText || !json.Valid(payload) {
		c.boundary.retire()
		return kind, nil, errOpenAIWSResponseBoundary
	}
	eventType, _, _ := parseOpenAIWSEventEnvelope(payload)
	// These are connection-level acknowledgements, not response events.
	if eventType != "session.created" && eventType != "session.updated" {
		if err := c.boundary.observe(payload); err != nil {
			return kind, nil, err
		}
	}
	if isOpenAIWSTerminalEvent(eventType) && len(c.pending) > 0 {
		c.boundary.retire()
		c.pending = nil
	}
	return kind, payload, nil
}

func (c *openAIWSResponseFrameConn) Close() error { return c.inner.Close() }
