package geminienterprise

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

var levelSuffix = regexp.MustCompile(`-(low|medium|high)$`)

// splitModel "gemini-3.8-flash-low" → ("gemini-3.8-flash", "LOW")。上游的 ?model= 不认档位后缀。
func splitModel(model string) (id, level string) {
	model = strings.TrimPrefix(model, "models/")
	if m := levelSuffix.FindStringSubmatch(model); m != nil {
		return strings.TrimSuffix(model, m[0]), strings.ToUpper(m[1])
	}
	return model, ""
}

// applyLevel 模型名带档位后缀时，写入 generationConfig.thinkingConfig.thinkingLevel（覆盖客户端的 budget）
func applyLevel(req *dto.GeminiChatRequest, level string) {
	if req == nil || level == "" {
		return
	}
	tc := req.GenerationConfig.ThinkingConfig
	if tc == nil {
		tc = &dto.GeminiThinkingConfig{}
	}
	tc.ThinkingBudget = nil // 与 thinkingLevel 互斥
	tc.ThinkingLevel = level
	req.GenerationConfig.ThinkingConfig = tc
}

// moveSchema 上游的 responseSchema（OpenAPI 子集）要求 type 为大写枚举，new-api 转换器输出的是小写，会被拒绝
// （"didn't specify the schema type field"）。responseJsonSchema 接受标准 JSON Schema，这里改用它。
func moveSchema(req *dto.GeminiChatRequest) {
	gc := &req.GenerationConfig
	if gc.ResponseSchema == nil || len(gc.ResponseJsonSchema) > 0 {
		return
	}
	if b, err := common.Marshal(gc.ResponseSchema); err == nil {
		gc.ResponseJsonSchema = b
		gc.ResponseSchema = nil
	}
}

// unwrapErrorBody 流式接口的错误体是数组 [{"error":{...}}]，new-api 的通用错误处理只认对象，这里展开第一个元素
func unwrapErrorBody(resp *http.Response) {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		body = nil
	}
	var arr []json.RawMessage
	if common.Unmarshal(body, &arr) == nil && len(arr) > 0 {
		body = arr[0]
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
}

// toSSE streamGenerateContent 返回首尾相接的 JSON 对象（不是 SSE，加 alt=sse 也一样）。
// 这里用 json.Decoder 逐个读出，改写成 "data: {...}\n\n"，交给 new-api 现有的 Gemini 流式处理器。
func toSSE(resp *http.Response) {
	src := resp.Body
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		dec := json.NewDecoder(src)
		var err error
		for {
			var obj json.RawMessage
			if err = dec.Decode(&obj); err != nil {
				break
			}
			if _, err = pw.Write(append(append([]byte("data: "), obj...), '\n', '\n')); err != nil {
				break
			}
		}
		if err == io.EOF {
			err = nil
		}
		_ = pw.CloseWithError(err)
	}()
	resp.Body = pr
	resp.ContentLength = -1
	resp.Header.Set("Content-Type", "text/event-stream")
}
