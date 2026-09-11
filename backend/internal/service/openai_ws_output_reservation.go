package service

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

type openAIWSOutputItemBudget struct {
	fields        map[string]int
	streamBytes   int
	completeBytes int
}

// OpenAIWSOutputReservation retains numeric counters, not model output. The
// owning turn serializes observations. Done-only providers are supported while
// the same text repeated in item.done/response.completed is counted once.
type OpenAIWSOutputReservation struct {
	items              map[int]*openAIWSOutputItemBudget
	bytes              int
	terminalBytes      int
	measuredTextTokens int
}

func (b *OpenAIWSOutputReservation) UpperBound(payload []byte) int {
	root := gjson.ParseBytes(payload)
	if root.Get("response.usage").Exists() || root.Get("usage").Exists() {
		if usage, ok := extractOpenAIUsageFromJSONBytes(payload); ok {
			// Image output already has a separate initial budget. Invisible
			// reasoning remains part of the measured text-output floor.
			b.measuredTextTokens = max(b.measuredTextTokens, usage.OutputTokens-usage.ImageOutputTokens)
		}
	}
	event := root.Get("type").String()
	index := int(root.Get("output_index").Int())
	var field, value string
	delta := strings.HasSuffix(event, ".delta")
	base := strings.TrimSuffix(strings.TrimSuffix(event, ".delta"), ".done")
	switch base {
	case "response.output_text", "response.reasoning_text", "response.reasoning_summary_text":
		field = "text"
	case "response.refusal":
		field = "refusal"
	case "response.function_call_arguments":
		field = "arguments"
	case "response.custom_tool_call_input":
		field = "input"
	}
	if field != "" && (delta || strings.HasSuffix(event, ".done")) {
		if delta {
			value = root.Get("delta").String()
		} else {
			value = root.Get(field).String()
		}
		item := b.item(index)
		before := max(item.streamBytes, item.completeBytes)
		key := base + ":" + strconv.FormatInt(root.Get("content_index").Int(), 10) + ":" + strconv.FormatInt(root.Get("summary_index").Int(), 10)
		old := item.fields[key]
		next := max(old, len(value))
		if delta {
			next = old + len(value)
		}
		item.fields[key] = next
		item.streamBytes += next - old
		b.bytes += max(item.streamBytes, item.completeBytes) - before
	} else if event == "response.output_item.done" {
		b.complete(index, openAIWSCompletedItemBytes(root.Get("item")))
	} else if event == "response.completed" || event == "response.failed" || event == "response.incomplete" {
		total := 0
		for _, item := range root.Get("response.output").Array() {
			total += openAIWSCompletedItemBytes(item)
		}
		b.terminalBytes = max(b.terminalBytes, total)
	}
	return max(max(b.bytes, b.terminalBytes), b.measuredTextTokens)
}

func (b *OpenAIWSOutputReservation) item(index int) *openAIWSOutputItemBudget {
	if b.items == nil {
		b.items = make(map[int]*openAIWSOutputItemBudget)
	}
	if b.items[index] == nil {
		b.items[index] = &openAIWSOutputItemBudget{fields: make(map[string]int)}
	}
	return b.items[index]
}

func (b *OpenAIWSOutputReservation) complete(index, bytes int) {
	item := b.item(index)
	before := max(item.streamBytes, item.completeBytes)
	item.completeBytes = max(item.completeBytes, bytes)
	b.bytes += max(item.streamBytes, item.completeBytes) - before
}

func openAIWSCompletedItemBytes(item gjson.Result) int {
	switch item.Get("type").String() {
	case "function_call":
		return len(item.Get("arguments").String())
	case "custom_tool_call":
		return len(item.Get("input").String())
	case "message", "reasoning":
		total := 0
		for _, field := range []string{"content", "summary"} {
			for _, part := range item.Get(field).Array() {
				total += len(part.Get("text").String()) + len(part.Get("refusal").String())
			}
		}
		return total
	default:
		return 0
	}
}
