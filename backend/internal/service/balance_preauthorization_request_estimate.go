package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	DefaultBalancePreauthorizationInputTokens              = 500
	DefaultBalancePreauthorizationNonStreamingOutputWindow = 4096
	MaxBalancePreauthorizationOutputTokens                 = 8192
)

// BalancePreauthorizationTokenEstimate is derived entirely from the current
// request. It deliberately carries no historical usage dependency: reserve
// sizing must not turn every gateway request into an aggregate database query.
type BalancePreauthorizationTokenEstimate struct {
	InputTokens       int
	OutputTokens      int
	ImageInputTokens  int
	ImageOutputTokens int
	AudioInputTokens  int
}

// EstimateBalancePreauthorizationTokens follows the request-local pre-consume
// model used by mature API gateways: estimate prompt tokens before forwarding,
// include an explicit client output limit when present, and reconcile the
// reserve against authoritative provider usage after the request completes.
//
// Protocol-specific estimators already used by the count_tokens endpoints are
// preferred. The strict gateway entry point rejects unknown shapes; the legacy
// no-error helper keeps a bounded compatibility result for non-gateway callers.
func EstimateBalancePreauthorizationTokens(body []byte) BalancePreauthorizationTokenEstimate {
	estimate, err := EstimateBalancePreauthorizationTokensStrict(body, gjson.GetBytes(body, "stream").Bool())
	if err != nil {
		// Keep the legacy helper total for non-gateway callers. Gateway admission
		// uses the strict variant below and fails before selecting an account.
		return estimateBalancePreauthorizationTokensFallback(body, gjson.GetBytes(body, "stream").Bool())
	}
	return estimate
}

// Responses WebSocket streams even when its payload omits the HTTP stream flag.
func EstimateStreamingPreauthorizationTokens(body []byte) BalancePreauthorizationTokenEstimate {
	estimate, err := EstimateBalancePreauthorizationTokensStrict(body, true)
	if err != nil {
		return estimateBalancePreauthorizationTokensFallback(body, true)
	}
	return estimate
}

// EstimateBalancePreauthorizationTokensStrict only accepts request shapes for
// which the local protocol estimator has a defined meaning. Callers on the
// request path must use this method: guessing from serialized JSON size can
// reserve hundreds of times the actual usage.
func EstimateBalancePreauthorizationTokensStrict(body []byte, streaming bool) (BalancePreauthorizationTokenEstimate, error) {
	if len(body) == 0 || !json.Valid(body) {
		return BalancePreauthorizationTokenEstimate{}, fmt.Errorf("preauthorization token estimate: invalid JSON request")
	}
	root := gjson.ParseBytes(body)
	switch {
	case root.Get("contents").Exists() || root.Get("systemInstruction").Exists():
	case root.Get("input").Exists() || root.Get("instructions").Exists():
		if _, err := EstimateOpenAIResponsesInputTokens(body); err != nil {
			return BalancePreauthorizationTokenEstimate{}, err
		}
	case root.Get("messages").Exists():
		if _, err := EstimateAnthropicCountTokens(body); err != nil {
			return BalancePreauthorizationTokenEstimate{}, err
		}
	case root.Get("prompt").Exists():
		if root.Get("prompt").Type != gjson.String {
			return BalancePreauthorizationTokenEstimate{}, fmt.Errorf("preauthorization token estimate: prompt must be a string")
		}
	case root.Get("type").String() == "response.create" || root.Get("max_output_tokens").Exists() || root.Get("max_completion_tokens").Exists():
		// Responses WebSocket turns may intentionally omit input when they
		// continue a previous response or send an empty turn.
	default:
		return BalancePreauthorizationTokenEstimate{}, fmt.Errorf("preauthorization token estimate: unsupported request shape")
	}
	return estimateBalancePreauthorizationTokens(body, streaming), nil
}

func estimateBalancePreauthorizationTokens(body []byte, streaming bool) BalancePreauthorizationTokenEstimate {
	inputTokens := estimateBalancePreauthorizationInputTokens(body)
	if inputTokens < DefaultBalancePreauthorizationInputTokens {
		inputTokens = DefaultBalancePreauthorizationInputTokens
	}

	outputTokens := requestedBalancePreauthorizationOutputTokens(body)
	if outputTokens <= 0 {
		outputTokens = DefaultBalancePreauthorizationNonStreamingOutputWindow
		if streaming {
			outputTokens = DefaultBalancePreauthorizationOutputWindow
		}
	}
	if outputTokens > MaxBalancePreauthorizationOutputTokens {
		outputTokens = MaxBalancePreauthorizationOutputTokens
	}

	estimate := BalancePreauthorizationTokenEstimate{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	root := gjson.ParseBytes(body)
	for _, path := range []string{"contents", "messages", "input"} {
		if requestContainsAudio(root.Get(path)) {
			// Reserve input media, not MIME examples in tool definitions. The
			// reported modality partition replaces this conservative estimate.
			estimate.AudioInputTokens = inputTokens
			break
		}
	}
	if IsGPTImageGenerationModel(root.Get("model").String()) {
		count := root.Get("n").Int()
		if count < 1 {
			count = 1
		}
		perImage := imageOutputReservationTokens(root.Get("size").String())
		if count > int64(math.MaxInt/perImage) {
			estimate.ImageOutputTokens = math.MaxInt
		} else {
			estimate.ImageOutputTokens = int(count) * perImage
		}
		estimate.OutputTokens = estimate.ImageOutputTokens
	} else {
		for _, tool := range root.Get("tools").Array() {
			if tool.Get("type").String() == "image_generation" {
				estimate.ImageOutputTokens = imageOutputReservationTokens(tool.Get("size").String())
				break
			}
		}
	}
	return estimate
}

func requestContainsAudio(value gjson.Result) bool {
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(key, child gjson.Result) bool {
		if (key.Str == "mimeType" || key.Str == "mime_type" || key.Str == "media_type") && strings.HasPrefix(child.Str, "audio/") ||
			key.Str == "type" && (child.Str == "input_audio" || child.Str == "audio_url") {
			found = true
			return false
		}
		found = requestContainsAudio(child)
		return !found
	})
	return found
}

// GeminiImageReservationTokens uses official output token counts, not rounded
// dollar-per-image examples. Unknown sizes reserve the largest supported size.
func GeminiImageReservationTokens(model string, body []byte) int {
	if !isGeminiTokenImageModel(model) {
		return 0
	}
	size := gjson.GetBytes(body, "generationConfig.imageConfig.imageSize").String()
	if size == "" {
		size = gjson.GetBytes(body, "image_config.image_size").String()
	}
	switch normalizeModelNameForPricing(model) {
	case "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview":
		switch strings.ToUpper(size) {
		case "0.5K":
			return 747
		case "1K":
			return 1120
		case "2K":
			return 1680
		default:
			return 2520
		}
	case "gemini-2.5-flash-image":
		return 1290
	default:
		if size == "1K" || size == "2K" {
			return 1120
		}
		return 2000
	}
}

func estimateBalancePreauthorizationInputTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}

	root := gjson.ParseBytes(body)
	var estimated int
	var err error
	switch {
	case root.Get("contents").Exists() || root.Get("systemInstruction").Exists():
		estimated = estimateGeminiCountTokens(body)
	case root.Get("input").Exists() || root.Get("instructions").Exists():
		estimated, err = EstimateOpenAIResponsesInputTokens(body)
	case root.Get("messages").Exists():
		estimated, err = EstimateAnthropicCountTokens(body)
	case root.Get("prompt").Exists():
		estimated = estimateTokensForText(root.Get("prompt").String())
	}
	if err == nil && estimated > 0 {
		return estimated
	}

	// Unknown shapes are handled by the strict gateway path. Keep the helper's
	// compatibility result bounded for internal callers that cannot return an
	// error, without deriving tokens from the serialized JSON envelope.
	return DefaultBalancePreauthorizationInputTokens
}

func estimateBalancePreauthorizationTokensFallback(body []byte, streaming bool) BalancePreauthorizationTokenEstimate {
	output := DefaultBalancePreauthorizationNonStreamingOutputWindow
	if streaming {
		output = DefaultBalancePreauthorizationOutputWindow
	}
	return BalancePreauthorizationTokenEstimate{InputTokens: DefaultBalancePreauthorizationInputTokens, OutputTokens: output}
}

func requestedBalancePreauthorizationOutputTokens(body []byte) int {
	root := gjson.ParseBytes(body)
	paths := [...]string{
		"max_output_tokens",
		"max_completion_tokens",
		"max_tokens",
		"generationConfig.maxOutputTokens",
	}
	maximum := int64(0)
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() || value.Type != gjson.Number {
			continue
		}
		candidate := value.Int()
		if candidate > maximum {
			maximum = candidate
		}
	}
	if maximum <= 0 {
		return 0
	}
	if uint64(maximum) > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(maximum)
}
