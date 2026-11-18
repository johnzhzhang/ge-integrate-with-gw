# AIClient2API 接入 Gemini Enterprise（Discovery Engine API）部署指南

本文说明如何给开源代理 AIClient2API 增加一个 Discovery Engine（Gemini Enterprise 助手接口 `streamAssist`）提供方，并部署到 Cloud Run，对外提供 OpenAI / Claude / Gemini 兼容接口。内容包括实际执行过的每一步操作和核心代码说明。

文中项目、账号、地址、密钥等均用占位符表示：

| 占位符 | 含义 |
|---|---|
| `<PROJECT_ID>` | GCP 项目 ID |
| `<PROJECT_NUMBER>` | GCP 项目编号 |
| `<ENGINE_ID>` | Gemini Enterprise 应用（engine）ID |
| `<REGION>` | Cloud Run 和 Artifact Registry 所在区域，例如 `us-central1` |
| `<AR_REPO>` | Artifact Registry Docker 仓库名 |
| `<SERVICE_ACCOUNT>` | Cloud Run 运行身份（服务账号邮箱） |
| `<API_KEY>` | 客户端访问代理用的密钥 |
| `<SERVICE_URL>` | Cloud Run 服务地址 |

## 1. 方案概述

### 1.1 为什么要改代码

AIClient2API 是把各类客户端专用模型接口转换成 OpenAI 兼容接口的 Node.js 代理。它自带的 Gemini 提供方都不能用来调 Gemini Enterprise：

- `gemini-antigravity`：调 `cloudcode-pa.googleapis.com`，用 Antigravity 桌面客户端的个人账号 OAuth，是消费者渠道。
- `gemini-cli-oauth`：同样调 `cloudcode-pa`，当前版本已在代码中注释掉。
- `openai-custom`：只能接 OpenAI 兼容接口，Discovery Engine 没有这种接口。

因此需要新增一个提供方 `gemini-ge-assist`，上游调用 Discovery Engine 的 `streamAssist`。

### 1.2 架构

```
客户端（OpenAI / Claude / Gemini SDK）
   │  Authorization: Bearer <API_KEY>
   ▼
AIClient2API（Cloud Run）
   │  ① 内置转换器：OpenAI / Claude 请求 → Gemini 格式
   │  ② gemini-ge-assist：Gemini 请求 → streamAssist 请求
   │                      streamAssist 回答 → Gemini 响应
   │  ③ 内置转换器：Gemini 响应 → OpenAI / Claude 响应
   │  认证：Google ADC（Cloud Run 服务账号）
   ▼
POST https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/
     collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:streamAssist
```

AIClient2API 内部统一用 Gemini 格式调用提供方，所以新提供方只需处理 Gemini 格式；OpenAI、Claude 协议的转换由 AIClient2API 现有的转换器完成。

### 1.3 streamAssist 的限制

streamAssist 是 GE 助手接口，不是模型推理接口：

| 能力 | 支持情况 | 提供方的处理 |
|---|---|---|
| 系统指令、对话历史 | 只接受一段 `query.text` | 拼成带标签的一段文字 |
| JSON Schema | 不支持 | 把 schema 写进文字，回答后去掉 ```json 代码块 |
| 选择模型 | `generationSpec.modelId`，不带档位后缀 | 去掉客户端传的 `-low/-medium/-high` |
| thinking 档位 | 不能设置 | 思考摘要作为 thought 输出 |
| token 用量 | 不返回 | usage 为 0 |
| 联网搜索 | 由 `toolsSpec` 控制 | 默认 `toolsSpec: {}`，不联网 |

## 2. 前提条件

| 项目 | 要求 |
|---|---|
| Gemini Enterprise | 项目内已有 GE 应用 `<ENGINE_ID>`，要用的模型在应用的模型设置中为 `MODEL_ENABLED` |
| 服务账号 | `<SERVICE_ACCOUNT>` 能调用该应用的 streamAssist（例如 `roles/discoveryengine.user`），并有 `roles/serviceusage.serviceUsageConsumer`；按 GE 要求分配许可证 |
| 已启用 API | `discoveryengine.googleapis.com`、`run.googleapis.com`、`artifactregistry.googleapis.com`、`secretmanager.googleapis.com` |
| 构建机 | Docker、gcloud CLI、git |

确认服务账号能调 streamAssist（在以该服务账号运行的 GCE 机器上执行）：

```bash
TOKEN=$(curl -s -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" \
  | python3 -c "import json,sys;print(json.load(sys.stdin)['access_token'])")
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "X-Goog-User-Project: <PROJECT_ID>" \
  -H "Content-Type: application/json" \
  "https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:streamAssist" \
  -d '{"query":{"text":"1+1=? 只回答数字"},"generationSpec":{"modelId":"gemini-3.8-flash"},"toolsSpec":{}}'
```

返回 HTTP 200 且 `answer.replies` 中有文本即可。查看应用启用了哪些模型：

```bash
curl -s -H "Authorization: Bearer $(gcloud auth print-access-token)" -H "X-Goog-User-Project: <PROJECT_ID>" \
  "https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/collections/default_collection/engines/<ENGINE_ID>" \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('modelConfigs'))"
```

## 3. 部署步骤

### 3.1 获取源码并打补丁

```bash
git clone https://github.com/justlovemaki/AIClient2API.git
cd AIClient2API
git checkout 31b8d90                       # 本文基于的上游版本（3.6.8）
git apply /path/to/ge-assist-provider.patch
```

补丁改动 5 个文件（详见第 4 节）：

| 文件 | 改动 |
|---|---|
| `src/providers/gemini/ge-assist-core.js` | 新增，提供方实现 |
| `src/providers/adapter.js` | 新增适配器类并注册 |
| `src/utils/constants.js` | 新增常量 `GE_ASSIST: 'gemini-ge-assist'` |
| `src/providers/provider-models.js` | 模型列表 |
| `src/providers/provider-pool-manager.js` | 健康检查默认模型 |

### 3.2 构建基础镜像

```bash
docker build -t aiclient2api-ge:3.6.8 .
```

使用上游 Dockerfile，不做修改。

### 3.3 本机验证（可选）

```bash
D=$HOME/aiclient2api-ge && mkdir -p $D/configs $D/plugins $D/.private
KEY=$(python3 -c "import secrets;print('sk-ge-'+secrets.token_hex(24))")
cat > $D/configs/config.json <<JSON
{
  "REQUIRED_API_KEY": "$KEY",
  "SERVER_PORT": 3000,
  "HOST": "0.0.0.0",
  "MODEL_PROVIDER": "gemini-ge-assist",
  "GE_PROJECT_ID": "<PROJECT_ID>",
  "GE_ENGINE_ID": "<ENGINE_ID>",
  "GE_WEB_GROUNDING": false,
  "REQUEST_MAX_RETRIES": 1,
  "PROMPT_LOG_MODE": "none"
}
JSON
chmod 600 $D/configs/config.json

docker run -d --name aiclient2api-ge -p 127.0.0.1:3000:3000 \
  -v $D/configs:/app/configs -v $D/plugins:/app/src/plugins-user -v $D/.private:/app/.private \
  aiclient2api-ge:3.6.8

curl -s http://127.0.0.1:3000/v1/models -H "Authorization: Bearer $KEY"
```

在 GCE 上运行时，容器通过元数据服务器自动拿到机器服务账号的凭证。其他环境可挂载服务账号 key，并在配置中加 `"GE_CREDENTIALS_FILE": "/app/configs/sa.json"`。

### 3.4 构建 Cloud Run 镜像

Cloud Run 不方便挂载可写的配置目录，所以加一层启动脚本，用环境变量生成 `configs/config.json`。

`cloudrun/entrypoint.sh`：

```sh
#!/bin/sh
# Cloud Run 启动：用环境变量生成 configs/config.json，API Key 来自 Secret Manager 注入的 API_KEY
set -e
mkdir -p /app/configs
cat > /app/configs/config.json <<JSON
{
  "SERVER_PORT": ${PORT:-8080},
  "HOST": "0.0.0.0",
  "MODEL_PROVIDER": "gemini-ge-assist",
  "GE_PROJECT_ID": "${GE_PROJECT_ID}",
  "GE_ENGINE_ID": "${GE_ENGINE_ID}",
  "GE_WEB_GROUNDING": ${GE_WEB_GROUNDING:-false},
  "REQUEST_MAX_RETRIES": 1,
  "PROMPT_LOG_MODE": "none",
  "LOG_ENABLED": true
}
JSON
exec node src/core/master.js --api-key "${API_KEY:?API_KEY not set}" --port "${PORT:-8080}" --no-ui
```

- API Key 通过 `--api-key` 参数传入，不写进配置文件。
- `--port` 使用 Cloud Run 注入的 `PORT`。
- `--no-ui` 关闭 Web 管理控制台，避免暴露到公网。

`cloudrun/Dockerfile`：

```dockerfile
FROM aiclient2api-ge:3.6.8
COPY cloudrun/entrypoint.sh /app/cloudrun-entrypoint.sh
HEALTHCHECK NONE
ENTRYPOINT ["/app/cloudrun-entrypoint.sh"]
```

`HEALTHCHECK NONE` 关掉上游镜像里针对 3000 端口的 Docker 健康检查，Cloud Run 用自己的启动探测。

构建并推送：

```bash
IMAGE=<REGION>-docker.pkg.dev/<PROJECT_ID>/<AR_REPO>/aiclient2api-ge:3.6.8-assist
chmod +x cloudrun/entrypoint.sh
docker build -f cloudrun/Dockerfile -t $IMAGE .
gcloud auth configure-docker <REGION>-docker.pkg.dev -q
docker push $IMAGE
```

### 3.5 在 Secret Manager 中保存 API Key

```bash
gcloud services enable secretmanager.googleapis.com run.googleapis.com --project <PROJECT_ID>

python3 -c "import secrets;print('sk-ge-'+secrets.token_hex(24), end='')" \
  | gcloud secrets create aiclient2api-ge-api-key --project <PROJECT_ID> \
      --replication-policy=automatic --data-file=-

# 只给运行身份读取这一个密钥的权限
gcloud secrets add-iam-policy-binding aiclient2api-ge-api-key --project <PROJECT_ID> \
  --member="serviceAccount:<SERVICE_ACCOUNT>" --role=roles/secretmanager.secretAccessor
```

### 3.6 部署 Cloud Run

```bash
gcloud run deploy aiclient2api-ge --project <PROJECT_ID> --region <REGION> \
  --image $IMAGE \
  --service-account <SERVICE_ACCOUNT> \
  --port 8080 --cpu 1 --memory 1Gi \
  --timeout 900 --concurrency 40 --min-instances 0 --max-instances 3 \
  --set-env-vars GE_PROJECT_ID=<PROJECT_ID>,GE_ENGINE_ID=<ENGINE_ID>,GE_WEB_GROUNDING=false \
  --set-secrets API_KEY=aiclient2api-ge-api-key:latest \
  --allow-unauthenticated
```

| 参数 | 取值原因 |
|---|---|
| `--timeout 900` | streamAssist 处理长 prompt 实测可超过 1 分钟 |
| `--concurrency 40` | 请求大部分时间在等上游，单实例可承载多个并发 |
| `--min-instances 0` | 空闲不计费；需要避免冷启动时改为 1 |
| `--set-secrets` | 密钥以环境变量注入，不进入镜像和部署配置 |
| `--allow-unauthenticated` | 让 OpenAI 等客户端直接用 API Key 访问；鉴权由 AIClient2API 的 API Key 完成 |

### 3.7 验证

```bash
URL=<SERVICE_URL>
KEY=$(gcloud secrets versions access latest --secret aiclient2api-ge-api-key --project <PROJECT_ID>)

curl -s -o /dev/null -w "%{http_code}\n" $URL/v1/models                       # 401，未带 Key
curl -s $URL/v1/models -H "Authorization: Bearer $KEY"                          # 模型列表
curl -s $URL/v1/chat/completions -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"17*23=? 只回答数字"}]}'
```

查看上游调用日志（每次请求打印 `model -> modelId`、`queryChars`）：

```bash
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="aiclient2api-ge" AND textPayload:"GE Assist"' \
  --project <PROJECT_ID> --limit 20 --format="value(textPayload)"
```

### 3.8 更新与清理

```bash
# 代码更新后：重新执行 3.2、3.4 构建推送，再执行
gcloud run deploy aiclient2api-ge --project <PROJECT_ID> --region <REGION> --image $IMAGE

# 轮换 API Key：新增版本后重新部署一次，让新实例读到 latest
python3 -c "import secrets;print('sk-ge-'+secrets.token_hex(24), end='')" \
  | gcloud secrets versions add aiclient2api-ge-api-key --project <PROJECT_ID> --data-file=-

# 清理
gcloud run services delete aiclient2api-ge --project <PROJECT_ID> --region <REGION>
gcloud secrets delete aiclient2api-ge-api-key --project <PROJECT_ID>
```

## 4. 核心代码说明

### 4.1 提供方在 AIClient2API 中的位置

AIClient2API 处理一次请求的流程：

1. 根据请求路径判断客户端协议（`/v1/chat/completions` 为 OpenAI，`/v1/messages` 为 Claude，`/v1beta/models/...` 为 Gemini）。
2. 根据 `MODEL_PROVIDER` 找到提供方。提供方名字以 `gemini-` 开头，AIClient2API 按前缀把它归为 Gemini 协议。
3. 客户端协议与提供方协议不同时，用转换器把请求转成 Gemini 格式，调用适配器的 `generateContent` / `generateContentStream`，再把结果转回客户端协议。

因此提供方只需实现 Gemini 格式的 4 个方法：`generateContent`、`generateContentStream`、`listModels`、`isExpiryDateNear`。

### 4.2 注册（constants.js、adapter.js、provider-models.js）

```js
// src/utils/constants.js —— 新增提供方名
export const MODEL_PROVIDER = {
    ...
    GE_ASSIST: 'gemini-ge-assist',
};
```

```js
// src/providers/adapter.js —— 适配器只做初始化检查和转发
import { GeAssistApiService } from './gemini/ge-assist-core.js';

export class GeAssistApiServiceAdapter extends ApiServiceAdapter {
    constructor(config) {
        super();
        this.svc = new GeAssistApiService(config);
    }
    async _ensure() { if (!this.svc.isInitialized) await this.svc.initialize(); }
    async generateContent(model, requestBody) { await this._ensure(); return this.svc.generateContent(model, requestBody); }
    async *generateContentStream(model, requestBody) { await this._ensure(); yield* this.svc.generateContentStream(model, requestBody); }
    async listModels() { return this.svc.listModels(); }
    async refreshToken() { return false; }        // token 由 google-auth-library 自动刷新
    async forceRefreshToken() { return false; }
    isExpiryDateNear() { return false; }
}

registerAdapter(MODEL_PROVIDER.GE_ASSIST, GeAssistApiServiceAdapter);
```

```js
// src/providers/provider-models.js —— /v1/models 返回的模型列表
'gemini-ge-assist': [
    'gemini-3.8-flash', 'gemini-3.7-flash', 'gemini-3.6-flash', 'gemini-3.1-pro-preview', 'gemini-2.5-pro', 'gemini-4-argon',
],
```

```js
// src/providers/provider-pool-manager.js —— 账号池健康检查默认模型
'gemini-ge-assist': 'gemini-3.8-flash',
```

模型列表只影响 `/v1/models` 的展示，实际能否调用取决于 GE 应用的模型设置和许可证。

### 4.3 提供方实现（ge-assist-core.js）

#### 认证

```js
const authOpts = { scopes: ['https://www.googleapis.com/auth/cloud-platform'] };
if (config.GE_CREDENTIALS_FILE) authOpts.keyFile = config.GE_CREDENTIALS_FILE;
this.auth = new GoogleAuth(authOpts);
...
const { token } = await (await this.auth.getClient()).getAccessToken();
```

使用 `google-auth-library`（AIClient2API 已依赖）的 ADC：

- 在 Cloud Run / GCE 上自动从元数据服务器取运行身份的 token。
- 也可以用 `GOOGLE_APPLICATION_CREDENTIALS` 或 `GE_CREDENTIALS_FILE` 指定服务账号 key。
- token 由库缓存，过期前自动刷新，每次请求调用 `getAccessToken()` 不会重复申请。

#### 请求转换：`buildQueryText`

streamAssist 只有 `query.text`，所以把系统指令、历史消息和本次消息拼成一段带标签的文字：

```
【系统指令】
……

【助手】
……（历史）

【本次用户消息】
……
```

- 只有一条用户消息、没有系统指令时，直接发原文，不加标签。
- thought 类型的 part（历史中的思考内容）不拼进去。
- 请求了 JSON（`responseMimeType: application/json`）时，在末尾追加"只输出一个 JSON 对象……"，有 schema 时把 schema 一并写入。OpenAI 的 `response_format: json_schema` 会被转换器转成这两个字段。

#### 请求体：`_open`

```js
const body = {
    query: { text: this.buildQueryText(req) },
    generationSpec: { modelId: this.modelId(model) },               // 去掉 -low/-medium/-high
    toolsSpec: this.webGrounding ? { webGroundingSpec: {} } : {},   // {} = 不联网
};
axios.post(this.url, body, {
    headers: { Authorization: `Bearer ${token}`, 'X-Goog-User-Project': this.projectId },
    responseType: 'stream',
});
```

- `toolsSpec: {}` 时助手不调用搜索工具；传 `webGroundingSpec` 会调用 `google_search_tool`。
- `X-Goog-User-Project` 指定计费和配额项目。
- 上游返回错误时读出错误体写进异常信息。例如 `The model gemini-4-argon is not available during the free trial` 会原样返回给客户端，方便排查。

#### 响应解析：`iterJsonArray`

streamAssist 的流式响应是一个逐步输出的 JSON 数组 `[{...},{...},...]`，不是 SSE。函数逐字符扫描，记录括号深度和是否在字符串内，每凑齐一个顶层元素就 `JSON.parse` 并产出，从而实现真正的流式。

```js
const dec = new StringDecoder('utf8');
for await (const chunk of stream) {
    for (const ch of dec.write(chunk)) { ... }
}
```

网络分块可能把一个汉字（UTF-8 3 字节）切在两块里。直接 `chunk.toString()` 会出现乱码（实测出现过）；`StringDecoder` 会把不完整的字节留到下一块再解码。

#### 响应转换：`_toGemini`

每个 streamAssist 元素的 `answer.replies[].groundedContent.content` 转成一个 Gemini 响应块：

| streamAssist | 转换结果 |
|---|---|
| `text` + `thought: true` | `{text, thought: true}`，OpenAI 协议下进 `reasoning_content` |
| `text` | `{text}`，正文 |
| 带 `mimeType`（推荐问题等） | 跳过 |
| `error`，或 `answer.state` 为 `FAILED` / `SKIPPED` | 抛异常 |

#### 非流式与 JSON：`generateContent`

- 读完整个数组，拼接正文和思考摘要。
- 去掉回答末尾固定带的 `\n\n`。
- JSON 模式下用 `stripFence` 去掉外层 ```json 代码块。
- 流式请求如果是 JSON 模式，也走这个方法整段返回（`generateContentStream` 第一段判断）。因为只有拿到完整回答才能可靠地去掉代码块。

### 4.4 完整源码

```js
/**
 * Gemini Enterprise via Discovery Engine API（assistants/default_assistant:streamAssist）。
 *
 * streamAssist 是 GE 助手接口，只接受一段 query.text，没有 systemInstruction / contents 历史 /
 * generationConfig / JSON Schema，也不返回 token 用量。本提供方把 Gemini 请求压平成一段文字：
 *   【系统指令】… 【用户】… 【助手】… （历史）… 最后一条用户消息
 * 请求了 JSON（responseMimeType=application/json）时，在末尾加格式要求，并去掉回答外层的 ```json 代码块。
 *
 * 模型：generationSpec.modelId，不带档位后缀（客户端传 -low/-medium/-high 会被去掉）。
 * 认证：Google ADC（GCE/Cloud Run 服务账号，或 GE_CREDENTIALS_FILE）。
 */
import axios from 'axios';
import { StringDecoder } from 'string_decoder';
import { GoogleAuth } from 'google-auth-library';
import logger from '../../utils/logger.js';
import { getProviderModels } from '../provider-models.js';
import { MODEL_PROVIDER } from '../../utils/common.js';

const BASE = 'https://discoveryengine.googleapis.com/v1alpha';
const ROLE = { user: '用户', model: '助手', assistant: '助手' };

function partsText(parts) {
    return (parts || []).filter(p => typeof p?.text === 'string' && !p.thought).map(p => p.text).join('');
}

function stripFence(t) {
    const m = String(t).trim().match(/^```(?:json)?\s*([\s\S]*?)\s*```$/i);
    return m ? m[1] : t;
}

/** 增量解析 streamAssist 返回的 JSON 数组 `[{...},{...}]`，每凑齐一个元素就产出 */
async function* iterJsonArray(stream) {
    let depth = 0, inStr = false, esc = false, buf = '';
    const dec = new StringDecoder('utf8');     // 网络分块可能切断多字节汉字，必须用 StringDecoder
    for await (const chunk of stream) {
        for (const ch of dec.write(chunk)) {
            if (depth === 0) { if (ch === '[') depth = 1; continue; }
            if (depth === 1 && ch !== '{') { if (ch === ']') depth = 0; continue; }
            buf += ch;
            if (inStr) { if (esc) esc = false; else if (ch === '\\') esc = true; else if (ch === '"') inStr = false; continue; }
            if (ch === '"') inStr = true;
            else if (ch === '{') depth++;
            else if (ch === '}') { depth--; if (depth === 1) { yield JSON.parse(buf); buf = ''; } }
        }
    }
}

export class GeAssistApiService {
    constructor(config) {
        this.config = config;
        this.projectId = config.GE_PROJECT_ID || process.env.GE_PROJECT_ID;
        this.engineId = config.GE_ENGINE_ID || process.env.GE_ENGINE_ID;
        if (!this.projectId || !this.engineId) throw new Error('GE_PROJECT_ID and GE_ENGINE_ID are required for gemini-ge-assist');
        this.location = config.GE_LOCATION || 'global';
        this.webGrounding = config.GE_WEB_GROUNDING === true;
        this.timeout = config.GE_TIMEOUT_MS || 600000;
        const authOpts = { scopes: ['https://www.googleapis.com/auth/cloud-platform'] };
        if (config.GE_CREDENTIALS_FILE) authOpts.keyFile = config.GE_CREDENTIALS_FILE;
        this.auth = new GoogleAuth(authOpts);
        this.url = `${BASE}/projects/${this.projectId}/locations/${this.location}/collections/default_collection/engines/${this.engineId}/assistants/default_assistant:streamAssist`;
        this.isInitialized = false;
    }

    async initialize() {
        await this._token();
        this.isInitialized = true;
        logger.info(`[GE Assist] project=${this.projectId} engine=${this.engineId} webGrounding=${this.webGrounding}`);
    }

    async _token() {
        const { token } = await (await this.auth.getClient()).getAccessToken();
        if (!token) throw new Error('[GE Assist] failed to obtain access token');
        return token;
    }

    modelId(model) {
        return String(model || '').replace(/^models\//, '').replace(/-(low|medium|high)$/, '');
    }

    isJson(req) {
        const gc = req?.generationConfig || req?.generation_config || {};
        return gc.responseMimeType === 'application/json' || gc.response_mime_type === 'application/json';
    }

    /** 把 Gemini 请求压平成 query.text */
    buildQueryText(req) {
        const sys = req?.systemInstruction || req?.system_instruction;
        const contents = req?.contents || [];
        const out = [];
        const sysText = sys ? partsText(sys.parts) : '';
        if (sysText) out.push(`【系统指令】\n${sysText}`);
        contents.forEach((c, i) => {
            const t = partsText(c.parts);
            if (!t) return;
            const last = i === contents.length - 1 && (c.role || 'user') === 'user';
            out.push(contents.length === 1 && !sysText ? t : `【${last ? '本次用户消息' : (ROLE[c.role] || '用户')}】\n${t}`);
        });
        if (this.isJson(req)) {
            const gc = req.generationConfig || req.generation_config;
            const schema = gc.responseJsonSchema || gc.responseSchema || gc.response_json_schema || gc.response_schema;
            out.push('只输出一个 JSON 对象，不要用 Markdown 代码块包裹，不要输出 JSON 以外的任何文字。'
                + (schema ? `JSON 必须符合以下 JSON Schema：\n${JSON.stringify(schema)}` : ''));
        }
        return out.join('\n\n');
    }

    async _open(model, req) {
        const body = {
            query: { text: this.buildQueryText(req) },
            generationSpec: { modelId: this.modelId(model) },
            toolsSpec: this.webGrounding ? { webGroundingSpec: {} } : {},   // {} = 不联网
        };
        logger.info(`[GE Assist] model=${model} -> modelId=${body.generationSpec.modelId} queryChars=${body.query.text.length} json=${this.isJson(req)}`);
        try {
            return await axios.post(this.url, body, {
                headers: { Authorization: `Bearer ${await this._token()}`, 'X-Goog-User-Project': this.projectId, 'Content-Type': 'application/json' },
                timeout: this.timeout, responseType: 'stream',
            });
        } catch (error) {
            if (error.response?.data) {
                let s = ''; for await (const c of error.response.data) s += c.toString('utf8');
                error.message = `[GE Assist] HTTP ${error.response.status}: ${s.slice(0, 800)}`;
            }
            logger.error(error.message);
            throw error;
        }
    }

    /** streamAssist 元素 → Gemini 响应块（只取文本；思考摘要作为 thought part） */
    *_toGemini(el) {
        if (el.error) throw new Error(`[GE Assist] ${JSON.stringify(el.error)}`);
        const ans = el.answer || {};
        if (ans.state === 'FAILED' || ans.state === 'SKIPPED') throw new Error(`[GE Assist] answer ${ans.state}: ${JSON.stringify(ans.assistSkippedReasons || [])}`);
        for (const r of ans.replies || []) {
            const c = r.groundedContent?.content || {};
            if (typeof c.text !== 'string' || c.mimeType) continue;          // 跳过推荐问题等非文本
            yield { candidates: [{ index: 0, content: { role: 'model', parts: [c.thought ? { text: c.text, thought: true } : { text: c.text }] } }] };
        }
    }

    async generateContent(model, req) {
        const r = await this._open(model, req);
        let text = '', thoughts = '', session;
        for await (const el of iterJsonArray(r.data)) {
            session = el.sessionInfo?.session || session;
            for (const g of this._toGemini(el)) {
                const p = g.candidates[0].content.parts[0];
                if (p.thought) thoughts += p.text; else text += p.text;
            }
        }
        text = text.replace(/\s+$/, '');      // streamAssist 回答末尾固定带 \n\n
        if (this.isJson(req)) text = stripFence(text);
        const parts = []; if (thoughts) parts.push({ text: thoughts, thought: true }); parts.push({ text });
        return { candidates: [{ index: 0, finishReason: 'STOP', content: { role: 'model', parts } }], modelVersion: this.modelId(model), geAssistSession: session };
    }

    async *generateContentStream(model, req) {
        if (this.isJson(req)) {           // JSON 需要整体去掉代码块，只能整段返回
            yield await this.generateContent(model, req);
            return;
        }
        const r = await this._open(model, req);
        for await (const el of iterJsonArray(r.data)) yield* this._toGemini(el);
        yield { candidates: [{ index: 0, finishReason: 'STOP', content: { role: 'model', parts: [{ text: '' }] } }], modelVersion: this.modelId(model) };
    }

    async listModels() {
        return { models: getProviderModels(MODEL_PROVIDER.GE_ASSIST).map(id => ({
            name: `models/${id}`, version: '1.0.0', displayName: id, description: `Gemini Enterprise streamAssist ${id}`,
            inputTokenLimit: 1048576, outputTokenLimit: 65536, supportedGenerationMethods: ['generateContent', 'streamGenerateContent'],
        })) };
    }

    isExpiryDateNear() { return false; }
}
```

## 5. 测试方法

测试分三层：先确认 GE 侧可用（不经过代理），再测代理的各项功能，最后测延迟。测试脚本 `smoke_test.py` 只依赖 `requests`，通过环境变量指定服务地址和 Key，不在脚本里写任何项目信息。

### 5.1 准备

```bash
pip install requests
export BASE_URL=<SERVICE_URL>          # 本机测试用 http://127.0.0.1:3000
export API_KEY=$(gcloud secrets versions access latest --secret aiclient2api-ge-api-key --project <PROJECT_ID>)
export MODEL=gemini-3.8-flash          # 可选
```

### 5.2 第一层：直连 streamAssist

先用第 2 节的 curl 命令，以服务账号身份直接调 streamAssist。这一层失败说明是 GE 侧的问题（权限、许可证、模型未启用），与代理无关：

| 直连返回 | 原因 | 处理 |
|---|---|---|
| 403 `PERMISSION_DENIED` | 服务账号缺 Discovery Engine 权限 | 授予 `roles/discoveryengine.user` |
| 403 `serviceusage.services.use` | 缺 Service Usage Consumer | 授予 `roles/serviceusage.serviceUsageConsumer` |
| 400 `not available during the free trial` | 许可证为免费试用，模型受限 | 换付费许可证，或换模型 |
| 400 `Model ... is disabled by default` | 应用模型设置中未启用 | 管理员在 GE 应用中启用 |
| 400 `model id ... is invalid` | 模型 ID 带了档位后缀或拼写错误 | 用不带后缀的 ID |

### 5.3 第二层：功能测试

```bash
python3 smoke_test.py
```

脚本依次检查：

| 用例 | 请求 | 通过条件 |
|---|---|---|
| 鉴权 | 不带 Key 请求 `/v1/models` | 返回 401 |
| 模型列表 | `GET /v1/models` | 包含 `MODEL` |
| OpenAI 非流式 | "17*23=?" | 回答含 391 |
| OpenAI 流式 | 三段话介绍杭州 | 正文 > 50 字 |
| 流式中文 | 同上 | 正文没有 `\ufffd`（UTF-8 分块解码正确） |
| 系统指令 + 多轮历史 | system + 第 1 轮"记住 4271"+ 第 3 轮提问 | 回答含 4271（历史被正确拼进 query.text） |
| JSON Schema | `response_format: json_schema` | 能被 `json.loads` 解析，且包含 required 字段 |
| Claude 协议 | `POST /v1/messages` | 返回 text 内容 |
| Gemini 协议 | `POST /v1beta/models/{MODEL}:generateContent` | 回答含 2 |
| argon（信息） | `gemini-4-argon` | 只打印结果，不计入通过数；结果取决于许可证 |

全部通过时退出码为 0，可以放进 CI 或部署后检查。实测输出：

```
[PASS] 不带 Key 返回 401  401
[PASS] 模型列表  ['gemini-2.5-pro', 'gemini-3.1-pro-preview', 'gemini-3.6-flash', 'gemini-3.7-flash', 'gemini-3.8-flash', 'gemini-4-argon']
[PASS] OpenAI 非流式  '391' 7.4s
[PASS] OpenAI 流式  626 字 首token 2.8s 总 7.7s
[PASS] 流式中文无乱码
[PASS] 系统指令 + 多轮历史  '4271'
[PASS] JSON Schema  ['answer', 'tags']
[PASS] Claude /v1/messages  ['蓝色']
[PASS] Gemini generateContent  '2'
[INFO] gemini-4-argon（取决于许可证）: HTTP 400 ... not available during the free trial ...

9/9 通过
```

### 5.4 第三层：延迟测试

```bash
python3 smoke_test.py latency 10                                # 默认 prompt：用 300 字介绍杭州
PROMPT_FILE=./my_prompt.txt python3 smoke_test.py latency 10    # 用自己的长 prompt
```

每次打印首 token、总耗时、正文字数和思考摘要字数，最后汇总中位数和 P90。实测（短 prompt，3 次）：

```
#1 HTTP 200 首token 3.76s 总 6.72s 正文 438 字 思考摘要 0 字
#2 HTTP 200 首token 6.86s 总 9.00s 正文 390 字 思考摘要 66 字
#3 HTTP 200 首token 9.41s 总 12.24s 正文 367 字 思考摘要 131 字

首 token 中位数 6.86s P90 9.41s；总耗时中位数 9.00s P90 12.24s（3/3 成功）
```

- 思考摘要越长，首 token 越慢；streamAssist 不能关闭思考，所以延迟波动大。
- 约 4 万字的业务 prompt 实测首 token 14~100 秒。评估时至少跑 10 次看 P90。
- 区分代理开销和上游耗时：对同一 prompt，用第 2 节的 curl 直连 streamAssist 计时，与经代理的结果对比。

### 5.5 排查

| 现象 | 查看 |
|---|---|
| 客户端报 500 / 502 | Cloud Run 日志中的 `[GE Assist] HTTP xxx: ...`，即上游原始错误 |
| 回答与系统指令不符 | 日志 `queryChars` 是否包含系统指令长度；检查客户端是否把系统指令放在 `system` 角色 |
| 请求超时 | Cloud Run `--timeout` 和客户端超时是否大于上游耗时（建议 ≥ 900 秒） |
| 冷启动慢 | 首次请求多出几秒，可设 `--min-instances 1` |

```bash
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="aiclient2api-ge" AND severity>=ERROR' \
  --project <PROJECT_ID> --limit 20 --format="value(textPayload)"
```

### 5.6 测试脚本 smoke_test.py

```python
"""AIClient2API（gemini-ge-assist）功能与延迟测试。

用法:
  BASE_URL=<SERVICE_URL> API_KEY=<API_KEY> python3 smoke_test.py            # 功能测试
  BASE_URL=<SERVICE_URL> API_KEY=<API_KEY> python3 smoke_test.py latency 5  # 延迟测试，5 次
可选: MODEL（默认 gemini-3.8-flash）、PROMPT_FILE（延迟测试用的长 prompt 文本文件）
"""
import json, os, statistics, sys, time
import requests

BASE = os.environ["BASE_URL"].rstrip("/")
KEY = os.environ["API_KEY"]
MODEL = os.environ.get("MODEL", "gemini-3.8-flash")
H = {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"}


def chat(body, stream=False, headers=H, path="/v1/chat/completions"):
    """返回 (status, 正文, reasoning 字符数, 首 token 秒, 总秒)"""
    t0 = time.time()
    r = requests.post(BASE + path, headers=headers, json=body, stream=stream, timeout=900)
    if not stream:
        return r.status_code, r.json(), None, None, time.time() - t0
    r.encoding = "utf-8"
    text, rc, ttft = "", 0, None
    for line in r.iter_lines(decode_unicode=True):
        if not line.startswith("data:") or "[DONE]" in line:
            continue
        for c in json.loads(line[5:]).get("choices", []):
            d = c.get("delta", {})
            rc += len(d.get("reasoning_content") or "")
            if d.get("content"):
                ttft = ttft or time.time() - t0
                text += d["content"]
    return r.status_code, text, rc, ttft, time.time() - t0


def check(name, ok, detail=""):
    print(f"[{'PASS' if ok else 'FAIL'}] {name}  {detail}")
    return ok


def functional():
    res = []
    r = requests.get(BASE + "/v1/models", timeout=30)
    res.append(check("不带 Key 返回 401", r.status_code == 401, r.status_code))

    r = requests.get(BASE + "/v1/models", headers=H, timeout=30)
    ids = [m["id"] for m in r.json().get("data", [])]
    res.append(check("模型列表", r.ok and MODEL in ids, ids))

    s, d, *_ , t = chat({"model": MODEL, "messages": [{"role": "user", "content": "17*23=? 只回答数字"}]})
    out = d["choices"][0]["message"]["content"] if s == 200 else d
    res.append(check("OpenAI 非流式", s == 200 and "391" in out, f"{out!r} {t:.1f}s"))

    s, text, rc, ttft, t = chat({"model": MODEL, "stream": True,
                                 "messages": [{"role": "user", "content": "用三段话介绍杭州的历史、美食和景点"}]}, stream=True)
    res.append(check("OpenAI 流式", s == 200 and len(text) > 50, f"{len(text)} 字 首token {ttft or 0:.1f}s 总 {t:.1f}s"))
    res.append(check("流式中文无乱码", "\ufffd" not in text))

    s, d, *_ = chat({"model": MODEL, "messages": [
        {"role": "system", "content": "你只能用一个词回答。"},
        {"role": "user", "content": "记住数字 4271"}, {"role": "assistant", "content": "好的。"},
        {"role": "user", "content": "我让你记住的数字是多少？只回答数字"}]})
    out = d["choices"][0]["message"]["content"] if s == 200 else d
    res.append(check("系统指令 + 多轮历史", s == 200 and "4271" in out, repr(out)))

    schema = {"type": "object", "properties": {"answer": {"type": "string"}, "tags": {"type": "array", "items": {"type": "string"}}},
              "required": ["answer", "tags"]}
    s, d, *_ = chat({"model": MODEL, "messages": [{"role": "user", "content": "介绍 Cloud Spanner"}],
                     "response_format": {"type": "json_schema", "json_schema": {"name": "a", "schema": schema}}})
    try:
        obj = json.loads(d["choices"][0]["message"]["content"]); ok = set(schema["required"]) <= set(obj)
    except Exception as e:
        obj, ok = str(e), False
    res.append(check("JSON Schema", s == 200 and ok, list(obj) if isinstance(obj, dict) else obj))

    s, d, *_ = chat({"model": MODEL, "max_tokens": 100, "messages": [{"role": "user", "content": "说一个颜色，只说一个词"}]},
                    headers={"x-api-key": KEY, "anthropic-version": "2023-06-01", "Content-Type": "application/json"}, path="/v1/messages")
    out = [c.get("text") for c in d.get("content", []) if c.get("type") == "text"] if s == 200 else d
    res.append(check("Claude /v1/messages", s == 200 and bool(out), out))

    r = requests.post(f"{BASE}/v1beta/models/{MODEL}:generateContent", headers={"x-goog-api-key": KEY, "Content-Type": "application/json"},
                      json={"contents": [{"role": "user", "parts": [{"text": "1+1=? 只回答数字"}]}]}, timeout=300)
    out = "".join(p.get("text", "") for p in r.json().get("candidates", [{}])[0].get("content", {}).get("parts", []) if not p.get("thought")) if r.ok else r.text[:200]
    res.append(check("Gemini generateContent", r.ok and "2" in out, repr(out)))

    s, d, *_ = chat({"model": "gemini-4-argon", "messages": [{"role": "user", "content": "hi"}]})
    msg = json.dumps(d, ensure_ascii=False)[:160]
    print(f"[INFO] gemini-4-argon（取决于许可证）: HTTP {s} {msg}")

    print(f"\n{sum(res)}/{len(res)} 通过")
    return all(res)


def latency(n):
    prompt = open(os.environ["PROMPT_FILE"], encoding="utf-8").read() if os.environ.get("PROMPT_FILE") else "用 300 字介绍杭州。"
    ttfts, totals = [], []
    for i in range(n):
        s, text, rc, ttft, t = chat({"model": MODEL, "stream": True, "messages": [{"role": "user", "content": prompt}]}, stream=True)
        print(f"#{i+1} HTTP {s} 首token {ttft or 0:.2f}s 总 {t:.2f}s 正文 {len(text)} 字 思考摘要 {rc} 字")
        if s == 200 and ttft:
            ttfts.append(ttft); totals.append(t)
    if ttfts:
        q = lambda v, p: sorted(v)[min(len(v) - 1, round(p / 100 * (len(v) - 1)))]
        print(f"\n首 token 中位数 {statistics.median(ttfts):.2f}s P90 {q(ttfts, 90):.2f}s；总耗时中位数 {statistics.median(totals):.2f}s P90 {q(totals, 90):.2f}s（{len(ttfts)}/{n} 成功）")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "latency":
        latency(int(sys.argv[2]) if len(sys.argv) > 2 else 5)
    else:
        sys.exit(0 if functional() else 1)
```

## 6. 客户端调用

```python
from openai import OpenAI
client = OpenAI(base_url="<SERVICE_URL>/v1", api_key="<API_KEY>")

# 流式
for ch in client.chat.completions.create(
        model="gemini-3.8-flash", stream=True,
        messages=[{"role": "system", "content": "……"}, {"role": "user", "content": "你好"}]):
    if ch.choices:
        print(ch.choices[0].delta.content or "", end="")

# JSON
r = client.chat.completions.create(
    model="gemini-3.8-flash",
    messages=[{"role": "user", "content": "介绍 Cloud Spanner"}],
    response_format={"type": "json_schema", "json_schema": {"name": "a", "schema": {
        "type": "object", "properties": {"answer": {"type": "string"}}, "required": ["answer"]}}})
print(r.choices[0].message.content)
```

```bash
# Claude 协议
curl <SERVICE_URL>/v1/messages -H "x-api-key: <API_KEY>" -H "anthropic-version: 2023-06-01"   -H "Content-Type: application/json"   -d '{"model":"gemini-3.8-flash","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'

# Gemini 协议
curl "<SERVICE_URL>/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse"   -H "x-goog-api-key: <API_KEY>" -H "Content-Type: application/json"   -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}'
```

## 7. 配置项

| 配置 | 默认 | 说明 |
|---|---|---|
| `GE_PROJECT_ID` | 必填 | GE 所在项目 |
| `GE_ENGINE_ID` | 必填 | GE 应用 ID |
| `GE_LOCATION` | `global` | GE 应用所在位置 |
| `GE_WEB_GROUNDING` | `false` | 是否允许助手联网搜索 |
| `GE_CREDENTIALS_FILE` | 无 | 服务账号 key 路径；不填则用 ADC |
| `GE_TIMEOUT_MS` | 600000 | 上游请求超时 |
| `API_KEY`（Cloud Run） | 必填 | 客户端访问代理的密钥，由 Secret Manager 注入 |

## 8. 注意事项

- **延迟**：streamAssist 会做规划和思考，长 prompt 首 token 实测 14~100 秒，思考无法关闭。
- **没有 usage 和缓存**：OpenAI 协议的 `usage` 全为 0。
- **JSON 模式不是真流式**：需要整段返回才能去掉代码块。
- **会话记录**：每次请求在 GE 应用中新建一个会话，提示词和回答保存在运行身份的 GE 会话历史中。
- **模型可用性**：
  - 免费试用许可证下 gemini-4-argon 返回 400。
  - 应用模型设置里没有启用的模型（例如 gemini-4-hydrogen、gemini-4-carbon）返回 400 `disabled by default`。
- **提示词落盘**：AIClient2API 会把每次收到的系统提示词覆盖写入 `configs/fetch_system_prompt.txt`，没有开关。Cloud Run 上写在内存文件系统里，会占用实例内存。
- **公网暴露**：`--allow-unauthenticated` 时服务地址公网可达，只靠 API Key 保护。正式环境建议改用内网入口、IAP 或 API Gateway，并定期轮换 Key。
- **许可合规**：用一个服务账号代表大量终端用户调用 GE 助手，是否符合 GE 许可条款需与 Google 确认。
- **不要启用** AIClient2API 的多账号轮询、绕过限额等功能。
- **维护**：AIClient2API 是 GPL-3.0 项目，补丁需要随上游版本自行维护。
