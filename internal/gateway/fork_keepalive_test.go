package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestHeldKeepaliveRespectsWholeAndClientStream(test *testing.T) {
	for _, scenario := range []struct {
		name          string
		streams       bool
		whole         bool
		wantKeepalive bool
	}{
		{name: "client stream", streams: true, wantKeepalive: true},
		{name: "whole summary", streams: true, whole: true},
		{name: "nonstream client"},
	} {
		test.Run(scenario.name, func(subtest *testing.T) {
			recorder := httptest.NewRecorder()
			alive := &keptAlive{proto: provider.Responses, streams: scenario.streams, whole: scenario.whole}
			held := newHoldWriter(recorder, true)
			held.streams, held.alive = scenario.streams, alive
			held.wantWhole = scenario.whole
			held.keepAlive()
			if scenario.wantKeepalive {
				if !alive.sent || recorder.Body.String() != ": keepalive\n" {
					subtest.Fatalf("client stream received no keepalive: sent=%v body=%q", alive.sent, recorder.Body.String())
				}
			} else if alive.sent || recorder.Body.Len() != 0 || recorder.Header().Get("Content-Type") != "" {
				subtest.Fatalf("keepalive reached %s: sent=%v body=%q", scenario.name, alive.sent, recorder.Body.String())
			}
		})
	}
}
