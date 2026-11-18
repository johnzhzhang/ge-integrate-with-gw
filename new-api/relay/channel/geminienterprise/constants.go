package geminienterprise

// ChannelName 渠道名
const ChannelName = "gemini-enterprise"

// DefaultBaseURL Discovery Engine API 地址
const DefaultBaseURL = "https://discoveryengine.googleapis.com"

// ModelList streamAssist generationSpec.modelId 可用的模型 ID（不带 -low/-medium/-high 档位后缀）。
// 实际能否调用取决于 GE 应用的模型设置和许可证。
var ModelList = []string{
	"gemini-3.8-flash",
	"gemini-3.7-flash",
	"gemini-3.6-flash",
	"gemini-3.1-pro-preview",
	"gemini-2.5-pro",
	"gemini-4-argon",
}
