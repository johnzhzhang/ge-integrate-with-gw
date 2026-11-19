// Package geminienterprise 通过 Discovery Engine API 调用 Gemini Enterprise（GE）应用中的 Gemini 模型。
//
// 上游（v1alpha，未写入公开 discovery 文档）：
//
//	POST {base}/v1alpha/projects/{project}/locations/{location}/collections/default_collection/
//	     engines/{engine}/assistants/default_assistant:generateContent?model={model}
//	POST …:streamGenerateContent?model={model}
//
// 请求体和响应都是 Gemini 原生 GenerateContent 格式，所以适配器内嵌 gemini.Adaptor，
// 复用它的请求转换（OpenAI / Claude / Gemini → Gemini）和响应处理（usage、计费、格式转换），
// 只改 URL、认证、档位后缀和流式响应的分帧。
//
// 渠道配置：
//   - 密钥：服务账号 JSON，或 "adc"（在 GCE / Cloud Run / GKE 上用运行身份）
//   - 其他：{"project_id":"…","engine_id":"…","location":"global"}
//   - Base URL：默认 https://discoveryengine.googleapis.com
package geminienterprise

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
	gemini.Adaptor
	cfg Config
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) loadConfig(info *relaycommon.RelayInfo) error {
	cfg := Config{}
	if s := strings.TrimSpace(info.ApiVersion); s != "" { // 渠道“其他”字段
		if err := common.Unmarshal([]byte(s), &cfg); err != nil {
			return fmt.Errorf("channel \"other\" must be JSON like {\"project_id\":\"…\",\"engine_id\":\"…\"}: %w", err)
		}
	}
	if cfg.ProjectID == "" {
		if creds, _ := parseServiceAccount(info.ApiKey); creds != nil {
			cfg.ProjectID = creds.ProjectID
		}
	}
	if cfg.Location == "" {
		cfg.Location = "global"
	}
	if cfg.ProjectID == "" || cfg.EngineID == "" {
		return errors.New("gemini-enterprise channel requires project_id and engine_id in \"other\"")
	}
	a.cfg = cfg
	return nil
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if err := a.loadConfig(info); err != nil {
		return "", err
	}
	base := strings.TrimRight(info.ChannelBaseUrl, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	action := "generateContent"
	if info.IsStream {
		action = "streamGenerateContent"
	}
	modelID, _ := splitModel(info.UpstreamModelName)
	return fmt.Sprintf("%s/v1alpha/projects/%s/locations/%s/collections/default_collection/engines/%s/assistants/default_assistant:%s?model=%s",
		base, a.cfg.ProjectID, a.cfg.Location, a.cfg.EngineID, action, url.QueryEscape(modelID)), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	if a.cfg.ProjectID == "" {
		if err := a.loadConfig(info); err != nil {
			return err
		}
	}
	token, err := getAccessToken(info)
	if err != nil {
		return err
	}
	req.Set("Authorization", "Bearer "+token)
	req.Set("X-Goog-User-Project", a.cfg.ProjectID) // 计费 / 配额项目
	return nil
}

// withLevel 把模型名的 -low/-medium/-high 后缀转成 thinkingLevel，并把 responseSchema 改成 responseJsonSchema
func withLevel(info *relaycommon.RelayInfo, v any, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	_, level := splitModel(info.UpstreamModelName)
	if req, ok := v.(*dto.GeminiChatRequest); ok {
		applyLevel(req, level)
		moveSchema(req)
	}
	return v, nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	v, err := a.Adaptor.ConvertOpenAIRequest(c, info, request)
	return withLevel(info, v, err)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	v, err := a.Adaptor.ConvertClaudeRequest(c, info, request)
	return withLevel(info, v, err)
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	v, err := a.Adaptor.ConvertGeminiRequest(c, info, request)
	return withLevel(info, v, err)
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	resp, err := channel.DoApiRequest(a, c, info, requestBody) // 传入 a，使用本适配器的 URL 和请求头
	if err != nil || resp == nil {
		return resp, err
	}
	if resp.StatusCode != http.StatusOK {
		unwrapErrorBody(resp)
	} else if info.IsStream {
		toSSE(resp)
	}
	return resp, nil
}

// DoResponse 沿用 gemini.Adaptor：响应就是标准 Gemini 格式

func (a *Adaptor) GetModelList() []string { return ModelList }
func (a *Adaptor) GetChannelName() string { return ChannelName }

// ---- 不支持的接口 ----

func (a *Adaptor) ConvertEmbeddingRequest(*gin.Context, *relaycommon.RelayInfo, dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("gemini-enterprise: embeddings not supported")
}
func (a *Adaptor) ConvertImageRequest(*gin.Context, *relaycommon.RelayInfo, dto.ImageRequest) (any, error) {
	return nil, errors.New("gemini-enterprise: image generation not supported")
}
func (a *Adaptor) ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("gemini-enterprise: audio not supported")
}
