// Package geminienterprise 通过 Discovery Engine API（Gemini Enterprise 助手 streamAssist）调用 GE 中的 Gemini 模型。
//
// 上游：POST {base}/v1alpha/projects/{project}/locations/{location}/collections/default_collection/
//
//	engines/{engine}/assistants/default_assistant:streamAssist
//
// 渠道配置：
//   - 密钥：服务账号 JSON，或 "adc"（在 GCE / Cloud Run / GKE 上用运行身份）
//   - 其他：{"project_id":"…","engine_id":"…","location":"global","web_grounding":false}
//   - Base URL：默认 https://discoveryengine.googleapis.com
package geminienterprise

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
	cfg      Config
	jsonMode bool
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
	return fmt.Sprintf("%s/v1alpha/projects/%s/locations/%s/collections/default_collection/engines/%s/assistants/default_assistant:streamAssist",
		base, a.cfg.ProjectID, a.cfg.Location, a.cfg.EngineID), nil
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

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if err := a.loadConfig(info); err != nil {
		return nil, err
	}
	a.jsonMode = wantsJSON(request)
	tools := map[string]any{} // {} = 不联网
	if a.cfg.WebGrounding {
		tools["webGroundingSpec"] = map[string]any{}
	}
	return &AssistRequest{
		Query:          AssistQuery{Text: buildQueryText(request)},
		GenerationSpec: &GenerationSpec{ModelID: modelID(info.UpstreamModelName)},
		ToolsSpec:      tools,
	}, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	resp, err := channel.DoApiRequest(a, c, info, requestBody)
	if err == nil && resp != nil && resp.StatusCode != http.StatusOK {
		unwrapErrorBody(resp)
	}
	return resp, err
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// streamAssist 输出 → OpenAI 格式，再交给 OpenAI 处理器（负责转成客户端要的 OpenAI/Claude/Gemini 格式和 usage 估算）
	if info.IsStream {
		return openai.OaiStreamHandler(c, info, toOpenAISSE(c, info, resp, a.jsonMode))
	}
	r, e := toOpenAIJSON(c, info, resp, a.jsonMode)
	if e != nil {
		return nil, types.NewError(e, types.ErrorCodeBadResponseBody)
	}
	return openai.OpenaiHandler(c, info, r)
}

func (a *Adaptor) GetModelList() []string { return ModelList }
func (a *Adaptor) GetChannelName() string { return ChannelName }

// ConvertClaudeRequest / ConvertGeminiRequest：先用 new-api 的转换器转成 OpenAI 请求，再走同一条路径
func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return a.viaOpenAI(c, info, request)
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return a.viaOpenAI(c, info, request)
}

func (a *Adaptor) viaOpenAI(c *gin.Context, info *relaycommon.RelayInfo, request any) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	req, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	return a.ConvertOpenAIRequest(c, info, req)
}

// ---- 不支持的接口 ----

func (a *Adaptor) ConvertRerankRequest(*gin.Context, int, dto.RerankRequest) (any, error) {
	return nil, errors.New("not supported")
}
func (a *Adaptor) ConvertEmbeddingRequest(*gin.Context, *relaycommon.RelayInfo, dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("not supported")
}
func (a *Adaptor) ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("not supported")
}
func (a *Adaptor) ConvertImageRequest(*gin.Context, *relaycommon.RelayInfo, dto.ImageRequest) (any, error) {
	return nil, errors.New("not supported")
}
func (a *Adaptor) ConvertOpenAIResponsesRequest(*gin.Context, *relaycommon.RelayInfo, dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("gemini-enterprise: Responses API not supported, use /v1/chat/completions")
}
