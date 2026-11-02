package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGroupAnswerTimeoutSaved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: "g", Name: "G", Members: []string{"a/m"}, AnswerTimeout: -7}); err != nil {
		t.Fatal(err)
	}
	if got := mustGroup(t, "g").AnswerTimeout; got != 0 {
		t.Fatalf("negative timeout = %d", got)
	}
	if err := SaveGroup(Group{ID: "g", Name: "G", Members: []string{"a/m"}, AnswerTimeout: 60}); err != nil {
		t.Fatal(err)
	}
	g := mustGroup(t, "g")
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if g.AnswerTimeout != 60 || !strings.Contains(string(raw), `"answerTimeout":60`) {
		t.Fatalf("group %+v JSON %s", g, raw)
	}
}
