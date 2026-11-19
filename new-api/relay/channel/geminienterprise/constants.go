package geminienterprise

// ChannelName 渠道名
const ChannelName = "gemini-enterprise"

// DefaultBaseURL Discovery Engine API 地址
const DefaultBaseURL = "https://discoveryengine.googleapis.com"

// ModelList 可用模型。上游 ?model= 只认不带后缀的 ID；-low/-medium/-high 后缀由适配器转成 thinkingLevel。
// 实际能否调用取决于 GE 应用的模型设置和许可证。
var ModelList = []string{
	"gemini-3.8-flash", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high",
	"gemini-3.7-flash", "gemini-3.7-flash-low",
	"gemini-3.6-flash",
	"gemini-3.1-pro-preview",
	"gemini-2.5-pro",
	"gemini-4-argon", "gemini-4-argon-low", "gemini-4-argon-high",
}
