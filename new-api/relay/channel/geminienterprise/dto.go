package geminienterprise

import "encoding/json"

// Config 渠道“其他”字段（JSON）：
//
//	{"project_id": "<PROJECT_ID>", "engine_id": "<ENGINE_ID>", "location": "global", "web_grounding": false}
//
// project_id 不填时取服务账号 JSON 里的 project_id。
type Config struct {
	ProjectID    string `json:"project_id"`
	EngineID     string `json:"engine_id"`
	Location     string `json:"location"`
	WebGrounding bool   `json:"web_grounding"`
}

// ---- streamAssist 请求 ----

type AssistRequest struct {
	Query          AssistQuery     `json:"query"`
	GenerationSpec *GenerationSpec `json:"generationSpec,omitempty"`
	ToolsSpec      map[string]any  `json:"toolsSpec"`
}

type AssistQuery struct {
	Text string `json:"text"`
}

type GenerationSpec struct {
	ModelID string `json:"modelId"`
}

// ---- streamAssist 响应（数组中的每个元素） ----

type AssistResponse struct {
	Answer      *AssistAnswer   `json:"answer,omitempty"`
	SessionInfo *SessionInfo    `json:"sessionInfo,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
}

type AssistAnswer struct {
	State                string          `json:"state"`
	Replies              []AssistReply   `json:"replies"`
	AssistSkippedReasons json.RawMessage `json:"assistSkippedReasons,omitempty"`
}

type AssistReply struct {
	GroundedContent struct {
		Content struct {
			Role     string  `json:"role"`
			Text     *string `json:"text,omitempty"`
			Thought  bool    `json:"thought,omitempty"`
			MimeType string  `json:"mimeType,omitempty"`
		} `json:"content"`
	} `json:"groundedContent"`
}

type SessionInfo struct {
	Session string `json:"session"`
}
