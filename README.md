# ge-integrate-with-gw：new-api 接入 Gemini Enterprise

给开源 LLM 网关 [new-api](https://github.com/QuantumNous/new-api) 增加原生渠道类型 **Gemini Enterprise（type 64）**。渠道直接调用 Discovery Engine API（GE 助手接口 `streamAssist`），不经过其他代理。客户端可以用 OpenAI、Claude、Gemini 三种格式访问。

| 路径 | 说明 |
|---|---|
| `new-api/new-api-gemini-enterprise.patch` | 对 new-api（上游 commit `a1d1fb0`）的完整补丁，含后端和前端 |
| `new-api/relay/channel/geminienterprise/` | 渠道适配器源码（已包含在补丁中，单独放出便于阅读） |
| `scripts/admin_setup.sh` | 用管理 API 创建渠道和调用令牌 |
| `tests/smoke_test.py` | 功能测试和延迟测试 |
| `docs/NEW_API_GUIDE.md` | 部署步骤、核心代码说明、测试方法 |

## 快速开始

```bash
git clone https://github.com/QuantumNous/new-api.git && cd new-api
git checkout a1d1fb0
git apply ../ge-integrate-with-gw/new-api/new-api-gemini-enterprise.patch
docker build -t new-api-ge:dev .
docker run -d --name new-api-ge -p 127.0.0.1:3000:3000 -v $PWD/data:/data new-api-ge:dev
```

然后在控制台新建渠道：类型选 **Gemini Enterprise**，密钥填服务账号 JSON 或 `adc`，“其他”填：

```json
{"project_id": "<PROJECT_ID>", "engine_id": "<ENGINE_ID>", "location": "global", "web_grounding": false}
```

详见 [docs/NEW_API_GUIDE.md](docs/NEW_API_GUIDE.md)。

## 许可

new-api 使用 AGPL-3.0，本补丁及 `new-api/` 下的代码按 AGPL-3.0 发布。
