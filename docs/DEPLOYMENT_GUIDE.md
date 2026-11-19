# AIClient2API 接入 Gemini Enterprise（Discovery Engine API）部署指南

本文说明怎样给开源代理 AIClient2API 增加一个 Gemini Enterprise 提供方 `gemini-ge-assist`，并部署到 Cloud Run，对外提供 OpenAI、Claude、Gemini 兼容接口。内容包括实际执行过的每一步操作、核心代码说明和测试方法。

文中项目、账号、地址、密钥等均用占位符表示：

| 占位符 | 含义 |
|---|---|
| `<PROJECT_ID>` | GCP 项目 ID |
| `<ENGINE_ID>` | Gemini Enterprise 应用（engine）ID |
| `<REGION>` | Cloud Run 和 Artifact Registry 所在区域，例如 `us-central1` |
| `<AR_REPO>` | Artifact Registry Docker 仓库名 |
| `<SERVICE_ACCOUNT>` | Cloud Run 运行身份（服务账号邮箱） |
| `<API_KEY>` | 客户端访问代理用的密钥 |
| `<SERVICE_URL>` | Cloud Run 服务地址 |

## 1. 方案概述

### 1.1 上游接口

```
POST https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:generateContent?model=<MODEL_ID>
POST …/assistants/default_assistant:streamGenerateContent?model=<MODEL_ID>
```

- 请求体和响应都是 **Gemini 原生 GenerateContent 格式**：支持 systemInstruction、多轮 contents、responseJsonSchema、thinkingConfig、tools，返回 usageMetadata（含缓存和思考 token）。
- 模型由 **URL 查询参数 `model`** 指定，不带档位后缀；可用范围取决于 GE 应用的模型设置和许可证。
- 只在 **v1alpha** 可用，这个方法**没有写进公开 discovery 文档**。正式使用前请向 Google 确认支持情况和计费方式（响应中 `trafficType` 为 `PROVISIONED_THROUGHPUT`）。
- 不经过 GE 助手的规划流程，也不新建 GE 会话。业务 prompt 首 token 约 1.5~2.5 秒；GE 助手接口 `streamAssist` 要 14~100 秒，而且不支持 JSON Schema、思考档位和 usage。

### 1.2 为什么要改代码

AIClient2API 自带的 Gemini 提供方都不能调 Gemini Enterprise：

- `gemini-antigravity`：调 `cloudcode-pa.googleapis.com`，用个人账号 OAuth，是消费者渠道。
- `gemini-cli-oauth`：当前版本已在代码中注释掉。
- `openai-custom`：只能接 OpenAI 兼容接口，Discovery Engine 没有这种接口。

### 1.3 架构

```
客户端（OpenAI / Claude / Gemini SDK）
   │  Authorization: Bearer <API_KEY>
   ▼
AIClient2API（Cloud Run）
   │  ① 内置转换器：OpenAI / Claude 请求 → Gemini 格式
   │  ② gemini-ge-assist：字段整理、模型后缀 → thinkingLevel、responseSchema → responseJsonSchema
   │  ③ 调上游；流式响应（连续 JSON 对象）逐个解析
   │  ④ 内置转换器：Gemini 响应 → OpenAI / Claude 响应（含 usage）
   │  认证：Google ADC（Cloud Run 服务账号）
   ▼
Discovery Engine：assistants/default_assistant:(stream)GenerateContent?model=…
```

AIClient2API 内部统一用 Gemini 格式调用提供方，上游也是 Gemini 格式，所以提供方基本是透传，只处理与 Gemini API 不同的地方：

| 与 Gemini API 的差异 | 提供方的处理 |
|---|---|
| URL：`assistants/default_assistant:generateContent?model=` | 拼接 URL，`model` 放查询参数 |
| 认证：OAuth access token + `X-Goog-User-Project` | `google-auth-library` ADC |
| 模型 ID 不带档位后缀 | `gemini-3.8-flash-low` → `model=gemini-3.8-flash` + `thinkingLevel: LOW` |
| `responseSchema` 要求大写 `type` | 改用 `responseJsonSchema`（标准 JSON Schema） |
| 流式响应是首尾相接的 JSON 对象，不是 SSE | 增量扫描括号，每凑齐一个对象就产出 |
| AIClient2API 转换器输出 snake_case（`system_instruction`）并带内部字段 | 转成 camelCase，只透传上游认识的字段 |

## 2. 前提条件

| 项目 | 要求 |
|---|---|
| Gemini Enterprise | 项目内已有 GE 应用 `<ENGINE_ID>`，要用的模型在应用的模型设置中已启用 |
| 服务账号 | `<SERVICE_ACCOUNT>` 能调用该应用（例如 `roles/discoveryengine.user`），并有 `roles/serviceusage.serviceUsageConsumer`；按 GE 要求分配许可证 |
| 已启用 API | `discoveryengine.googleapis.com`、`run.googleapis.com`、`artifactregistry.googleapis.com`、`secretmanager.googleapis.com` |
| 构建机 | Docker、gcloud CLI、git |

先确认服务账号能直接调上游（在以该服务账号运行的 GCE 机器上执行）：

```bash
TOKEN=$(curl -s -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" \
  | python3 -c "import json,sys;print(json.load(sys.stdin)['access_token'])")
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "X-Goog-User-Project: <PROJECT_ID>" \
  -H "Content-Type: application/json" \
  "https://discoveryengine.googleapis.com/v1alpha/projects/<PROJECT_ID>/locations/global/collections/default_collection/engines/<ENGINE_ID>/assistants/default_assistant:generateContent?model=gemini-3.8-flash" \
  -d '{"contents":[{"role":"user","parts":[{"text":"1+1=? 只回答数字"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"}}}'
```

返回 `candidates` 和 `usageMetadata` 即可。查看应用启用了哪些模型：

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

在 GCE 上，容器通过元数据服务器自动拿到机器服务账号的凭证。其他环境可以挂载服务账号 key，并在配置中加 `"GE_CREDENTIALS_FILE": "/app/configs/sa.json"`。

### 3.4 构建 Cloud Run 镜像

Cloud Run 不方便挂载可写的配置目录，所以加一层启动脚本，用环境变量生成配置。

`cloudrun/entrypoint.sh`：

```sh
#!/bin/sh
set -e
mkdir -p /app/configs
cat > /app/configs/config.json <<JSON
{
  "SERVER_PORT": ${PORT:-8080},
  "HOST": "0.0.0.0",
  "MODEL_PROVIDER": "gemini-ge-assist",
  "GE_PROJECT_ID": "${GE_PROJECT_ID}",
  "GE_ENGINE_ID": "${GE_ENGINE_ID}",
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

构建并推送：

```bash
IMAGE=<REGION>-docker.pkg.dev/<PROJECT_ID>/<AR_REPO>/aiclient2api-ge:3.6.8-gc
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
  --set-env-vars GE_PROJECT_ID=<PROJECT_ID>,GE_ENGINE_ID=<ENGINE_ID> \
  --set-secrets API_KEY=aiclient2api-ge-api-key:latest \
  --allow-unauthenticated
```

| 参数 | 取值原因 |
|---|---|
| `--timeout 900` | 长 prompt + 高思考档位时可能超过 1 分钟 |
| `--concurrency 40` | 请求大部分时间在等上游 |
| `--min-instances 0` | 空闲不计费；需要避免冷启动时改为 1 |
| `--set-secrets` | 密钥以环境变量注入，不进入镜像和部署配置 |
| `--allow-unauthenticated` | 让 OpenAI 等客户端直接用 API Key 访问；鉴权由 AIClient2API 完成 |

### 3.7 验证、更新与清理

```bash
URL=<SERVICE_URL>
KEY=$(gcloud secrets versions access latest --secret aiclient2api-ge-api-key --project <PROJECT_ID>)
curl -s -o /dev/null -w "%{http_code}\n" $URL/v1/models                       # 401
curl -s $URL/v1/chat/completions -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash-low","messages":[{"role":"user","content":"17*23=? 只回答数字"}]}'

# 日志：每次请求打印 model -> 上游方法、模型 ID 和 thinkingLevel
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="aiclient2api-ge" AND textPayload:"GE Assist"' \
  --project <PROJECT_ID> --limit 20 --format="value(textPayload)"

# 代码更新：重新执行 3.2、3.4，再执行
gcloud run deploy aiclient2api-ge --project <PROJECT_ID> --region <REGION> --image $IMAGE

# 清理
gcloud run services delete aiclient2api-ge --project <PROJECT_ID> --region <REGION>
gcloud secrets delete aiclient2api-ge-api-key --project <PROJECT_ID>
```

## 4. 核心代码说明

### 4.1 提供方在 AIClient2API 中的位置

AIClient2API 处理一次请求的流程：

1. 根据路径判断客户端协议（`/v1/chat/completions` 为 OpenAI，`/v1/messages` 为 Claude，`/v1beta/models/...` 为 Gemini）。
2. 根据 `MODEL_PROVIDER` 找到提供方。名字以 `gemini-` 开头，按 Gemini 协议处理。
3. 协议不同时，用转换器把请求转成 Gemini 格式，调用适配器的 `generateContent` / `generateContentStream`，再把结果转回客户端协议（包括 `usageMetadata` → OpenAI `usage` 的映射）。

### 4.2 注册

```js
// src/utils/constants.js
GE_ASSIST: 'gemini-ge-assist',

// src/providers/adapter.js —— 适配器只做初始化检查和转发
export class GeAssistApiServiceAdapter extends ApiServiceAdapter {
    constructor(config) { super(); this.svc = new GeAssistApiService(config); }
    async _ensure() { if (!this.svc.isInitialized) await this.svc.initialize(); }
    async generateContent(model, body) { await this._ensure(); return this.svc.generateContent(model, body); }
    async *generateContentStream(model, body) { await this._ensure(); yield* this.svc.generateContentStream(model, body); }
    async listModels() { return this.svc.listModels(); }
    async refreshToken() { return false; }        // token 由 google-auth-library 自动刷新
    async forceRefreshToken() { return false; }
    isExpiryDateNear() { return false; }
}
registerAdapter(MODEL_PROVIDER.GE_ASSIST, GeAssistApiServiceAdapter);

// src/providers/provider-models.js —— /v1/models 展示的模型（含档位后缀）
'gemini-ge-assist': ['gemini-3.8-flash', 'gemini-3.8-flash-low', 'gemini-3.8-flash-medium', 'gemini-3.8-flash-high', ...],
```

模型列表只影响 `/v1/models` 的展示，实际能否调用取决于 GE 应用的模型设置和许可证。

### 4.3 ge-assist-core.js 要点

- **认证**：`new GoogleAuth({scopes: [cloud-platform]})`。Cloud Run / GCE 上自动用运行身份；也支持 `GE_CREDENTIALS_FILE`。token 由库缓存并自动刷新。
- **`splitModel`**：`gemini-3.8-flash-low` → `{id: 'gemini-3.8-flash', level: 'LOW'}`。上游 `model=` 不认后缀；上游支持的 `thinkingLevel` 是 LOW、MEDIUM、HIGH。
- **`buildBody`**：
  1. 只保留 `contents`、`systemInstruction`、`generationConfig`、`tools`、`toolConfig`、`safetySettings`，并把 `system_instruction` 等 snake_case 字段转成 camelCase。上游不校验未知字段，字段名写错会被静默忽略，系统提示词就丢了，所以必须转换。
  2. 有 `responseSchema` 且没有 `responseJsonSchema` 时，移到 `responseJsonSchema`。上游的 `responseSchema` 要求大写 `type`，否则报 "didn't specify the schema type field"。
  3. 有档位后缀时写入 `thinkingConfig.thinkingLevel`，并去掉 `thinkingBudget`，两者互斥。
- **`_post`**：`axios.post(\`${base}:${method}\`, body, {params: {model: id}, headers: {Authorization, 'X-Goog-User-Project'}})`。出错时从错误体（流式接口是数组 `[{"error":…}]`）取出 `error.message`，原样返回给客户端，例如 `The model gemini-4-argon is not available during the free trial`。
- **`iterJsonObjects`**：上游流式响应是 `{...}{...}{...}`。逐字符记录括号深度和是否在字符串内，深度回到 0 时 `JSON.parse` 一个对象并产出。用 `StringDecoder` 解码，网络分块切断汉字时不会乱码。
- **`generateContent`**：非流式直接返回上游 JSON。
- **`generateContentStream`**：逐个产出上游的 GenerateContentResponse，usage 由 AIClient2API 转换器从最后一块的 `usageMetadata` 映射。

### 4.4 完整源码

```js
/**
 * Gemini Enterprise via Discovery Engine API：assistants/default_assistant:(stream)GenerateContent?model=…
 *
 * 这是 Discovery Engine v1alpha 里未写入公开 discovery 文档的方法。请求体和响应都是 Gemini 原生
 * GenerateContent 格式，所以本提供方基本是透传：
 *   - 模型用 URL 查询参数 model 指定，不带档位后缀；客户端传 -low/-medium/-high 时转成 thinkingLevel
 *   - 只透传上游认识的字段（上游不校验未知字段，但 AIClient2API 内部字段没必要发出去）
 *   - responseSchema（要求大写 type）改用 responseJsonSchema（标准 JSON Schema）
 *   - 流式响应是首尾相接的 JSON 对象（不是 SSE），增量解析后逐个产出
 * 认证：Google ADC（GCE/Cloud Run 服务账号，或 GE_CREDENTIALS_FILE）。
 */
import axios from 'axios';
import { StringDecoder } from 'string_decoder';
import { GoogleAuth } from 'google-auth-library';
import logger from '../../utils/logger.js';
import { getProviderModels } from '../provider-models.js';
import { MODEL_PROVIDER } from '../../utils/common.js';

const BASE = 'https://discoveryengine.googleapis.com/v1alpha';
const ALLOWED_FIELDS = ['contents', 'systemInstruction', 'generationConfig', 'tools', 'toolConfig', 'safetySettings'];
const SNAKE_ALIASES = { system_instruction: 'systemInstruction', generation_config: 'generationConfig',
    tool_config: 'toolConfig', safety_settings: 'safetySettings' };

/** 增量解析首尾相接的 JSON 对象流 `{...}{...}`（也兼容数组包裹 `[{...},{...}]`），每凑齐一个对象就产出 */
async function* iterJsonObjects(stream) {
    let depth = 0, inStr = false, esc = false, buf = '';
    const dec = new StringDecoder('utf8');     // 网络分块可能切断多字节汉字
    for await (const chunk of stream) {
        for (const ch of dec.write(chunk)) {
            if (depth === 0 && ch !== '{') continue;  // 跳过对象之间的空白、逗号、方括号
            buf += ch;
            if (inStr) { if (esc) esc = false; else if (ch === '\\') esc = true; else if (ch === '"') inStr = false; continue; }
            if (ch === '"') inStr = true;
            else if (ch === '{') depth++;
            else if (ch === '}' && --depth === 0) { yield JSON.parse(buf); buf = ''; }
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
        this.timeout = config.GE_TIMEOUT_MS || 600000;
        const authOpts = { scopes: ['https://www.googleapis.com/auth/cloud-platform'] };
        if (config.GE_CREDENTIALS_FILE) authOpts.keyFile = config.GE_CREDENTIALS_FILE;
        this.auth = new GoogleAuth(authOpts);
        this.base = `${BASE}/projects/${this.projectId}/locations/${this.location}/collections/default_collection/engines/${this.engineId}/assistants/default_assistant`;
        this.isInitialized = false;
    }

    async initialize() {
        await this._token();
        this.isInitialized = true;
        logger.info(`[GE Assist] project=${this.projectId} engine=${this.engineId} (generateContent)`);
    }

    async _token() {
        const { token } = await (await this.auth.getClient()).getAccessToken();
        if (!token) throw new Error('[GE Assist] failed to obtain access token');
        return token;
    }

    /** "gemini-3.8-flash-low" → { id: "gemini-3.8-flash", level: "LOW" } */
    splitModel(model) {
        const m = String(model || '').replace(/^models\//, '');
        const r = m.match(/-(low|medium|high)$/);
        return r ? { id: m.slice(0, -r[0].length), level: r[1].toUpperCase() } : { id: m, level: null };
    }

    buildBody(req, level) {
        const body = {};
        for (const k of ALLOWED_FIELDS) if (req?.[k] !== undefined) body[k] = req[k];
        // AIClient2API 的 OpenAI→Gemini 转换器输出 snake_case，统一成 camelCase
        for (const [snake, camel] of Object.entries(SNAKE_ALIASES)) {
            if (body[camel] === undefined && req?.[snake] !== undefined) body[camel] = req[snake];
        }
        const gc = body.generationConfig = { ...(body.generationConfig || {}) };
        if (gc.responseSchema && !gc.responseJsonSchema) {   // 上游 responseSchema 要求大写 type，改用 responseJsonSchema
            gc.responseJsonSchema = gc.responseSchema;
            delete gc.responseSchema;
        }
        if (level) gc.thinkingConfig = { ...(gc.thinkingConfig || {}), thinkingLevel: level, thinkingBudget: undefined };
        if (!Object.keys(gc).length) delete body.generationConfig;
        return body;
    }

    async _post(method, model, req, stream) {
        const { id, level } = this.splitModel(model);
        const body = this.buildBody(req, level);
        logger.info(`[GE Assist] model=${model} -> ${method}?model=${id} thinkingLevel=${body.generationConfig?.thinkingConfig?.thinkingLevel || '-'}`);
        try {
            return await axios.post(`${this.base}:${method}`, body, {
                params: { model: id },
                headers: { Authorization: `Bearer ${await this._token()}`, 'X-Goog-User-Project': this.projectId, 'Content-Type': 'application/json' },
                timeout: this.timeout, responseType: stream ? 'stream' : 'json',
            });
        } catch (error) {
            let data = error.response?.data;
            if (data && typeof data.on === 'function') { let s = ''; for await (const c of data) s += c.toString('utf8'); data = s; }
            if (error.response) {
                let msg = typeof data === 'string' ? data : JSON.stringify(data);
                try { const j = typeof data === 'string' ? JSON.parse(data) : data; msg = (Array.isArray(j) ? j[0] : j)?.error?.message || msg; } catch { /* 保留原文 */ }
                error.message = `[GE Assist] HTTP ${error.response.status}: ${String(msg).slice(0, 800)}`;
            }
            logger.error(error.message);
            throw error;
        }
    }

    async generateContent(model, req) {
        return (await this._post('generateContent', model, req, false)).data;
    }

    async *generateContentStream(model, req) {
        const r = await this._post('streamGenerateContent', model, req, true);
        for await (const obj of iterJsonObjects(r.data)) {
            if (obj.error) throw new Error(`[GE Assist] ${JSON.stringify(obj.error)}`);
            yield obj;
        }
    }

    async listModels() {
        return { models: getProviderModels(MODEL_PROVIDER.GE_ASSIST).map(id => ({
            name: `models/${id}`, version: '1.0.0', displayName: id, description: `Gemini Enterprise ${id}`,
            inputTokenLimit: 1048576, outputTokenLimit: 65536, supportedGenerationMethods: ['generateContent', 'streamGenerateContent'],
        })) };
    }

    isExpiryDateNear() { return false; }
}
```

## 5. 测试方法

测试分三层：先确认 GE 侧可用（不经过代理），再测代理功能，最后测延迟和缓存。脚本 `smoke_test.py` 只依赖 `requests`，通过环境变量指定服务地址和 Key。

### 5.1 准备

```bash
pip install requests
export BASE_URL=<SERVICE_URL>          # 本机测试用 http://127.0.0.1:3000
export API_KEY=$(gcloud secrets versions access latest --secret aiclient2api-ge-api-key --project <PROJECT_ID>)
export MODEL=gemini-3.8-flash-low
```

### 5.2 第一层：直连上游

用第 2 节的 curl 命令以服务账号身份直接调用。失败时：

| 返回 | 原因 | 处理 |
|---|---|---|
| 403 `PERMISSION_DENIED` | 服务账号缺 Discovery Engine 权限 | 授予 `roles/discoveryengine.user` |
| 403 `The model xxx is not enabled by the admin` | GE 应用未启用该模型，或模型 ID 带了后缀 | 管理员启用；ID 不带后缀 |
| 400 `not available during the free trial` | 许可证为免费试用，模型受限 | 换付费许可证或换模型 |
| 400 `Thinking level is unsupported` | 传了 MINIMAL 等不支持的档位 | 用 LOW / MEDIUM / HIGH |
| 404 | 不是 v1alpha | 改用 v1alpha |

### 5.3 第二层：功能测试

```bash
python3 smoke_test.py
```

| 用例 | 通过条件 |
|---|---|
| 鉴权 | 不带 Key 返回 401 |
| 模型列表 | 包含 `MODEL` |
| OpenAI 非流式 / 流式 | 回答正确，流式中文无乱码 |
| 系统指令 + 多轮历史 | 回答含前文让它记住的数字（验证 snake_case 转换） |
| JSON Schema | 能被 `json.loads` 解析且包含 required 字段（验证 responseJsonSchema 转换） |
| Claude 协议 | `POST /v1/messages` 返回 text |
| Gemini 协议 | `POST /v1beta/models/{MODEL}:generateContent` 正确 |
| argon（信息） | 只打印结果，取决于许可证 |

实测（Cloud Run）：

```
[PASS] 不带 Key 返回 401  401
[PASS] 模型列表  [... 'gemini-3.8-flash', 'gemini-3.8-flash-high', 'gemini-3.8-flash-low', 'gemini-3.8-flash-medium', ...]
[PASS] OpenAI 非流式  '391'
[PASS] OpenAI 流式  …字 首token 1.2s
[PASS] 流式中文无乱码
[PASS] 系统指令 + 多轮历史  '4271'
[PASS] JSON Schema  ['answer', 'tags']
[PASS] Claude /v1/messages  ['蓝']
[PASS] Gemini generateContent  '2'
[INFO] gemini-4-argon（取决于许可证）: HTTP 400 ... not available during the free trial ...

9/9 通过
```

### 5.4 第三层：延迟与缓存

```bash
python3 smoke_test.py latency 10                                # 默认短 prompt
PROMPT_FILE=./my_prompt.txt python3 smoke_test.py latency 10    # 自己的长 prompt
```

长 prompt 实测：约 2.5 万 token 的固定 system prompt + JSON Schema，经 Cloud Run 上的代理连发 5 次（`gemini-3.8-flash-low`，OpenAI 流式 + `include_usage`）：

| 次数 | 首 token | prompt | cached | reasoning | JSON |
|---|---|---|---|---|---|
| 1 | 1.52s | 24,888 | 0 | 0 | 正确 |
| 2 | 1.66s | 24,888 | 20,358 | 0 | 正确 |
| 3 | 1.35s | 24,888 | 20,358 | 0 | 正确 |
| 4 | 1.44s | 24,888 | 20,358 | 0 | 正确 |
| 5 | 1.38s | 24,888 | 20,358 | 0 | 正确 |

- `-low` 后缀生效，没有思考 token。
- 第 2 次起隐式缓存命中 82%。缓存是否命中由上游决定，不保证每次命中。

### 5.5 排查

| 现象 | 查看 |
|---|---|
| 客户端报 4xx / 5xx | Cloud Run 日志中 `[GE Assist] HTTP xxx: ...`，即上游原始错误 |
| 回答不遵守系统指令 | 日志里是否有 `model=… -> generateContent?model=…`；检查客户端是否用 `system` 角色 |
| 思考 token 不为 0 | 模型名是否带 `-low`；日志 `thinkingLevel=` 字段 |
| 冷启动慢 | `--min-instances 1` |

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
s = client.chat.completions.create(
    model="gemini-3.8-flash-low", stream=True, stream_options={"include_usage": True},
    messages=[{"role": "system", "content": "……"}, {"role": "user", "content": "你好"}])
for ch in s:
    if ch.choices: print(ch.choices[0].delta.content or "", end="")
    if ch.usage: print("\n", ch.usage)
```

```bash
# Claude 协议
curl <SERVICE_URL>/v1/messages -H "x-api-key: <API_KEY>" -H "anthropic-version: 2023-06-01"   -H "Content-Type: application/json"   -d '{"model":"gemini-3.8-flash-low","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'
# Gemini 协议
curl "<SERVICE_URL>/v1beta/models/gemini-3.8-flash-low:streamGenerateContent?alt=sse"   -H "x-goog-api-key: <API_KEY>" -H "Content-Type: application/json"   -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}'
```

## 7. 配置项

| 配置 | 默认 | 说明 |
|---|---|---|
| `GE_PROJECT_ID` | 必填 | GE 所在项目 |
| `GE_ENGINE_ID` | 必填 | GE 应用 ID |
| `GE_LOCATION` | `global` | GE 应用所在位置 |
| `GE_CREDENTIALS_FILE` | 无 | 服务账号 key 路径；不填则用 ADC |
| `GE_TIMEOUT_MS` | 600000 | 上游请求超时 |
| `API_KEY`（Cloud Run） | 必填 | 客户端访问代理的密钥，由 Secret Manager 注入 |

## 8. 注意事项

- **未公开接口**：`assistants/default_assistant:generateContent` 不在 discovery 文档里，只在 v1alpha 可用，行为可能变化。上线前向 Google 确认支持情况、SLA 和计费方式。
- **思考档位**：模型名不带后缀且客户端不指定时，模型默认会思考。业务调用建议用 `-low`。
- **提示词落盘**：AIClient2API 会把每次收到的系统提示词覆盖写入 `configs/fetch_system_prompt.txt`，没有开关。Cloud Run 上写在内存文件系统里，会占用实例内存。
- **公网暴露**：`--allow-unauthenticated` 时服务地址公网可达，只靠 API Key 保护。正式环境建议改用内网入口、IAP 或 API Gateway，并定期轮换 Key。
- **许可合规**：用一个服务账号代表大量终端用户调用 GE，是否符合 GE 许可条款需与 Google 确认。
- **不要启用** AIClient2API 的多账号轮询、绕过限额等功能。
- **维护**：AIClient2API 是 GPL-3.0 项目，补丁需要随上游版本自行维护。
