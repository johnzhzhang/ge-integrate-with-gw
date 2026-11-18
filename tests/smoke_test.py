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
