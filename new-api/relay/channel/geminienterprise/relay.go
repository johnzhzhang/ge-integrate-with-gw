package geminienterprise

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/tokenkit"

	"github.com/gin-gonic/gin"
)

var levelSuffix = regexp.MustCompile(`-(low|medium|high)$`)
var jsonFence = regexp.MustCompile("(?s)^```(?:json)?\\s*(.*?)\\s*```$")

// modelID streamAssist 的 modelId 不带档位后缀
func modelID(model string) string {
	return levelSuffix.ReplaceAllString(strings.TrimPrefix(model, "models/"), "")
}

func roleLabel(role string) string {
	switch role {
	case "system", "developer":
		return "系统指令"
	case "assistant":
		return "助手"
	default:
		return "用户"
	}
}

func wantsJSON(req *dto.GeneralOpenAIRequest) bool {
	return req.ResponseFormat != nil && (req.ResponseFormat.Type == "json_object" || req.ResponseFormat.Type == "json_schema")
}

// buildQueryText 把 OpenAI messages 压平成 streamAssist 的 query.text。
// streamAssist 没有 systemInstruction / 历史 / JSON Schema 字段，只能写进同一段文字。
func buildQueryText(req *dto.GeneralOpenAIRequest) string {
	type seg struct{ role, text string }
	segs := make([]seg, 0, len(req.Messages))
	for i := range req.Messages {
		m := &req.Messages[i]
		if t := m.StringContent(); t != "" {
			segs = append(segs, seg{m.Role, t})
		}
	}
	var b strings.Builder
	if len(segs) == 1 && segs[0].role == "user" {
		b.WriteString(segs[0].text) // 单条用户消息直接发原文
	} else {
		for i, s := range segs {
			label := roleLabel(s.role)
			if i == len(segs)-1 && label == "用户" {
				label = "本次用户消息"
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString("【" + label + "】\n" + s.text)
		}
	}
	if wantsJSON(req) {
		b.WriteString("\n\n只输出一个 JSON 对象，不要用 Markdown 代码块包裹，不要输出 JSON 以外的任何文字。")
		if req.ResponseFormat.Type == "json_schema" && len(req.ResponseFormat.JsonSchema) > 0 {
			var js struct {
				Schema json.RawMessage `json:"schema"`
			}
			if common.Unmarshal(req.ResponseFormat.JsonSchema, &js) == nil && len(js.Schema) > 0 {
				b.WriteString("JSON 必须符合以下 JSON Schema：\n" + string(js.Schema))
			}
		}
	}
	return b.String()
}

func stripFence(t string) string {
	t = strings.TrimSpace(t)
	if m := jsonFence.FindStringSubmatch(t); m != nil {
		return m[1]
	}
	return t
}

// unwrapErrorBody streamAssist 的错误体是数组 [{"error":{...}}]，new-api 的通用错误处理只认对象，这里展开第一个元素
func unwrapErrorBody(resp *http.Response) {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return
	}
	var arr []json.RawMessage
	if common.Unmarshal(body, &arr) == nil && len(arr) > 0 {
		body = arr[0]
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
}

// iterate 用 json.Decoder 增量读取响应数组，每读到一个元素回调一次（真正的流式，且按 UTF-8 正确解码）
func iterate(body io.Reader, fn func(*AssistResponse) error) error {
	dec := json.NewDecoder(body)
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("unexpected streamAssist response start: %v", tok)
	}
	for dec.More() {
		var el AssistResponse
		if err := dec.Decode(&el); err != nil {
			return err
		}
		if len(el.Error) > 0 {
			return fmt.Errorf("streamAssist error: %s", string(el.Error))
		}
		if el.Answer != nil && (el.Answer.State == "FAILED" || el.Answer.State == "SKIPPED") {
			return fmt.Errorf("streamAssist answer %s: %s", el.Answer.State, string(el.Answer.AssistSkippedReasons))
		}
		if err := fn(&el); err != nil {
			return err
		}
	}
	return nil
}

// texts 取一个元素中的正文和思考摘要；跳过带 mimeType 的块（推荐问题等）
func texts(el *AssistResponse) (content, thought string) {
	if el.Answer == nil {
		return
	}
	for _, r := range el.Answer.Replies {
		c := r.GroundedContent.Content
		if c.Text == nil || c.MimeType != "" {
			continue
		}
		if c.Thought {
			thought += *c.Text
		} else {
			content += *c.Text
		}
	}
	return
}

func readAll(resp *http.Response) (content, thought string, err error) {
	var cb, tb strings.Builder
	err = iterate(resp.Body, func(el *AssistResponse) error {
		c, t := texts(el)
		cb.WriteString(c)
		tb.WriteString(t)
		return nil
	})
	return strings.TrimRight(cb.String(), " \t\r\n"), tb.String(), err
}

// estimateUsage streamAssist 不返回 token 数，按文本估算（与 new-api 本地计数方式一致）
func estimateUsage(info *relaycommon.RelayInfo, text string) dto.Usage {
	u := dto.Usage{PromptTokens: info.GetEstimatePromptTokens(), CompletionTokens: tokenkit.Estimate(info.UpstreamModelName, text)}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return u
}

// 下面把 streamAssist 的输出改写成 OpenAI chat completions 格式，再交给 new-api 现成的 OpenAI 处理器。
// 这样 OpenAI / Claude / Gemini 三种客户端格式的响应转换、usage 估算、计费都复用现有逻辑。

// toOpenAIJSON 非流式：读完整个数组，生成 OpenAI chat.completion JSON
func toOpenAIJSON(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, jsonMode bool) (*http.Response, error) {
	content, thought, err := readAll(resp)
	service.CloseResponseBodyGracefully(resp)
	if err != nil {
		return nil, err
	}
	if jsonMode {
		content = stripFence(content)
	}
	msg := dto.Message{Role: "assistant", Content: content}
	if thought != "" {
		msg.ReasoningContent = &thought
	}
	out := dto.OpenAITextResponse{
		Id:      helper.GetResponseID(c),
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Model:   info.UpstreamModelName,
		Choices: []dto.OpenAITextResponseChoice{{Index: 0, Message: msg, FinishReason: "stop"}},
		Usage:   estimateUsage(info, thought+content),
	}
	data, err := common.Marshal(out)
	if err != nil {
		return nil, err
	}
	return synthetic(resp, "application/json", io.NopCloser(bytes.NewReader(data))), nil
}

// toOpenAISSE 流式：边读 streamAssist 数组边写 OpenAI SSE（data: {...}），用 io.Pipe 保持真正的流式
func toOpenAISSE(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, jsonMode bool) *http.Response {
	pr, pw := io.Pipe()
	id := helper.GetResponseID(c)
	created := common.GetTimestamp()
	model := info.UpstreamModelName
	write := func(v any) error {
		b, err := common.Marshal(v)
		if err != nil {
			return err
		}
		_, err = pw.Write([]byte("data: " + string(b) + "\n\n"))
		return err
	}
	chunk := func(content, reasoning string) *dto.ChatCompletionsStreamResponse {
		ch := dto.ChatCompletionsStreamResponseChoice{Index: 0}
		if content != "" {
			ch.Delta.SetContentString(content)
		}
		if reasoning != "" {
			ch.Delta.SetReasoningContent(reasoning)
		}
		return &dto.ChatCompletionsStreamResponse{Id: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []dto.ChatCompletionsStreamResponseChoice{ch}}
	}
	go func() {
		defer service.CloseResponseBodyGracefully(resp)
		var err error
		var all strings.Builder // 用于估算 completion tokens
		if jsonMode {
			// JSON 要拿到完整回答才能去掉代码块，整段作为一个块发出
			var content, thought string
			if content, thought, err = readAll(resp); err == nil {
				content = stripFence(content)
				all.WriteString(thought + content)
				err = write(chunk(content, thought))
			}
		} else {
			err = iterate(resp.Body, func(el *AssistResponse) error {
				content, thought := texts(el)
				if content == "" && thought == "" {
					return nil
				}
				all.WriteString(thought + content)
				return write(chunk(content, thought))
			})
		}
		if err == nil {
			err = write(helper.GenerateStopResponse(id, created, model, "stop"))
		}
		if err == nil && (info.ShouldIncludeUsage || info.RelayFormat != types.RelayFormatOpenAI) {
			// 显式给出 usage 块：new-api 在 Claude/Gemini 格式下不会从流文本里累计 token，没有这个块会记成 0。
			// OpenAI 客户端没要 include_usage 时不发，由 OaiStreamHandler 自己按文本估算。
			err = write(helper.GenerateFinalUsageResponse(id, created, model, estimateUsage(info, all.String())))
		}
		if err == nil {
			_, err = pw.Write([]byte("data: [DONE]\n\n"))
		}
		if err != nil {
			common.SysLog(common.LogText("gemini-enterprise stream error: %s", err.Error()))
		}
		_ = pw.CloseWithError(err)
	}()
	return synthetic(resp, "text/event-stream", pr)
}

func synthetic(orig *http.Response, contentType string, body io.ReadCloser) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: h, Body: body,
		ContentLength: -1, Request: orig.Request, Proto: orig.Proto, ProtoMajor: orig.ProtoMajor, ProtoMinor: orig.ProtoMinor}
}
