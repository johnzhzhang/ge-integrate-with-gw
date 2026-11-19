package geminienterprise

// Config 渠道“其他”字段（JSON）：
//
//	{"project_id": "<PROJECT_ID>", "engine_id": "<ENGINE_ID>", "location": "global"}
//
// project_id 不填时取服务账号 JSON 里的 project_id。
type Config struct {
	ProjectID string `json:"project_id"`
	EngineID  string `json:"engine_id"`
	Location  string `json:"location"`
}
