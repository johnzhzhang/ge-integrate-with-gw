# new-api 原生接入 Gemini Enterprise（Discovery Engine API）指南

本文说明怎样给 new-api 增加一个原生渠道类型 **Gemini Enterprise**。渠道通过 Discovery Engine API 调用 GE 应用里的 Gemini 模型，不经过 AIClient2API 等中间代理。内容包括实际执行过的操作、核心代码说明和测试方法。

项目、账号、密钥等隐私信息用占位符代替：

| 占位符 | 含义 |
|---|---|
| `<PROJECT_ID>` | GCP 项目 ID |
| `<ENGINE_ID>` | Gemini Enterprise 应用（engine）ID |
| `<SERVICE_ACCOUNT>` | 调用 GE 的服务账号 |
| `<ADMIN_PASS>` | new-api 管理员密码 |
| `<NEWAPI_TOKEN>` | new-api 签发给客户端的令牌（`sk-...`） |

## 1. 方案

### 1.1 上游接口

```
POST https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:generateContent?model=<MODEL_ID>
POST …/assistants/default_assistant:streamGenerateContent?model=<MODEL_ID>
```

- 请求体和响应都是 **Gemini 原生 GenerateContent 格式**：支持 systemInstruction、多轮 contents、responseJsonSchema、thinkingConfig、tools，返回 usageMetadata（含缓存和思考 token）。
- 模型由 **URL 查询参数 `model`** 指定，不带档位后缀；可用范围取决于 GE 应用的模型设置和许可证。
- 只在 **v1alpha** 可用，这个方法**没有写进公开 discovery 文档**。正式使用前请向 Google 确认支持情况和计费方式（响应中 `trafficType` 为 `PROVISIONED_THROUGHPUT`）。
- 和 GE 助手接口 `streamAssist` 不同，它不经过助手的规划流程，也不新建 GE 会话。业务 prompt 的首 token 约 1.5~2.5 秒，streamAssist 要 14~100 秒。

### 1.2 架构

```
客户端（OpenAI / Claude / Gemini 格式）
   │  Authorization: Bearer <NEWAPI_TOKEN>
   ▼
new-api
   │  geminienterprise.Adaptor（内嵌 gemini.Adaptor）
   │   ① 请求转换：复用 Gemini 渠道的转换器（OpenAI / Claude / Gemini → Gemini）
   │   ② 本渠道附加处理：模型后缀 → thinkingLevel；responseSchema → responseJsonSchema
   │   ③ URL、认证（服务账号 JSON 或 adc）
   │   ④ 流式响应分帧：连续 JSON 对象 → SSE
   │   ⑤ 响应处理：复用 Gemini 渠道的处理器（格式转换、usage、计费、日志）
   ▼
Discovery Engine：assistants/default_assistant:(stream)GenerateContent?model=…
```

上游就是 Gemini 格式，所以适配器内嵌 new-api 现成的 `gemini.Adaptor`，只改与 Gemini API 不同的地方：

| 与 Gemini API 的差异 | 适配器的处理 |
|---|---|
| URL 和模型参数：`assistants/default_assistant:generateContent?model=` | 重写 `GetRequestURL` |
| 认证：OAuth access token + `X-Goog-User-Project` | 服务账号 JSON 换 token，或在 GCP 上用元数据服务器（`adc`） |
| 模型 ID 不带档位后缀 | `gemini-3.8-flash-low` → `model=gemini-3.8-flash` + `thinkingLevel: LOW` |
| `responseSchema` 要求大写 `type`，new-api 转换器输出小写会被拒绝 | 改用 `responseJsonSchema`（标准 JSON Schema） |
| 流式响应是首尾相接的 JSON 对象，不是 SSE（加 `alt=sse` 也一样） | 用 `json.Decoder` 逐个读出，改写成 `data: {...}\n\n` |
| 流式错误体是数组 `[{"error":{...}}]` | 展开成对象，客户端能看到上游原始信息 |

### 1.3 为什么不用现有渠道

| 现有渠道 | 原因 |
|---|---|
| Gemini（type 24） | 调 Gemini API（API Key），不走 GE 应用和 GE 许可证 |
| Vertex AI（type 41） | 调 Vertex AI 模型接口，不走 GE |
| OpenAI / 自定义 | Discovery Engine 没有 OpenAI 兼容接口 |

## 2. 前提条件

| 项目 | 要求 |
|---|---|
| GE 应用 | 项目里已有 GE 应用 `<ENGINE_ID>`，要用的模型在应用的模型设置中已启用 |
| 调用身份 | `<SERVICE_ACCOUNT>` 能调用该应用（例如 `roles/discoveryengine.user`），有 `roles/serviceusage.serviceUsageConsumer`，并按 GE 要求分配许可证 |
| 构建环境 | Docker、git |

先确认调用身份能直接调上游。这一步失败说明是 GE 侧的权限或许可证问题，和 new-api 无关：

```bash
curl -s -X POST -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  -H "X-Goog-User-Project: <PROJECT_ID>" -H "Content-Type: application/json" \
  "https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:generateContent?model=gemini-3.8-flash" \
  -d '{"contents":[{"role":"user","parts":[{"text":"1+1=? 只回答数字"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"}}}'
```

返回 `candidates` 和 `usageMetadata` 即可。

## 3. 部署步骤

### 3.1 获取源码并打补丁

```bash
git clone https://github.com/QuantumNous/new-api.git && cd new-api
git checkout a1d1fb0                     # 本文基于的上游版本
git checkout -b feature/gemini-enterprise
git apply /path/to/new-api-gemini-enterprise.patch
```

| 文件 | 改动 |
|---|---|
| `relay/channel/geminienterprise/*.go` | 新增：适配器、认证、模型后缀和流式分帧处理、单元测试 |
| `constant/channel.go` | 新增渠道类型 `ChannelTypeGEAssist = 64`、名称 `Gemini Enterprise`、默认 Base URL |
| `constant/api_type.go` | 新增 `APITypeGEAssist` |
| `common/api_type.go` | 渠道类型 64 → `APITypeGEAssist` |
| `relay/relay_adaptor.go` | `APITypeGEAssist` → `geminienterprise.Adaptor` |
| `middleware/distributor.go` | 把渠道“其他”字段传给适配器（`api_version`） |
| `web/src/features/channels/...` | 前端：渠道类型、图标、提示文字、“其他”字段的 JSON 输入框 |

### 3.2 运行单元测试

```bash
docker run --rm -v $PWD:/src -w /src golang:1.26.10-alpine \
  go test ./relay/channel/geminienterprise/ -v
```

预期 5 个测试全部 PASS：

| 测试 | 内容 |
|---|---|
| `TestSplitModel` | 模型后缀拆分 |
| `TestApplyLevel` | 写入 thinkingLevel 并清掉 budget |
| `TestMoveSchema` | responseSchema 改为 responseJsonSchema |
| `TestToSSE` | 连续 JSON 对象改写为 SSE |
| `TestUnwrapErrorBody` | 展开数组形式的错误体 |

### 3.3 构建并启动

```bash
docker build -t new-api-ge:dev .          # 上游 Dockerfile，含前端构建，约 3 分钟
mkdir -p $HOME/new-api-ge/data
docker run -d --name new-api-ge --restart=unless-stopped \
  -p 127.0.0.1:3000:3000 -v $HOME/new-api-ge/data:/data -e TZ=UTC new-api-ge:dev
```

- 默认使用 SQLite，数据在 `/data`。生产环境建议用 MySQL / PostgreSQL（`SQL_DSN` 环境变量）。
- 端口只绑 `127.0.0.1`；对外提供服务时放在 HTTPS 反向代理或负载均衡后面。
- 渠道密钥用 `adc` 时，new-api 必须运行在 GCE / Cloud Run / GKE 上，并以 `<SERVICE_ACCOUNT>` 作为运行身份。

### 3.4 初始化管理员

```bash
curl -s -X POST http://127.0.0.1:3000/api/setup -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"<ADMIN_PASS>","confirmPassword":"<ADMIN_PASS>","SelfUseModeEnabled":true,"DemoSiteEnabled":false}'
```

也可以在浏览器打开控制台完成初始化。

### 3.5 创建渠道

**方式一：控制台。** 渠道 → 添加渠道：

| 字段 | 填写 |
|---|---|
| 类型 | Gemini Enterprise |
| Base URL | 留空（默认 `https://discoveryengine.googleapis.com`） |
| 密钥 | 服务账号 JSON 全文；或 `adc`，表示用 new-api 所在机器的运行身份 |
| Gemini Enterprise config（其他） | `{"project_id": "<PROJECT_ID>", "engine_id": "<ENGINE_ID>", "location": "global"}` |
| 模型 | 例如 `gemini-3.8-flash-low,gemini-3.8-flash-medium,gemini-3.8-flash-high,gemini-3.8-flash,gemini-2.5-pro` |

- 密钥是服务账号 JSON 时，`project_id` 可以不填，默认取 JSON 里的项目。
- 模型名的 `-low/-medium/-high` 后缀表示思考档位；不带后缀时由客户端的 `reasoning_effort` 等参数决定，不传则用模型默认档位（会思考）。

**方式二：脚本。** `scripts/admin_setup.sh` 登录管理端、创建渠道和一个不限额的测试令牌。令牌写入 `.token`（权限 600），输出中不打印任何密钥：

```bash
NEWAPI=http://127.0.0.1:3000 ADMIN_USER=admin ADMIN_PASS='<ADMIN_PASS>' \
GE_PROJECT_ID=<PROJECT_ID> GE_ENGINE_ID=<ENGINE_ID> GE_KEY=adc \
./scripts/admin_setup.sh
```

脚本调用的管理 API：

| 步骤 | 接口 |
|---|---|
| 登录 | `POST /api/user/login`，取返回的 `data.access_token` |
| 创建渠道 | `POST /api/channel/`，请求头 `Authorization: Bearer <access_token>`、`New-Api-User: 1`，请求体 `{"mode":"single","channel":{"type":64,"key":"adc","other":"{...}","models":"...","group":"default"}}` |
| 修改渠道 | `PUT /api/channel/`，请求体 `{"id":1,"models":"...","other":"..."}` |
| 创建令牌 | `POST /api/token/` |
| 读取令牌 | `GET /api/token/` 找到 ID，`POST /api/token/{id}/key` 取明文 |

### 3.6 调用

```python
from openai import OpenAI
c = OpenAI(base_url="http://127.0.0.1:3000/v1", api_key="<NEWAPI_TOKEN>")
s = c.chat.completions.create(
    model="gemini-3.8-flash-low", stream=True, stream_options={"include_usage": True},
    messages=[{"role": "system", "content": "……"}, {"role": "user", "content": "你好"}])
for ch in s:
    if ch.choices: print(ch.choices[0].delta.content or "", end="")
    if ch.usage: print("\n", ch.usage)   # prompt_tokens / prompt_tokens_details.cached_tokens / reasoning_tokens
```

```bash
# Claude 格式
curl http://127.0.0.1:3000/v1/messages -H "x-api-key: <NEWAPI_TOKEN>" -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash-low","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'
# Gemini 格式
curl "http://127.0.0.1:3000/v1beta/models/gemini-3.8-flash-low:streamGenerateContent?alt=sse" \
  -H "x-goog-api-key: <NEWAPI_TOKEN>" -H "Content-Type: application/json" \
  -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}'
```

## 4. 核心代码说明

代码在 `relay/channel/geminienterprise/`：`adaptor.go`、`auth.go`、`relay.go`、`dto.go`、`constants.go`，单元测试在 `relay_test.go`。

### 4.1 注册渠道

new-api 按“渠道类型 → API 类型 → 适配器”三级分发：

```go
// constant/channel.go
ChannelTypeGEAssist = 64
ChannelTypeGEAssist: "Gemini Enterprise",
"https://discoveryengine.googleapis.com",          // ChannelBaseURLs[64]，默认 Base URL

// constant/api_type.go
APITypeGEAssist                                     // 加在 APITypeDummy 前

// common/api_type.go
case constant.ChannelTypeGEAssist:
    apiType = constant.APITypeGEAssist

// relay/relay_adaptor.go
case constant.APITypeGEAssist:
    return &geminienterprise.Adaptor{}              // 每个请求新建一个实例

// middleware/distributor.go —— 渠道“其他”字段交给适配器（info.ApiVersion）
case constant.ChannelTypeGEAssist:
    c.Set("api_version", channel.Other)
```

### 4.2 内嵌 Gemini 适配器（adaptor.go）

```go
type Adaptor struct {
    gemini.Adaptor   // 复用：请求转换、DoResponse（Gemini → OpenAI/Claude/Gemini、usage、计费）
    cfg Config
}
```

本适配器只覆盖以下方法：

| 方法 | 做什么 |
|---|---|
| `GetRequestURL` | `…/assistants/default_assistant:generateContent?model=<不带后缀的 ID>`；流式用 `streamGenerateContent` |
| `SetupRequestHeader` | `Authorization: Bearer <token>`、`X-Goog-User-Project: <project_id>` |
| `ConvertOpenAIRequest` / `ConvertClaudeRequest` / `ConvertGeminiRequest` | 先调内嵌的 Gemini 转换，再调 `withLevel` 做本渠道的附加处理 |
| `DoRequest` | 必须重写成 `channel.DoApiRequest(a, …)`，传入外层适配器，否则会用内嵌适配器的 Gemini API URL 和请求头；非 200 时展开错误体，流式 200 时套上 `toSSE` |
| `GetModelList` / `GetChannelName` | 本渠道的模型列表和名称 |
| 向量 / 图片 / 音频 | 返回不支持 |

`DoResponse` 不覆盖，直接用 Gemini 渠道的处理器。上游返回的是真实 `usageMetadata`，new-api 日志和计费里的 prompt、completion、cached tokens 都是上游的真实值。

### 4.3 渠道配置与认证（adaptor.go `loadConfig`、auth.go）

- “其他”字段是 JSON，解析为 `Config{ProjectID, EngineID, Location}`。`project_id` 缺省时从服务账号 JSON 读取，`location` 默认 `global`。
- 密钥为 `adc` 时，从 `http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token` 取 token。
- 密钥为服务账号 JSON 时，用 `vertex.AcquireAccessToken` 签 JWT 换 token（复用 Vertex 渠道的实现）。
- token 按渠道 ID（多密钥时再加序号）缓存，过期前刷新。缓存键包含 `client_email` 和 `private_key_id`，换了密钥不会复用旧 token。

### 4.4 模型后缀与 JSON Schema（relay.go）

```go
// "gemini-3.8-flash-low" → ("gemini-3.8-flash", "LOW")
func splitModel(model string) (id, level string)

// 写入 thinkingLevel，并清掉客户端可能带来的 thinkingBudget（两者互斥）
func applyLevel(req *dto.GeminiChatRequest, level string)

// new-api 把 OpenAI 的 json_schema 转成 responseSchema（小写 type），上游要求大写枚举，会报
// "didn't specify the schema type field"；改放到 responseJsonSchema（标准 JSON Schema）
func moveSchema(req *dto.GeminiChatRequest)
```

上游支持的 `thinkingLevel`：LOW、MEDIUM、HIGH。`MINIMAL` 返回 400。

### 4.5 流式分帧与错误（relay.go `toSSE`、`unwrapErrorBody`）

```go
dec := json.NewDecoder(src)          // 上游流：{...}{...}{...}
for {
    var obj json.RawMessage
    if err = dec.Decode(&obj); err != nil { break }   // 每读完一个对象
    pw.Write("data: " + obj + "\n\n")                 // 写成 SSE 一行
}
```

- 通过 `io.Pipe` 边读边写，new-api 的 Gemini 流式处理器从管道读，首 token 不被缓冲。
- `json.Decoder` 按 UTF-8 解码，网络分块切断汉字时不会产生乱码。
- 非 200 响应的错误体是数组 `[{"error":{...}}]`（流式接口），展开第一个元素后交给 new-api 的通用错误处理，客户端能看到原始信息，例如 `The model gemini-4-argon is not available during the free trial`。

### 4.6 前端

- 新增渠道类型 64：`constants.ts`、`channel-type-config.ts`、`channel-utils.ts`，使用 Gemini 图标，并给出密钥和“其他”字段的填写提示。
- `channel-mutate-drawer.tsx`：类型为 64 时显示 "Gemini Enterprise config (JSON)" 多行输入框。

## 5. 测试方法

### 5.1 单元测试

见 3.2，`go test ./relay/channel/geminienterprise/`。

### 5.2 功能测试

```bash
BASE_URL=http://127.0.0.1:3000 API_KEY=$(cat .token) MODEL=gemini-3.8-flash-low python3 tests/smoke_test.py
```

| 用例 | 通过条件 |
|---|---|
| 不带令牌 | 401 |
| `/v1/models` | 包含渠道配置的模型 |
| OpenAI 非流式 / 流式 | 回答正确，流式中文无乱码 |
| 系统指令 + 多轮历史 | 能回答前文让它记住的数字 |
| `response_format: json_schema` | 能被 `json.loads` 解析且字段齐全 |
| Claude `/v1/messages` | 返回 text |
| Gemini `generateContent` | 返回正确回答 |
| gemini-4-argon（信息） | 只打印结果，取决于许可证 |

实测：

```
[PASS] 不带 Key 返回 401  401
[PASS] 模型列表  ['gemini-2.5-pro', 'gemini-3.7-flash', 'gemini-3.8-flash', 'gemini-3.8-flash-high', 'gemini-3.8-flash-low', 'gemini-3.8-flash-medium', 'gemini-4-argon']
[PASS] OpenAI 非流式  '391' 1.7s
[PASS] OpenAI 流式  528 字 首token 1.4s 总 6.0s
[PASS] 流式中文无乱码
[PASS] 系统指令 + 多轮历史  '4271'
[PASS] JSON Schema  ['answer', 'tags']
[PASS] Claude /v1/messages  ['蓝']
[PASS] Gemini generateContent  '2'
[INFO] gemini-4-argon（取决于许可证）: HTTP 400 ... The model gemini-4-argon is not available during the free trial ...

9/9 通过
```

### 5.3 用量与缓存

用一个约 2.5 万 token 的长 system prompt（固定前缀）+ JSON Schema，通过 new-api 连发 5 次（OpenAI 格式流式，`include_usage`）：

| 次数 | 首 token | prompt | cached | reasoning | JSON |
|---|---|---|---|---|---|
| 1 | 10.22s | 24,888 | 0 | 0 | 正确 |
| 2 | 2.71s | 24,888 | 22,845 | 0 | 正确 |
| 3 | 2.67s | 24,888 | 22,845 | 0 | 正确 |
| 4 | 2.13s | 24,888 | 22,845 | 0 | 正确 |
| 5 | 1.94s | 24,888 | 22,845 | 0 | 正确 |

- `-low` 后缀生效，没有思考 token。
- 第 2 次起隐式缓存命中 92%。
- 控制台日志（`GET /api/log/?type=2`）里的 prompt / completion / 缓存 token 与上游 `usageMetadata` 一致。Claude 流式请求的事件序列完整（message_start → content_block_* → message_delta → message_stop）。

### 5.4 延迟测试

```bash
BASE_URL=http://127.0.0.1:3000 API_KEY=$(cat .token) MODEL=gemini-3.8-flash-low python3 tests/smoke_test.py latency 10
PROMPT_FILE=./my_prompt.txt BASE_URL=... API_KEY=... python3 tests/smoke_test.py latency 10
```

输出首 token、总耗时的中位数和 P90。评估时至少跑 10 次看 P90。

### 5.5 排查

| 现象 | 原因 / 处理 |
|---|---|
| `channel "other" must be JSON...` | “其他”字段不是合法 JSON |
| `requires project_id and engine_id` | “其他”缺少 `engine_id`，或密钥为 `adc` 时没填 `project_id` |
| `metadata server unreachable` | 密钥为 `adc` 但不在 GCE / Cloud Run / GKE 上运行；改用服务账号 JSON |
| 403 `The model xxx is not enabled by the admin` | GE 应用没有启用该模型，或模型 ID 写错（上游不认带后缀的 ID，适配器会自动去掉 `-low/-medium/-high`） |
| 400 `not available during the free trial` | GE 许可证为试用版，模型受限 |
| 400 `Thinking level is unsupported` | 客户端传了 MINIMAL 等不支持的档位 |
| 404 | 不是 v1alpha，或 Base URL 填错 |

new-api 日志：`docker logs -f new-api-ge`，错误会带上游原始信息和请求 ID。

## 6. 注意事项

- **未公开接口**：`assistants/default_assistant:generateContent` 没有出现在 Discovery Engine 的 discovery 文档里，只在 v1alpha 可用，行为可能变化。上线前向 Google 确认支持情况、SLA 和计费方式。
- **思考档位**：不带后缀且客户端不指定时，模型默认会思考，首 token 慢几秒。业务调用建议用 `-low`。
- **请求体不校验未知字段**：上游会忽略拼错的字段而不报错，调试时以 `usageMetadata` 和实际输出为准。
- **密钥安全**：优先在 GCP 上用 `adc`，避免在数据库里保存服务账号私钥；必须用 JSON 时给最小权限并定期轮换。
- **许可合规**：用一个服务账号代表大量终端用户调用 GE，是否符合 GE 许可条款需与 Google 确认。
- **维护**：补丁基于 new-api `a1d1fb0`，升级上游时重新应用，并跑 3.2 的单元测试和 5.2 的功能测试。
