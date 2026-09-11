package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImageReservationExcludesEncodedOutputFromTextTopUps(t *testing.T) {
	encoded := strings.Repeat("a", 10000)
	for _, data := range []string{
		`{"type":"response.image_generation_call.partial_image","partial_image_b64":"` + encoded + `"}`,
		`{"type":"response.output_item.done","item":{"type":"image_generation_call","result":"` + encoded + `"}}`,
	} {
		require.Zero(t, openAIStreamTextReservationBytes(parseOpenAISSEDataFrame([]byte(data), "")))
	}
	data := `{"type":"response.completed","response":{"output":[{"type":"image_generation_call","result":"` + encoded + `"},{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`
	n := openAIStreamTextReservationBytes(parseOpenAISSEDataFrame([]byte(data), ""))
	require.Greater(t, n, 0)
	require.Less(t, n, 300)
}

func TestOpenAIImagesUsageOutputClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		images     int
	}{
		{"image_only", `{"usage":{"input_tokens":50,"input_tokens_details":{"text_tokens":10,"image_tokens":40},"output_tokens":100}}`, 100},
		{"explicit_split", `{"usage":{"input_tokens":50,"output_tokens":100,"output_tokens_details":{"image_tokens":80,"text_tokens":20}}}`, 80},
		{"explicit_zero", `{"usage":{"input_tokens":50,"output_tokens":100,"output_tokens_details":{"image_tokens":0,"text_tokens":100}}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, ok := extractOpenAIImagesUsageFromJSONBytes([]byte(tc.body))
			require.True(t, ok)
			require.Equal(t, tc.images, usage.ImageOutputTokens)
			var streamed OpenAIUsage
			mergeOpenAIUsage(&streamed, []byte(tc.body))
			require.Equal(t, usage, streamed)
			tool, ok := openAIImagesToolUsageFromGJSON(gjson.Get(tc.body, "usage"))
			require.True(t, ok)
			require.Equal(t, usage, tool)
		})
	}
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":50,"output_tokens":100}}`))
	require.True(t, ok)
	require.Zero(t, usage.ImageOutputTokens, "text Responses must never inherit Images normalization")
}

func TestOpenAIImageReservationUsesFinalPricingAndParsedImageDimensions(t *testing.T) {
	billing := &BillingService{}
	svc := &BalancePreauthorizationService{costCalculator: billing}
	parsed := &OpenAIImagesRequest{N: 2, Size: "1024x1024", SizeTier: "1K", Prompt: "draw",
		Uploads: []OpenAIImagesUpload{{Width: 512, Height: 512}}}
	estimate := parsed.PreauthorizationEstimate()
	require.Equal(t, imageOutputReservationTokens("512x512"), estimate.Tokens.ImageInputTokens)
	request := BalancePreauthorizationRequest{
		EstimateKind:       PreauthorizationEstimatePerRequest,
		PerRequestEstimate: estimate,
		CostInput: CostInput{Model: "custom-image-name", RateMultiplier: 2, Resolver: &ModelPricingResolver{},
			Resolved: &ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{
				InputPricePerToken: 5e-6, ImageInputPricePerToken: 8e-6, ImageOutputPricePerToken: 30e-6,
			}}},
	}
	for _, funding := range []int8{BillingTypeBalance, BillingTypeSubscription} {
		request.BillingType = funding
		hold, err := svc.estimateHold(context.Background(), request)
		require.NoError(t, err)
		actual, err := billing.CalculateCostUnified(CostInput{Model: request.CostInput.Model,
			RateMultiplier: 2, Resolver: &ModelPricingResolver{}, Resolved: request.CostInput.Resolved, Tokens: estimate.Tokens})
		require.NoError(t, err)
		require.Equal(t, quantizeBillingHoldUpFromFloat(actual.ActualCost), hold.HoldAmount)
		require.Greater(t, hold.HoldAmount, 0.0)
		require.Zero(t, hold.OutputUnitPrice, "encoded image bytes cannot drive text top-ups")
	}
	request.CostInput.Resolved = &ResolvedPricing{Mode: BillingModeImage, DefaultPerRequestPrice: 0.25}
	hold, err := svc.estimateHold(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, 1.0, hold.HoldAmount, "two images at configured price and multiplier")
}

func TestOpenAIImageReservationMixedResponseKeepsTextMarginalPrice(t *testing.T) {
	svc := &BalancePreauthorizationService{costCalculator: &BillingService{}}
	request := BalancePreauthorizationRequest{
		EstimatedInputTokens: 100, InitialOutputWindowTokens: 200, EstimatedImageOutputTokens: 500,
		CostInput: CostInput{Model: "gpt-5.6-sol", RateMultiplier: 2, Resolver: &ModelPricingResolver{},
			Resolved: &ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{
				InputPricePerToken: 5e-6, OutputPricePerToken: 10e-6, ImageOutputPricePerToken: 30e-6,
			}}},
	}
	hold, err := svc.estimateHold(context.Background(), request)
	require.NoError(t, err)
	require.InDelta(t, 20e-6, hold.OutputUnitPrice, 1e-12)
	require.Equal(t, 200, hold.OutputWindow)
	// Ordinary input plus independent text and image output budgets.
	require.InDelta(t, 0.035, hold.HoldAmount, 2e-8)
}
