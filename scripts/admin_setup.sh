#!/bin/bash
# 登录 new-api 管理端，创建 Gemini Enterprise 渠道和调用令牌（不在输出中打印密钥）
# 用法: NEWAPI=http://127.0.0.1:3001 ADMIN_USER=admin ADMIN_PASS=... GE_PROJECT_ID=... GE_ENGINE_ID=... ./admin_setup.sh
set -euo pipefail
B=${NEWAPI:-http://127.0.0.1:3001}
AT=$(curl -s -X POST $B/api/user/login -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,os;print(json.dumps({"username":os.environ["ADMIN_USER"],"password":os.environ["ADMIN_PASS"]}))')" \
  | python3 -c "import json,sys;print(json.load(sys.stdin)['data']['access_token'])")
H=(-H "Authorization: Bearer $AT" -H "New-Api-User: 1" -H "Content-Type: application/json")

OTHER=$(python3 -c 'import json,os;print(json.dumps({"project_id":os.environ["GE_PROJECT_ID"],"engine_id":os.environ["GE_ENGINE_ID"],"location":"global"}))')
BODY=$(OTHER="$OTHER" python3 -c 'import json,os;print(json.dumps({"mode":"single","channel":{"type":64,"name":"gemini-enterprise","key":os.environ.get("GE_KEY","adc"),"base_url":"https://discoveryengine.googleapis.com","other":os.environ["OTHER"],"models":os.environ.get("GE_MODELS","gemini-3.8-flash,gemini-3.8-flash-low,gemini-3.8-flash-medium,gemini-3.8-flash-high,gemini-3.7-flash,gemini-2.5-pro"),"group":"default","status":1}}))')
echo "add channel: $(curl -s -X POST $B/api/channel/ "${H[@]}" -d "$BODY" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["success"],d.get("message",""))')"

echo "add token: $(curl -s -X POST $B/api/token/ "${H[@]}" -d '{"name":"ge-test","remain_quota":0,"unlimited_quota":true,"expired_time":-1}' | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["success"],d.get("message",""))')"
TID=$(curl -s "$B/api/token/?p=1&page_size=50" "${H[@]}" | python3 -c 'import json,sys;d=json.load(sys.stdin)["data"];items=d["items"] if isinstance(d,dict) else d;print([t["id"] for t in items if t["name"]=="ge-test"][-1])')
KEY=$(curl -s -X POST "$B/api/token/$TID/key" "${H[@]}" | python3 -c 'import json,sys;d=json.load(sys.stdin)["data"];print(d["key"] if isinstance(d,dict) else d)')
umask 077; echo "sk-${KEY#sk-}" > "${TOKEN_FILE:-.token}"
echo "token saved to ${TOKEN_FILE:-.token}"
