package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestAnswerKinds(t *testing.T) {
	tests := []struct {
		name, event string
		want        bool
	}{
		{"chat text", `data: {"choices":[{"delta":{"content":"hi"}}]}`, true},
		{"chat tool", `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"ls"}}]}}]}`, true},
		{"chat mixed reasoning and tool", `data: {"choices":[{"delta":{"reasoning_content":"hm","tool_calls":[{"index":0,"function":{"name":"ls"}}]}}]}`, true},
		{"chat reasoning", `data: {"choices":[{"delta":{"reasoning_content":"hm"}}]}`, false},
		{"chat role", `data: {"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"responses text", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}", true},
		{"responses function", `data: {"type":"response.output_item.added","item":{"type":"function_call","name":"ls"}}`, true},
		{"responses custom tool", `data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","name":"apply_patch"}}`, true},
		{"responses reasoning", `data: {"type":"response.reasoning_summary_text.delta","delta":"plan"}`, false},
		{"responses queued", `data: {"type":"response.queued"}`, false},
		{"anthropic text", `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`, true},
		{"anthropic tool", `data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"t","name":"ls","input":{}}}`, true},
		{"anthropic thinking", `data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hm"}}`, false},
		{"empty text", `data: {"type":"response.output_text.delta","delta":""}`, false},
		{"keepalive", `: keep-alive`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := answerKind([]byte(tt.event)); got != tt.want {
				t.Fatalf("answerKind() = %v, want %v for %s", got, tt.want, tt.event)
			}
		})
	}
}

func TestWholeAnswerKinds(t *testing.T) {
	tests := []struct {
		name  string
		proto provider.Protocol
		body  string
		want  bool
	}{
		{"chat reasoning", provider.Chat, `{"choices":[{"message":{"role":"assistant","content":null,"reasoning_content":"hm"},"finish_reason":"stop"}]}`, false},
		{"chat text", provider.Chat, `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, true},
		{"chat tool", provider.Chat, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"ls","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, true},
		{"responses reasoning", provider.Responses, `{"object":"response","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"hm"}]}]}`, false},
		{"responses text", provider.Responses, `{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`, true},
		{"anthropic thinking", provider.Anthropic, `{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"hm"}],"stop_reason":"end_turn"}`, false},
		{"anthropic tool", provider.Anthropic, `{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"t","name":"ls","input":{}}],"stop_reason":"tool_use"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wholeAnswer(tt.proto, []byte(tt.body)); got != tt.want {
				t.Fatalf("wholeAnswer() = %v, want %v for %s", got, tt.want, tt.body)
			}
		})
	}
}
