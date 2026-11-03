package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// AIGW answers this as a 400; a remote magpie can wrap it in a 502
// response.failed. Both must recover before the agent sees the failure.
func TestResponsesMissingReasoningRecovery(t *testing.T) {
	for _, shape := range []string{"http400", "http502", "stream502", "http400bare"} {
		t.Run(shape, func(t *testing.T) {
			fresh(t)
			var mu sync.Mutex
			var inputs [][]map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var q struct {
					Input []map[string]any `json:"input"`
				}
				json.NewDecoder(r.Body).Decode(&q)
				mu.Lock()
				inputs = append(inputs, q.Input)
				mu.Unlock()
				for _, it := range q.Input {
					if it["type"] != "reasoning" {
						continue
					}
					msg := "AIGW: Item with id 'rs_missing' not found. Items are not persisted when store is set to false."
					if shape == "stream502" {
						w.Header().Set("Content-Type", "text/event-stream")
						b, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"id": "resp_failed", "status": "failed", "error": map[string]any{"code": "server_error", "message": msg}}})
						io.WriteString(w, "event: response.failed\ndata: "+string(b)+"\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						status := 400
						if shape == "http502" {
							status = 502
						}
						w.WriteHeader(status)
						json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"recovered"}`, `data: {"type":"response.completed","response":{"id":"resp_ok","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}}`))
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-5.4"}, Responses: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"relay/gpt-5.4","stream":true,"store":false,"input":[{"type":"message","role":"user","content":"continue"},{"type":"reasoning","id":"rs_missing","summary":[],"encrypted_content":"sealed-old"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}],"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`
			if shape == "http400bare" {
				body = strings.ReplaceAll(body, `,"encrypted_content":"sealed-old"`, "")
			}
			s := New()
			for i := 0; i < 2; i++ {
				rec := codexPostTo(s, body)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "recovered") || strings.Contains(rec.Body.String(), "not found") {
					t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(inputs) != 3 {
				t.Fatalf("got %d requests, want original, cleaned retry, then cleaned next turn", len(inputs))
			}
			for _, input := range inputs[1:] {
				if len(reasoningIDs(input)) != 0 {
					t.Fatalf("reasoning still sent: %v", reasoningIDs(input))
				}
				k := kinds(input)
				if k["message"] != 1 || k["function_call"] != 1 || k["function_call_output"] != 1 {
					t.Fatalf("conversation lost: %v", k)
				}
			}
		})
	}
}

func TestResponsesMissingReasoningRecoveryBounded(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		want     int
	}{
		{"cleaned request refused", "rs_missing", 2},
		{"unknown reasoning id", "rs_other", 1},
		{"missing call", "fc_1", 1},
		{"missing compaction", "cmp_1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "Item with id '" + tc.id + "' not found."}})
			u := &scripted{replies: []reply{{400, "application/json", string(b)}}}
			scriptedOn(t, "relay", provider.Responses, u)
			body := `{"model":"relay/m","stream":true,"store":false,"input":[{"type":"message","role":"user","content":"continue"},{"type":"reasoning","id":"rs_missing","summary":[],"encrypted_content":"sealed-old"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"},{"type":"compaction","id":"cmp_1","encrypted_content":"sealed-summary"}],"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`
			rec := codexPostTo(New(), body)
			if rec.Code != 400 || u.n != tc.want {
				t.Fatalf("got %d asks and status %d, want %d and 400: %s", u.n, rec.Code, tc.want, rec.Body)
			}
		})
	}
}
