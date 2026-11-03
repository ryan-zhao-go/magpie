package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type answerStall struct {
	n int
}

func TestAnswerTimeoutRace(t *testing.T) {
	for range 20 {
		w := newHoldWriter(httptest.NewRecorder(), true)
		ctx, cancel := context.WithCancel(t.Context())
		w.stop = cancel
		w.streams, w.stream, w.status = true, true, http.StatusOK
		w.answerWait = 20 * time.Millisecond
		done := w.watch()
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"answer"}}]}` + "\n\n"))
		done()
		cancel()
		w.mu.Lock()
		answered, timedOut := w.answered, w.answerTimedOut
		w.mu.Unlock()
		if answered == timedOut {
			t.Fatalf("answered=%v answerTimedOut=%v", answered, timedOut)
		}
		<-ctx.Done()
	}
}

type protocolAnswerStall struct {
	body string
	n    int
}

func (s *protocolAnswerStall) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.n++
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, s.body)
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func stallOn(t *testing.T, id string, proto provider.Protocol, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}}
	if proto == provider.Responses {
		p.Responses = up.URL
	} else {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func (s *answerStall) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.n++
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, `data: {"id":"a","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n")
	io.WriteString(w, `data: {"id":"a","choices":[{"index":0,"delta":{"reasoning_content":"private reasoning"}}]}`+"\n\n")
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func TestGroupAnswerTimeoutFallsBack(t *testing.T) {
	fresh(t)
	a := &answerStall{}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, FirstToken: 1, AnswerTimeout: 1}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "private reasoning") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail != "answer_timeout" || r.Tries[0].Rest != nil ||
		!strings.HasSuffix(r.Tries[0].Error, "no answer text or tool call in 1s") || r.Tries[1].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	code, body = postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"again"}]}`)
	if code != 200 || !strings.Contains(body, "from b") || a.n != 2 || b.n != 2 || lastRoute(s).Tries[0].Fail != failAnswerTimeout {
		t.Fatalf("second: %d %s (a %d, b %d) tries=%+v", code, body, a.n, b.n, lastRoute(s).Tries)
	}
}

type delayedAnswer struct {
	first, last string
	wait        time.Duration
	n           int
}

type delayedJSONError struct {
	wait time.Duration
}

func (d delayedJSONError) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	select {
	case <-r.Context().Done():
		return
	case <-time.After(d.wait):
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	io.WriteString(w, `{"error":{"message":"bad input","type":"invalid_request_error"}}`)
}

func (s *delayedAnswer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.n++
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, s.first+"\n\n")
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
		return
	case <-time.After(s.wait):
	}
	io.WriteString(w, s.last+"\n\n")
}

func TestGroupAnswerTimeoutBoundaries(t *testing.T) {
	t.Run("last member is always waited for", func(t *testing.T) {
		fresh(t)
		a := &slowStart{wait: 1200 * time.Millisecond}
		slowOn(t, "a", a)
		provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m"}, Routing: provider.Ordered, AnswerTimeout: 1})
		code, body := postAs(t, New(), "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "slow answer") {
			t.Fatalf("%d %s", code, body)
		}
	})

	t.Run("zero keeps the old behavior", func(t *testing.T) {
		fresh(t)
		a := &slowStart{wait: 1200 * time.Millisecond}
		b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
		slowOn(t, "a", a)
		scriptedOn(t, "b", provider.Chat, b)
		provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered})
		code, body := postAs(t, New(), "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "slow answer") || b.n != 0 {
			t.Fatalf("%d %s (b %d)", code, body, b.n)
		}
	})

	t.Run("tool call starts the answer", func(t *testing.T) {
		fresh(t)
		a := &delayedAnswer{
			first: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"ls","arguments":""}}]}}]}`,
			last:  `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}\n\ndata: [DONE]`,
			wait:  1200 * time.Millisecond,
		}
		b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
		slowOn(t, "a", a)
		scriptedOn(t, "b", provider.Chat, b)
		provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, AnswerTimeout: 1})
		code, body := postAs(t, New(), "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, `"name":"ls"`) || b.n != 0 {
			t.Fatalf("%d %s (b %d)", code, body, b.n)
		}
	})

	t.Run("innermost group wins", func(t *testing.T) {
		fresh(t)
		a := &answerStall{}
		b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
		slowOn(t, "a", a)
		scriptedOn(t, "b", provider.Chat, b)
		provider.SaveGroup(provider.Group{Name: "In", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, AnswerTimeout: 1})
		provider.SaveGroup(provider.Group{Name: "G", Members: []string{"group/in"}, Routing: provider.Ordered, AnswerTimeout: 60})
		s := New()
		code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "from b") || !strings.HasSuffix(lastRoute(s).Tries[0].Error, "in 1s") {
			t.Fatalf("%d %s tries=%+v", code, body, lastRoute(s).Tries)
		}
	})
}

func TestGroupAnswerTimeoutWholeJSON(t *testing.T) {
	for _, tc := range []struct {
		name, first string
		fallback    bool
	}{
		{"reasoning only falls back", `{"choices":[{"message":{"role":"assistant","content":null,"reasoning_content":"private"},"finish_reason":"stop"}]}`, true},
		{"text answers", chatOK, false},
		{"tool answers", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"ls","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{200, "application/json", tc.first}}}
			b := &scripted{replies: []reply{{200, "application/json", chatOK}}}
			scriptedOn(t, "a", provider.Chat, a)
			scriptedOn(t, "b", provider.Chat, b)
			provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, AnswerTimeout: 1})
			s := New()
			code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || (b.n == 1) != tc.fallback || strings.Contains(body, "private") {
				t.Fatalf("%d %s (a %d b %d) tries=%+v", code, body, a.n, b.n, lastRoute(s).Tries)
			}
		})
	}
}

func TestGroupAnswerTimeoutNonStreamDoesNotKeepAlive(t *testing.T) {
	fresh(t)
	heldAfter, watchedEvery := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 20*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = heldAfter, watchedEvery })

	slowOn(t, "a", delayedJSONError{wait: 80 * time.Millisecond})
	b := &scripted{replies: []reply{{200, "application/json", chatOK}}}
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, AnswerTimeout: 1}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/g","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type %q, want JSON", got)
	}
	if strings.Contains(rec.Body.String(), ": keepalive") {
		t.Fatalf("non-stream response contains SSE keepalive: %q", rec.Body.String())
	}
	if b.n != 0 {
		t.Fatalf("request error unexpectedly fell back to b %d times", b.n)
	}
}

func TestAnswerTimeoutStreamingKeepalive(t *testing.T) {
	rec := httptest.NewRecorder()
	h := newHoldWriter(rec, true)
	h.streams = true
	h.answerWait = time.Second
	h.alive = &keptAlive{
		proto:   provider.Chat,
		streams: true,
		at:      time.Now().Add(-10 * time.Second),
	}
	h.keepAlive()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), ": keepalive") {
		t.Fatalf("streaming keepalive: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestGroupAnswerTimeoutProtocolReasoning(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto provider.Protocol
		stall string
	}{
		{"responses summary", provider.Responses, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r","status":"in_progress","output":[]}}`,
			`event: response.reasoning_summary_text.delta`+"\n"+`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs","output_index":0,"summary_index":0,"delta":"private reasoning"}`)},
		{"anthropic thinking", provider.Anthropic, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private reasoning"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &protocolAnswerStall{body: tc.stall}
			b := &scripted{replies: []reply{goodReply(tc.proto, true)}}
			stallOn(t, "a", tc.proto, a)
			scriptedOn(t, "b", tc.proto, b)
			provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, AnswerTimeout: 1})
			s := New()
			rec := askOn(s, tc.proto, "group/g", true)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from b") || strings.Contains(rec.Body.String(), "private reasoning") ||
				lastRoute(s).Tries[0].Fail != failAnswerTimeout || lastRoute(s).Tries[0].Rest != nil {
				t.Fatalf("%d %s tries=%+v", rec.Code, rec.Body.String(), lastRoute(s).Tries)
			}
		})
	}
}

func TestGroupAnswerTimeoutClientCancellationWins(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "", chatOK}}, hang: make(chan struct{}), hanging: make(chan struct{})}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, AnswerTimeout: 1})
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		select {
		case <-a.hanging:
			cancel()
		case <-served:
		}
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
		close(a.hang)
	}()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx))
	close(served)
	r := lastRoute(s)
	if b.n != 0 || len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Rest != nil {
		t.Fatalf("b tried %d, tries %+v", b.n, r.Tries)
	}
}
