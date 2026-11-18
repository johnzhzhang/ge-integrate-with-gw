package geminienterprise

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func TestModelID(t *testing.T) {
	for in, want := range map[string]string{
		"gemini-3.8-flash":           "gemini-3.8-flash",
		"gemini-3.8-flash-low":       "gemini-3.8-flash",
		"models/gemini-4-argon-high": "gemini-4-argon",
	} {
		if got := modelID(in); got != want {
			t.Errorf("modelID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBuildQueryText(t *testing.T) {
	single := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "你好"}}}
	if got := buildQueryText(single); got != "你好" {
		t.Errorf("single message should be sent as-is, got %q", got)
	}
	multi := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "system", Content: "S"}, {Role: "user", Content: "U1"},
			{Role: "assistant", Content: "A1"}, {Role: "user", Content: "U2"},
		},
		ResponseFormat: &dto.ResponseFormat{Type: "json_schema", JsonSchema: json.RawMessage(`{"name":"x","schema":{"type":"object"}}`)},
	}
	got := buildQueryText(multi)
	for _, want := range []string{"【系统指令】\nS", "【用户】\nU1", "【助手】\nA1", "【本次用户消息】\nU2", `{"type":"object"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("query text missing %q:\n%s", want, got)
		}
	}
}

func TestStripFence(t *testing.T) {
	if got := stripFence("```json\n{\"a\":1}\n```\n"); got != `{"a":1}` {
		t.Errorf("stripFence got %q", got)
	}
	if got := stripFence(`{"a":1}`); got != `{"a":1}` {
		t.Errorf("stripFence changed plain JSON: %q", got)
	}
}

func TestIterateAndTexts(t *testing.T) {
	body := `[{"answer":{"state":"IN_PROGRESS","replies":[
	   {"groundedContent":{"content":{"role":"model","text":"想一想","thought":true}}},
	   {"groundedContent":{"content":{"role":"model","text":"杭州"}}}]}},
	 {"answer":{"state":"IN_PROGRESS","replies":[
	   {"groundedContent":{"content":{"role":"model","text":"很美\n\n"}}},
	   {"groundedContent":{"content":{"role":"model","text":"[]","mimeType":"application/json+suggestions"}}}]}},
	 {"answer":{"state":"SUCCEEDED"},"sessionInfo":{"session":"s"}}]`
	var content, thought string
	err := iterate(strings.NewReader(body), func(el *AssistResponse) error {
		c, th := texts(el)
		content += c
		thought += th
		return nil
	})
	if err != nil || content != "杭州很美\n\n" || thought != "想一想" {
		t.Errorf("err=%v content=%q thought=%q", err, content, thought)
	}
	if err := iterate(strings.NewReader(`[{"error":{"code":400}}]`), func(*AssistResponse) error { return nil }); err == nil {
		t.Error("expected error element to fail")
	}
}
