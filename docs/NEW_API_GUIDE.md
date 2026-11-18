# new-api 原生接入 Gemini Enterprise（Discovery Engine API）指南

本文说明怎样给 new-api 增加一个原生渠道类型 **Gemini Enterprise**。这个渠道直接调用 Discovery Engine API 的 `streamAssist`（Gemini Enterprise 助手接口），使用 GE 应用里的 Gemini 模型，不经过 AIClient2API 等中间代理。文中包含实际执行过的操作、核心代码说明和测试方法。

项目、账号、密钥等隐私信息用占位符代替：

| 占位符 | 含义 |
|---|---|
| `<PROJECT_ID>` | GCP 项目 ID |
| `<ENGINE_ID>` | Gemini Enterprise 应用（engine）ID |
| `<SERVICE_ACCOUNT>` | 调用 GE 的服务账号 |
| `<ADMIN_PASS>` | new-api 管理员密码 |
| `<NEWAPI_TOKEN>` | new-api 签发给客户端的令牌（`sk-...`） |

## 1. 方案

### 1.1 架构

```
客户端（OpenAI / Claude / Gemini 格式）
   │  Authorization: Bearer <NEWAPI_TOKEN>
   ▼
new-api
   │  ① Claude / Gemini 请求 → OpenAI 请求（new-api 现有转换器）
   │  ② geminienterprise 适配器：OpenAI 请求 → streamAssist 请求
   │  ③ 适配器：streamAssist 回答 → OpenAI chat completions（JSON 或 SSE）
   │  ④ new-api 现有 OpenAI 处理器：转成客户端格式，估算 usage，计费、记日志
   │  认证：服务账号 JSON 或运行身份（adc）
   ▼
POST https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/
     collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:streamAssist
```

第 ③ 步是关键：适配器把 streamAssist 的输出改写成标准 OpenAI 格式，再交给 new-api 现成的 `OaiStreamHandler` / `OpenaiHandler`。这样三种客户端格式的响应转换、usage 统计、计费、日志都直接复用，不需要为每种格式单独写转换。

### 1.2 为什么不用现有渠道

| 现有渠道 | 不能用的原因 |
|---|---|
| Gemini（type 24） | 调 Gemini API（API Key），不是 GE |
| Vertex AI（type 41） | 调 Vertex AI 模型接口，不走 GE 应用和 GE 许可证 |
| OpenAI / 自定义 | Discovery Engine 没有 OpenAI 兼容接口 |

### 1.3 streamAssist 的限制与处理

| streamAssist 的特点 | 适配器的处理 |
|---|---|
| 只接受一段 `query.text`，没有系统指令和历史字段 | 把 messages 按【系统指令】【用户】【助手】【本次用户消息】拼成一段 |
| 不支持 JSON Schema | `response_format` 为 JSON 时把 schema 写进文字，回答后去掉 ```json 代码块 |
| 模型 ID 不带档位后缀 | 去掉客户端传的 `-low/-medium/-high` |
| 响应是逐步输出的 JSON 数组，不是 SSE | 用 `json.Decoder` 增量解析，改写成 OpenAI SSE |
| 带思考摘要（`thought: true`） | 转成 `reasoning_content`（Claude 格式下为 thinking） |
| 不返回 token 数 | 按文本估算 usage，供计费和日志 |
| 错误体是数组 `[{"error":{...}}]` | 展开成对象，new-api 能显示上游原始错误信息 |
| 默认可能联网 | 默认 `toolsSpec: {}` 不联网，渠道配置可打开 |

## 2. 前提条件

| 项目 | 要求 |
|---|---|
| GE 应用 | 项目里已有 GE 应用 `<ENGINE_ID>`，要用的模型在应用的模型设置中已启用 |
| 调用身份 | `<SERVICE_ACCOUNT>` 能调用该应用的 streamAssist（例如 `roles/discoveryengine.user`），有 `roles/serviceusage.serviceUsageConsumer`，并按 GE 要求分配许可证 |
| 构建环境 | Docker、git |

先确认调用身份能直接调 streamAssist。这一步失败说明是 GE 侧的权限或许可证问题，和 new-api 无关：

```bash
curl -s -X POST -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  -H "X-Goog-User-Project: <PROJECT_ID>" -H "Content-Type: application/json" \
  "https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:streamAssist" \
  -d '{"query":{"text":"1+1=? 只回答数字"},"generationSpec":{"modelId":"gemini-3.8-flash"},"toolsSpec":{}}'
```

## 3. 部署步骤

### 3.1 获取源码并打补丁

```bash
git clone https://github.com/QuantumNous/new-api.git && cd new-api
git checkout a1d1fb0                     # 本文基于的上游版本
git checkout -b feature/gemini-enterprise
git apply /path/to/new-api-gemini-enterprise.patch
```

补丁改动：

| 文件 | 改动 |
|---|---|
| `relay/channel/geminienterprise/*.go` | 新增：适配器、认证、请求/响应转换、单元测试 |
| `constant/channel.go` | 新增渠道类型 `ChannelTypeGEAssist = 64`、名称 `Gemini Enterprise`、默认 Base URL |
| `constant/api_type.go` | 新增 `APITypeGEAssist` |
| `common/api_type.go` | 渠道类型 64 → `APITypeGEAssist` |
| `relay/relay_adaptor.go` | `APITypeGEAssist` → `geminienterprise.Adaptor` |
| `middleware/distributor.go` | 把渠道“其他”字段传给适配器（`api_version`） |
| `web/src/features/channels/...` | 前端：渠道类型列表、图标、提示文字、“其他”字段的 JSON 输入框 |

### 3.2 运行单元测试

```bash
docker run --rm -v $PWD:/src -w /src golang:1.26.10-alpine \
  go test ./relay/channel/geminienterprise/ -v
```

预期 4 个测试全部 PASS：模型 ID 去后缀、messages 拼接、去代码块、streamAssist 数组解析。

### 3.3 构建并启动

```bash
docker build -t new-api-ge:dev .          # 上游 Dockerfile，含前端构建，约 3 分钟

mkdir -p $HOME/new-api-ge/data
docker run -d --name new-api-ge --restart=unless-stopped \
  -p 127.0.0.1:3000:3000 -v $HOME/new-api-ge/data:/data -e TZ=UTC new-api-ge:dev
```

- 默认使用 SQLite，数据在 `/data`。生产环境建议用 MySQL / PostgreSQL（`SQL_DSN` 环境变量）。
- 端口只绑 `127.0.0.1`；对外服务时放在 HTTPS 反向代理或负载均衡后面。
- 渠道密钥用 `adc` 时，new-api 必须运行在 GCE / Cloud Run / GKE 上，并以 `<SERVICE_ACCOUNT>` 作为运行身份。

### 3.4 初始化管理员

浏览器打开控制台完成初始化，或者用 API：

```bash
curl -s -X POST http://127.0.0.1:3000/api/setup -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"<ADMIN_PASS>","confirmPassword":"<ADMIN_PASS>","SelfUseModeEnabled":true,"DemoSiteEnabled":false}'
```

### 3.5 创建渠道

**方式一：控制台。** 渠道 → 添加渠道：

| 字段 | 填写 |
|---|---|
| 类型 | Gemini Enterprise |
| Base URL | 留空（默认 `https://discoveryengine.googleapis.com`） |
| 密钥 | 服务账号 JSON 全文；或 `adc`，表示用 new-api 所在机器的运行身份 |
| Gemini Enterprise config（其他） | `{"project_id": "<PROJECT_ID>", "engine_id": "<ENGINE_ID>", "location": "global", "web_grounding": false}` |
| 模型 | `gemini-3.8-flash,gemini-3.7-flash,gemini-2.5-pro`（按 GE 应用实际启用的填写） |

密钥是服务账号 JSON 时，`project_id` 可以不填，默认取 JSON 里的项目。

**方式二：脚本。** `scripts/admin_setup.sh` 登录管理端、创建渠道和一个不限额的测试令牌。令牌写入 `.token`（权限 600），脚本不在输出中打印任何密钥：

```bash
NEWAPI=http://127.0.0.1:3000 ADMIN_USER=admin ADMIN_PASS='<ADMIN_PASS>' \
GE_PROJECT_ID=<PROJECT_ID> GE_ENGINE_ID=<ENGINE_ID> \
GE_KEY=adc GE_MODELS=gemini-3.8-flash,gemini-3.7-flash,gemini-2.5-pro \
./scripts/admin_setup.sh
```

脚本调用的管理 API：

| 步骤 | 接口 |
|---|---|
| 登录 | `POST /api/user/login`，取返回的 `data.access_token` |
| 创建渠道 | `POST /api/channel/`，请求头 `Authorization: Bearer <access_token>`、`New-Api-User: 1`，请求体 `{"mode":"single","channel":{"type":64,"key":"adc","other":"{...}","models":"...","group":"default"}}` |
| 创建令牌 | `POST /api/token/` |
| 读取令牌 | `GET /api/token/` 找到 ID，`POST /api/token/{id}/key` 取明文 |

### 3.6 调用

```python
from openai import OpenAI
c = OpenAI(base_url="http://127.0.0.1:3000/v1", api_key="<NEWAPI_TOKEN>")
for ch in c.chat.completions.create(model="gemini-3.8-flash", stream=True,
        messages=[{"role": "system", "content": "……"}, {"role": "user", "content": "你好"}]):
    if ch.choices:
        print(ch.choices[0].delta.content or "", end="")
```

```bash
# Claude 格式
curl http://127.0.0.1:3000/v1/messages -H "x-api-key: <NEWAPI_TOKEN>" -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'
# Gemini 格式
curl "http://127.0.0.1:3000/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse" \
  -H "x-goog-api-key: <NEWAPI_TOKEN>" -H "Content-Type: application/json" \
  -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}'
```

## 4. 核心代码说明

代码在 `relay/channel/geminienterprise/`，共 5 个文件。

### 4.1 注册渠道（constant / common / relay_adaptor / distributor）

new-api 按“渠道类型 → API 类型 → 适配器”三级分发：

```go
// constant/channel.go
ChannelTypeGEAssist = 64
ChannelTypeGEAssist: "Gemini Enterprise",          // 渠道名
"https://discoveryengine.googleapis.com",          // ChannelBaseURLs[64]，默认 Base URL

// constant/api_type.go
APITypeGEAssist                                     // 加在 APITypeDummy 前

// common/api_type.go
case constant.ChannelTypeGEAssist:
    apiType = constant.APITypeGEAssist

// relay/relay_adaptor.go
case constant.APITypeGEAssist:
    return &geminienterprise.Adaptor{}              // 每个请求新建一个适配器实例

// middleware/distributor.go —— 把渠道“其他”字段交给适配器（info.ApiVersion）
case constant.ChannelTypeGEAssist:
    c.Set("api_version", channel.Other)
```

### 4.2 渠道配置（adaptor.go `loadConfig`）

“其他”字段是 JSON，解析成 `Config{ProjectID, EngineID, Location, WebGrounding}`。`project_id` 缺省时从服务账号 JSON 读取，`location` 缺省为 `global`。缺少 `project_id` 或 `engine_id` 时直接报错，不发请求。

### 4.3 认证（auth.go）

```go
// 密钥 "adc"：GCE / Cloud Run / GKE 元数据服务器
GET http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token
// 密钥是服务账号 JSON：复用 vertex 渠道的 JWT 换 token
token, err = vertex.AcquireAccessToken(*creds, info.ChannelSetting.Proxy)
```

- token 按渠道 ID（多密钥渠道再加密钥序号）缓存。元数据 token 在过期前 5 分钟刷新，服务账号 token 50 分钟刷新一次。
- 缓存键包含服务账号的 `client_email` 和 `private_key_id`。更换密钥后不会复用旧 token。
- `SetupRequestHeader` 设置 `Authorization: Bearer <token>` 和 `X-Goog-User-Project: <project_id>`（计费和配额项目）。

### 4.4 请求转换（relay.go `buildQueryText`、adaptor.go `ConvertOpenAIRequest`）

```go
&AssistRequest{
    Query:          AssistQuery{Text: buildQueryText(request)},
    GenerationSpec: &GenerationSpec{ModelID: modelID(info.UpstreamModelName)},  // 去掉 -low/-medium/-high
    ToolsSpec:      map[string]any{},                                            // {} = 不联网
}
```

`buildQueryText` 的规则：

- 只有一条用户消息时直接发原文。
- 多条消息时按角色加标签：`【系统指令】`、`【用户】`、`【助手】`；最后一条用户消息标为 `【本次用户消息】`。
- `response_format` 为 `json_object` / `json_schema` 时追加"只输出一个 JSON 对象……"；有 schema 时附上 schema。

Claude 和 Gemini 格式的请求，先用 `service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)` 转成 OpenAI 请求，再走同一个 `ConvertOpenAIRequest`。

### 4.5 错误处理（adaptor.go `DoRequest`、relay.go `unwrapErrorBody`）

streamAssist 出错时，返回的是数组 `[{"error":{"code":400,"message":"..."}}]`。new-api 的通用错误处理只认对象，否则只能显示 "bad response status code 400"。`DoRequest` 在状态码非 200 时展开数组第一个元素，客户端就能看到上游原始信息，例如：

```
The model gemini-4-argon is not available during the free trial. Upgrade to a paid subscription or select a different model.
```

响应数组中间出现 `error` 元素，或 `answer.state` 为 `FAILED` / `SKIPPED` 时，同样作为错误返回。

### 4.6 响应解析（relay.go `iterate`、`texts`）

```go
dec := json.NewDecoder(body)
dec.Token()                 // 读掉开头的 '['
for dec.More() {
    var el AssistResponse
    dec.Decode(&el)         // 每读完一个数组元素就处理，真正的流式
    ...
}
```

`json.Decoder` 按 UTF-8 解码，网络分块切断汉字时不会产生乱码。`texts` 从 `answer.replies[].groundedContent.content` 取文本：

- `thought: true` 的是思考摘要。
- 带 `mimeType` 的块是推荐问题等，跳过。

### 4.7 改写成 OpenAI 格式并复用 OpenAI 处理器（relay.go、adaptor.go `DoResponse`）

```go
func (a *Adaptor) DoResponse(c, resp, info) {
    if info.IsStream {
        return openai.OaiStreamHandler(c, info, toOpenAISSE(c, info, resp, a.jsonMode))
    }
    r, _ := toOpenAIJSON(c, info, resp, a.jsonMode)
    return openai.OpenaiHandler(c, info, r)
}
```

- **流式（`toOpenAISSE`）**：
  - 用 `io.Pipe` 构造一个合成的 `http.Response`。后台 goroutine 每解析到一段文本，就写一行 `data: {chat.completion.chunk}`。
  - 写完后依次发出 `finish_reason: stop`、usage 块和 `data: [DONE]`。
  - `OaiStreamHandler` 从管道读取，按客户端格式（OpenAI / Claude / Gemini）实时转发，所以首 token 不会被缓冲。
- **非流式（`toOpenAIJSON`）**：读完整个数组，生成一个 `chat.completion` JSON。
- **JSON 模式**：需要拿到完整回答才能去掉代码块，所以流式请求也是整段作为一个块发出。
- **usage**：
  - streamAssist 不返回 token 数，用 new-api 的 `tokenkit.Estimate` 按文本估算 completion tokens，prompt tokens 用 new-api 预估的值。
  - 流式时显式发出 usage 块。原因是 new-api 在 Claude / Gemini 格式下不会从流文本里累计 token，不发会被记成 0（实测修复前日志里是 0）。

### 4.8 前端

- 新增渠道类型 64：`constants.ts`、`channel-type-config.ts`、`channel-utils.ts` 三处，用 Gemini 图标，并给出密钥和“其他”字段的填写提示。
- `channel-mutate-drawer.tsx`：类型为 64 时显示 "Gemini Enterprise config (JSON)" 多行输入框。

## 5. 测试方法

### 5.1 单元测试

见 3.2，`go test ./relay/channel/geminienterprise/`。

### 5.2 功能测试

```bash
BASE_URL=http://127.0.0.1:3000 API_KEY=$(cat .token) python3 tests/smoke_test.py
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

实测结果：

```
[PASS] 不带 Key 返回 401  401
[PASS] 模型列表  ['gemini-2.5-pro', 'gemini-3.7-flash', 'gemini-3.8-flash', 'gemini-4-argon']
[PASS] OpenAI 非流式  '391' 7.9s
[PASS] OpenAI 流式  566 字 首token 4.3s 总 10.6s
[PASS] 流式中文无乱码
[PASS] 系统指令 + 多轮历史  '4271'
[PASS] JSON Schema  ['answer', 'tags']
[PASS] Claude /v1/messages  ['蓝色']
[PASS] Gemini generateContent  '2'
[INFO] gemini-4-argon（取决于许可证）: HTTP 400 ... The model gemini-4-argon is not available during the free trial ...

9/9 通过
```

### 5.3 流式格式和计费检查

```bash
K=$(cat .token); B=http://127.0.0.1:3000
# Claude 流式事件序列：message_start → content_block_* → message_delta → message_stop
curl -sN $B/v1/messages -H "x-api-key: $K" -H "anthropic-version: 2023-06-01" -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash","max_tokens":200,"stream":true,"messages":[{"role":"user","content":"一句话介绍杭州"}]}' \
  | grep "^event:" | sort | uniq -c
# OpenAI 流式最后的 usage 块
curl -sN $B/v1/chat/completions -H "Authorization: Bearer $K" -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"一句话介绍杭州"}]}' \
  | grep '"usage"'
```

然后在控制台“日志”页（或 `GET /api/log/?type=2`）确认：每条请求都记到 Gemini Enterprise 渠道，且 `completion_tokens` 不为 0。实测 OpenAI、Claude、Gemini 三种格式的流式和非流式请求都有正常的估算值。

### 5.4 延迟测试

```bash
BASE_URL=http://127.0.0.1:3000 API_KEY=$(cat .token) python3 tests/smoke_test.py latency 10
PROMPT_FILE=./my_prompt.txt BASE_URL=... API_KEY=... python3 tests/smoke_test.py latency 10
```

输出首 token、总耗时的中位数和 P90。实测约 4 万字的业务 prompt（gemini-3.8-flash，经 new-api）：

| 模式 | 首 token | 总耗时 | JSON 正确 |
|---|---|---|---|
| JSON（schema） | — | 100.3s、77.3s | 2/2 |
| 流式正文 | 42.5s、50.1s | 50.1s、54.8s | — |

耗时主要在 streamAssist 本身：同一请求直接调 streamAssist 也要几十秒，new-api 的开销可以忽略。

### 5.5 排查

| 现象 | 原因 / 处理 |
|---|---|
| `channel "other" must be JSON...` | “其他”字段不是合法 JSON |
| `requires project_id and engine_id` | “其他”缺少 `engine_id`，或密钥为 `adc` 时没填 `project_id` |
| `metadata server unreachable` | 密钥为 `adc`，但 new-api 没有运行在 GCE / Cloud Run / GKE 上；改用服务账号 JSON |
| 403 `PERMISSION_DENIED` | 调用身份缺 Discovery Engine 权限 |
| 400 `not available during the free trial` | GE 许可证为试用版，模型受限 |
| 400 `Model ... is disabled by default` | GE 应用模型设置中没有启用该模型 |
| 请求超时 | streamAssist 长 prompt 可能超过 1 分钟；调大反向代理和客户端超时，new-api 的 `STREAMING_TIMEOUT` 也要足够大 |

new-api 日志：`docker logs -f new-api-ge`，错误会带上游原始信息和请求 ID。

## 6. 注意事项

- **延迟**：streamAssist 会做规划和思考，长 prompt 首 token 要几十秒，思考不能关闭。对首 token 延迟要求高的场景不适合。
- **usage 是估算值**：streamAssist 不返回 token 数，new-api 里的计费基于本地估算，和 GE 侧的实际计量不一致。
- **会话记录**：每次请求会在 GE 应用中新建一个会话，提示词和回答保存在调用身份的 GE 会话历史中。
- **密钥安全**：
  - 优先在 GCP 上用 `adc`，避免在数据库里保存服务账号私钥。
  - 必须用 JSON 时，给该服务账号最小权限并定期轮换。
- **许可合规**：用一个服务账号代表大量终端用户调用 GE 助手，是否符合 GE 许可条款需要与 Google 确认。
- **维护**：补丁基于 new-api `a1d1fb0`，升级上游时需要重新应用并跑 3.2 的单元测试和 5.2 的功能测试。
