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
