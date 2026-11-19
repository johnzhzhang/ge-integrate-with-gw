package geminienterprise

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func TestSplitModel(t *testing.T) {
	cases := map[string][2]string{
		"gemini-3.8-flash":           {"gemini-3.8-flash", ""},
		"gemini-3.8-flash-low":       {"gemini-3.8-flash", "LOW"},
		"models/gemini-4-argon-high": {"gemini-4-argon", "HIGH"},
		"gemini-3.8-flash-medium":    {"gemini-3.8-flash", "MEDIUM"},
	}
	for in, want := range cases {
		id, lv := splitModel(in)
		if id != want[0] || lv != want[1] {
			t.Errorf("splitModel(%q)=(%q,%q) want %v", in, id, lv, want)
		}
	}
}

func TestApplyLevel(t *testing.T) {
	budget := 1024
	req := &dto.GeminiChatRequest{}
	req.GenerationConfig.ThinkingConfig = &dto.GeminiThinkingConfig{ThinkingBudget: &budget}
	applyLevel(req, "LOW")
	tc := req.GenerationConfig.ThinkingConfig
	if tc.ThinkingLevel != "LOW" || tc.ThinkingBudget != nil {
		t.Errorf("got level=%q budget=%v", tc.ThinkingLevel, tc.ThinkingBudget)
	}
	req2 := &dto.GeminiChatRequest{}
	applyLevel(req2, "")
	if req2.GenerationConfig.ThinkingConfig != nil {
		t.Error("empty level must not touch thinkingConfig")
	}
}

func TestToSSE(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"杭"}]}}]}{"candidates":[{"content":{"parts":[{"text":"州"}]}}],"usageMetadata":{"totalTokenCount":3}}`
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	toSSE(resp)
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Count(s, "data: ") != 2 || !strings.Contains(s, `"text":"杭"`) || !strings.HasSuffix(s, "}\n\n") {
		t.Errorf("unexpected SSE:\n%s", s)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Error("content-type not set")
	}
}

func TestUnwrapErrorBody(t *testing.T) {
	resp := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`[{"error":{"code":400,"message":"x"}}]`))}
	unwrapErrorBody(resp)
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"error":{"code":400,"message":"x"}}` {
		t.Errorf("got %s", b)
	}
}

func TestMoveSchema(t *testing.T) {
	req := &dto.GeminiChatRequest{}
	req.GenerationConfig.ResponseSchema = map[string]any{"type": "object"}
	moveSchema(req)
	if req.GenerationConfig.ResponseSchema != nil || string(req.GenerationConfig.ResponseJsonSchema) != `{"type":"object"}` {
		t.Errorf("schema not moved: %v %s", req.GenerationConfig.ResponseSchema, req.GenerationConfig.ResponseJsonSchema)
	}
}
