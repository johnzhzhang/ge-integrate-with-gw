#!/bin/sh
# Cloud Run 启动：用环境变量生成 configs/config.json，API Key 来自 Secret Manager 注入的 API_KEY
# 上游：Discovery Engine API assistants/default_assistant:generateContent（gemini-ge-assist）
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
