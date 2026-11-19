# ge-integrate-with-gw

给开源 API 网关 [AIClient2API](https://github.com/justlovemaki/AIClient2API) 增加 Gemini Enterprise 提供方 `gemini-ge-assist`。上游调用 Discovery Engine API 的 `assistants/default_assistant:generateContent`（Gemini 原生格式），对外提供 OpenAI / Claude / Gemini 兼容接口，并部署到 Cloud Run。

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
IMAGE=<REGION>-docker.pkg.dev/<PROJECT_ID>/<AR_REPO>/aiclient2api-ge:3.6.8-gc
docker build -f cloudrun/Dockerfile -t $IMAGE . && docker push $IMAGE

gcloud run deploy aiclient2api-ge --project <PROJECT_ID> --region <REGION> --image $IMAGE \
  --service-account <SERVICE_ACCOUNT> --port 8080 --timeout 900 \
  --set-env-vars GE_PROJECT_ID=<PROJECT_ID>,GE_ENGINE_ID=<ENGINE_ID> \
  --set-secrets API_KEY=<SECRET_NAME>:latest --allow-unauthenticated

BASE_URL=<SERVICE_URL> API_KEY=<API_KEY> python3 ../ge-integrate-with-gw/tests/smoke_test.py
```

详细步骤见 [docs/DEPLOYMENT_GUIDE.md](docs/DEPLOYMENT_GUIDE.md)。

## 说明

- 模型名可带 `-low/-medium/-high` 后缀，会转成 `thinkingLevel`，例如 `gemini-3.8-flash-low`。
- 支持 systemInstruction、多轮对话、JSON Schema、流式、隐式缓存，usage 为上游真实值。
- 上游方法只在 v1alpha 可用，且没有写进 Discovery Engine 的公开接口文档，正式使用前请向 Google 确认支持情况和计费方式。

## 许可

补丁修改的是 GPL-3.0 项目 AIClient2API，补丁及 `provider/` 下的代码按 GPL-3.0 发布。
