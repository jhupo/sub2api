package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

var ErrQuotaSummaryUnsupported = errors.New("quota summary endpoint is unavailable")

// Quota groups and windows are upstream identities, not inferred model families.
type QuotaSummary struct {
	Groups []QuotaGroup `json:"groups"`
}

type QuotaGroup struct {
	DisplayName string        `json:"displayName"`
	Buckets     []QuotaBucket `json:"buckets"`
}

type QuotaBucket struct {
	BucketID          string   `json:"bucketId"`
	DisplayName       string   `json:"displayName"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

func (b QuotaBucket) Valid() bool {
	return b.RemainingFraction != nil && !math.IsNaN(*b.RemainingFraction) &&
		!math.IsInf(*b.RemainingFraction, 0) && *b.RemainingFraction >= 0 && *b.RemainingFraction <= 1
}

func (c *Client) RetrieveUserQuotaSummary(ctx context.Context, token, project string, limit int64) (*QuotaSummary, error) {
	if c == nil || c.httpClient == nil || limit <= 0 {
		return nil, errors.New("quota summary client or response limit is invalid")
	}
	body, err := json.Marshal(FetchAvailableModelsRequest{Project: project})
	if err != nil {
		return nil, err
	}
	urls := CodeAssistBaseURLs()
	for index, baseURL := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1internal:retrieveUserQuotaSummary", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		ApplyCodeAssistRequestHeaders(req, ctx, token, "application/json")
		resp, err := c.fetchAvailableModelsHTTPClient().Do(req)
		if err != nil {
			if index+1 < len(urls) && shouldFallbackToNextURL(err, 0) {
				continue
			}
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(raw)) > limit {
			return nil, fmt.Errorf("quota summary response exceeds %d bytes", limit)
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
			if index+1 < len(urls) {
				continue
			}
			return nil, ErrQuotaSummaryUnsupported
		}
		if resp.StatusCode != http.StatusTooManyRequests && index+1 < len(urls) && shouldFallbackToNextURL(nil, resp.StatusCode) {
			continue
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, &ForbiddenError{StatusCode: resp.StatusCode, Body: string(raw)}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("quota summary failed (HTTP %d)", resp.StatusCode)
		}
		var summary QuotaSummary
		if err := json.Unmarshal(raw, &summary); err != nil {
			return nil, fmt.Errorf("decode quota summary: %w", err)
		}
		if summary.Groups == nil {
			return nil, errors.New("quota summary is missing groups")
		}
		for _, group := range summary.Groups {
			if group.Buckets == nil {
				return nil, errors.New("quota summary group is missing buckets")
			}
		}
		return &summary, nil
	}
	return nil, ErrQuotaSummaryUnsupported
}
