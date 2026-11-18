# ge-integrate-with-gw

给开源 API 网关 [AIClient2API](https://github.com/justlovemaki/AIClient2API) 增加 Gemini Enterprise（Discovery Engine API `streamAssist`）提供方 `gemini-ge-assist`，对外提供 OpenAI / Claude / Gemini 兼容接口，并部署到 Cloud Run。

## 目录

| 路径 | 说明 |
|---|---|
| `provider/ge-assist-provider.patch` | 对 AIClient2API（上游 commit `31b8d90`，v3.6.8）的完整补丁 |
| `provider/src/providers/gemini/ge-assist-core.js` | 提供方核心实现（已包含在补丁中，单独放出便于阅读） |
| `cloudrun/Dockerfile`、`cloudrun/entrypoint.sh` | Cloud Run 镜像：用环境变量生成配置，API Key 由 Secret Manager 注入 |
| `config.example.json` | 本机运行的配置示例 |
| `tests/smoke_test.py` | 功能测试和延迟测试脚本 |
| `docs/DEPLOYMENT_GUIDE.md` | 部署指南：操作步骤、核心代码说明、测试方法 |

## 快速开始

```bash
git clone https://github.com/justlovemaki/AIClient2API.git && cd AIClient2API
git checkout 31b8d90
git apply ../ge-integrate-with-gw/provider/ge-assist-provider.patch
docker build -t aiclient2api-ge:3.6.8 .

cp -r ../ge-integrate-with-gw/cloudrun .
IMAGE=<REGION>-docker.pkg.dev/<PROJECT_ID>/<AR_REPO>/aiclient2api-ge:3.6.8-assist
docker build -f cloudrun/Dockerfile -t $IMAGE . && docker push $IMAGE

gcloud run deploy aiclient2api-ge --project <PROJECT_ID> --region <REGION> --image $IMAGE \
  --service-account <SERVICE_ACCOUNT> --port 8080 --timeout 900 \
  --set-env-vars GE_PROJECT_ID=<PROJECT_ID>,GE_ENGINE_ID=<ENGINE_ID>,GE_WEB_GROUNDING=false \
  --set-secrets API_KEY=<SECRET_NAME>:latest --allow-unauthenticated

BASE_URL=<SERVICE_URL> API_KEY=<API_KEY> python3 ../ge-integrate-with-gw/tests/smoke_test.py
```

详细步骤见 [docs/DEPLOYMENT_GUIDE.md](docs/DEPLOYMENT_GUIDE.md)。

## 限制

streamAssist 是 GE 助手接口，不是模型推理接口：

- 没有 systemInstruction、JSON Schema、thinking 档位，系统指令和历史会被拼成一段文字。
- 不返回 token 用量，没有隐式缓存。
- 长 prompt 首 token 实测 14~100 秒。
- 每次请求会在 GE 应用中新建会话。

## 许可

补丁修改的是 GPL-3.0 项目 AIClient2API，补丁及 `provider/` 下的代码按 GPL-3.0 发布。
