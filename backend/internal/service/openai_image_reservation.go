package service

import (
	"math"
	"strconv"
	"strings"
)

// This is a reservation budget, never reported as provider usage. Auto size
// reserves a 2K image; final settlement uses the provider's actual token counts.
func imageOutputReservationTokens(size string) int {
	w, h := 2048, 2048
	if parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x"); len(parts) == 2 {
		width, e1 := strconv.Atoi(parts[0])
		height, e2 := strconv.Atoi(parts[1])
		if e1 == nil && e2 == nil && width > 0 && height > 0 && width <= 8192 && height <= 8192 {
			w, h = width, height
		}
	}
	return ((w+15)/16)*((h+15)/16) + 256
}

func (r *OpenAIImagesRequest) PreauthorizationEstimate() PerRequestPreauthorizationEstimate {
	count := r.N
	if count < 1 {
		count = 1
	}
	outputTokens := imageOutputReservationTokens(r.Size)
	if count > math.MaxInt/outputTokens {
		outputTokens = math.MaxInt
	} else {
		outputTokens *= count
	}
	// Reserve each edit input at the same 2K budget when its dimensions are not
	// available locally. Do not download remote URLs or count encoded bytes.
	imageTokens := len(r.InputImageURLs) * imageOutputReservationTokens("auto")
	for _, upload := range r.Uploads {
		imageTokens += imageOutputReservationTokens(strconv.Itoa(upload.Width) + "x" + strconv.Itoa(upload.Height))
	}
	if r.MaskUpload != nil {
		imageTokens += imageOutputReservationTokens(strconv.Itoa(r.MaskUpload.Width) + "x" + strconv.Itoa(r.MaskUpload.Height))
	} else if r.MaskImageURL != "" {
		imageTokens += imageOutputReservationTokens("auto")
	}
	return PerRequestPreauthorizationEstimate{
		RequestCount: count, SizeTier: r.SizeTier,
		Tokens: UsageTokens{
			InputTokens:       len([]byte(r.Prompt)) + imageTokens,
			ImageInputTokens:  imageTokens,
			OutputTokens:      outputTokens,
			ImageOutputTokens: outputTokens,
		},
	}
}

// Image output has a separate reservation budget. Exclude its encoded payload
// from text-window top-ups, including images embedded in terminal Responses.
func openAIStreamTextReservationBytes(frame openAISSEDataFrame) int {
	if strings.HasPrefix(frame.eventType, "response.image_generation_call.") ||
		frame.root.Get("item.type").String() == "image_generation_call" {
		return 0
	}
	n := len(frame.trimmed)
	for _, item := range frame.root.Get("response.output").Array() {
		if item.Get("type").String() == "image_generation_call" {
			n -= len(item.Raw)
		}
	}
	return max(n, 0)
}
