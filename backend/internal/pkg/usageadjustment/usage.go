package usageadjustment

import (
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Rewriter converts cache reads into ordinary input without changing total tokens.
// Each instance belongs to one upstream response, including all its SSE events.
type Rewriter struct {
	Percent            int
	InputIncludesCache bool
	input              int64
	hasInput           bool
	cached             int64
	hasCache           bool
}

func (r *Rewriter) Rewrite(body []byte) []byte {
	if r.Percent <= 0 || r.Percent > 100 || !gjson.ValidBytes(body) {
		return body
	}
	root := gjson.ParseBytes(body)
	if root.IsArray() {
		for i, item := range root.Array() {
			body = r.rewriteObject(body, item, strconv.Itoa(i)+".")
		}
		return body
	}
	return r.rewriteObject(body, root, "")
}

func (r *Rewriter) rewriteObject(body []byte, root gjson.Result, prefix string) []byte {
	for _, path := range []string{"usage", "message.usage", "response.usage"} {
		u := root.Get(path)
		if !u.IsObject() {
			continue
		}
		base := prefix + path + "."
		eventType := root.Get("type").String()
		inclusive := r.InputIncludesCache || path == "response.usage" || u.Get("prompt_tokens").Exists() ||
			u.Get("input_tokens_details").Exists() || root.Get("object").Exists() || strings.HasPrefix(eventType, "response.")
		// Anthropic input_tokens excludes cache reads; OpenAI totals include them.
		if !inclusive {
			if eventType == "message_start" {
				r.input, r.cached, r.hasInput, r.hasCache = 0, 0, false, false
			}
			_, inputPresent := tokenCount(u.Get("input_tokens"))
			if n, ok := tokenCount(u.Get("input_tokens")); ok {
				r.input, r.hasInput = n, true
			}
			cachePresent := false
			cached := int64(0)
			for _, key := range []string{"cache_read_input_tokens", "cache_read_tokens", "cached_tokens"} {
				if n, ok := tokenCount(u.Get(key)); ok {
					cachePresent = true
					if n > 0 {
						cached = n
						break
					}
				}
			}
			if cachePresent {
				r.cached, r.hasCache = cached, true
			}
			if r.hasInput && r.hasCache && r.cached > 0 && (cachePresent || inputPresent) {
				shift := r.shift(r.cached)
				if r.input > math.MaxInt64-shift {
					continue
				}
				body = set(body, base+"input_tokens", r.input+shift)
				if !cachePresent {
					body = set(body, base+"cache_read_input_tokens", r.cached-shift)
				}
			} else if cachePresent && cached > 0 {
				// A standalone incomplete Anthropic usage cannot conserve input yet.
				continue
			}
		}
		// Keep compatibility aliases in sync, including a 100% conversion to zero.
		for _, key := range []string{"cache_read_input_tokens", "cache_read_tokens", "cached_tokens", "input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "prompt_cache_hit_tokens"} {
			if cached, ok := tokenCount(u.Get(key)); ok && cached > 0 {
				shift := r.shift(cached)
				body = set(body, base+key, cached-shift)
				if strings.HasSuffix(key, ".cached_tokens") {
					detailsPath := strings.TrimSuffix(key, "cached_tokens") + "cached_tokens_details"
					body = r.rewriteBreakdown(body, base+detailsPath, u.Get(detailsPath), cached)
				}
				if key == "prompt_cache_hit_tokens" {
					if miss, ok := tokenCount(u.Get("prompt_cache_miss_tokens")); ok && miss <= math.MaxInt64-shift {
						body = set(body, base+"prompt_cache_miss_tokens", miss+shift)
					}
				}
			}
		}
	}
	for _, path := range []string{"usageMetadata", "response.usageMetadata"} {
		if cached, ok := tokenCount(root.Get(path + ".cachedContentTokenCount")); ok && cached > 0 {
			body = set(body, prefix+path+".cachedContentTokenCount", cached-r.shift(cached))
			body = r.rewriteBreakdown(body, prefix+path+".cacheTokensDetails", root.Get(path+".cacheTokensDetails"), cached)
		}
	}
	return body
}

func (r *Rewriter) rewriteBreakdown(body []byte, path string, details gjson.Result, total int64) []byte {
	type counter struct {
		path           string
		value, removed int64
	}
	var counters []counter
	if details.IsArray() {
		for i, entry := range details.Array() {
			if n, ok := tokenCount(entry.Get("tokenCount")); ok {
				counters = append(counters, counter{path: path + "." + strconv.Itoa(i) + ".tokenCount", value: n, removed: r.shift(n)})
			}
		}
	} else if details.IsObject() {
		for _, key := range []string{"text_tokens", "audio_tokens", "image_tokens"} {
			if n, ok := tokenCount(details.Get(key)); ok {
				counters = append(counters, counter{path: path + "." + key, value: n, removed: r.shift(n)})
			}
		}
	}
	var sum, removed int64
	for _, c := range counters {
		if sum > math.MaxInt64-c.value {
			return body
		}
		sum += c.value
		removed += c.removed
	}
	// Allocate integer rounding remainders so complete modality breakdowns still
	// add up to the adjusted aggregate cache count.
	remainder := int64(0)
	if sum == total {
		remainder = r.shift(total) - removed
	}
	for _, c := range counters {
		if remainder > 0 && c.value > c.removed {
			c.removed++
			remainder--
		}
		body = set(body, c.path, c.value-c.removed)
	}
	return body
}

func (r *Rewriter) shift(cached int64) int64 {
	// Integer arithmetic avoids floating point drift and overflow. Round down.
	return cached/100*int64(r.Percent) + cached%100*int64(r.Percent)/100
}

func tokenCount(value gjson.Result) (int64, bool) {
	if value.Type != gjson.Number {
		return 0, false
	}
	n, err := strconv.ParseInt(value.Raw, 10, 64)
	return n, err == nil && n >= 0
}

func set(body []byte, path string, n int64) []byte {
	next, err := sjson.SetBytes(body, path, n)
	if err != nil {
		return body
	}
	return next
}
