package service

import (
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	DefaultBalancePreauthorizationInputTokens              = 500
	DefaultBalancePreauthorizationNonStreamingOutputWindow = 4096
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
// preferred. If a request shape cannot be estimated, raw bytes remain the
// conservative upper bound. The minimum protects small requests without any
// database lookup; it is a reserve only and is never written as actual usage.
func EstimateBalancePreauthorizationTokens(body []byte) BalancePreauthorizationTokenEstimate {
	return estimateBalancePreauthorizationTokens(body, gjson.GetBytes(body, "stream").Bool())
}

// Responses WebSocket streams even when its payload omits the HTTP stream flag.
func EstimateStreamingPreauthorizationTokens(body []byte) BalancePreauthorizationTokenEstimate {
	return estimateBalancePreauthorizationTokens(body, true)
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
	case root.Get("input").Exists():
		estimated, err = EstimateOpenAIResponsesInputTokens(body)
	case root.Get("messages").Exists():
		estimated, err = EstimateAnthropicCountTokens(body)
	}
	if err == nil && estimated > 0 {
		return estimated
	}

	// One token cannot encode fewer than one source byte, so this fallback is
	// conservative even for an unfamiliar or newly introduced request shape.
	return len(body)
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
