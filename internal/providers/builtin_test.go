package providers

import (
	"os"
	"path/filepath"
	"testing"
)

// 内置供应商注册表是从自带 Pi 的 bundle 里抠出来的（TUI 的 /login 用的就是这份）。
// 这里用一份"合成的 chunk"测提取逻辑本身：提取要能认出 id/名字/baseUrl/环境变量/
// 是否支持 OAuth，并且在 bundle 缺失或格式变了的时候安静地退回空列表。
func TestExtractBuiltinProvidersFromBundle(t *testing.T) {
	home := t.TempDir()
	chunks := filepath.Join(home, "pi-agent", "node_modules", "@earendil-works",
		"pi-coding-agent", "dist", "bundle", "chunks")
	if err := os.MkdirAll(chunks, 0o755); err != nil {
		t.Fatal(err)
	}
	// 形状照抄自带 bundle 里真实的 createProvider 调用
	chunk := `
function openaiProvider(){return createProvider({id:"openai",name:"OpenAI",baseUrl:"https://api.openai.com/v1",auth:{apiKey:envApiKeyAuth("OpenAI API key",["OPENAI_API_KEY"]),oauth:lazyOAuth({name:"OpenAI (ChatGPT subscription)"})},models:Object.values(OPENAI_MODELS),api:openAIResponsesApi()})}
function groqProvider(){return createProvider({id:"groq",name:"Groq",baseUrl:"https://api.groq.com/openai/v1",auth:{apiKey:envApiKeyAuth("Groq API key",["GROQ_API_KEY"])},models:Object.values(GROQ_MODELS),api:openAICompletionsApi()})}
function ollamaProvider(){return createProvider({id:"ollama",name:"Ollama",baseUrl:"http://127.0.0.1:11434/v1",models:Object.values(OLLAMA_MODELS),api:openAICompletionsApi()})}
`
	if err := os.WriteFile(filepath.Join(chunks, "chunk-TEST.js"), []byte(chunk), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_AGENT_HOME", home)
	// 清掉进程内缓存，让这次扫描真的发生
	ResetCache()

	list := Builtin()
	byID := map[string]Provider{}
	for _, entry := range list {
		byID[entry.ID] = entry
	}
	if len(byID) != 3 {
		t.Fatalf("应该抠出 3 个供应商，实际 %d: %v", len(byID), byID)
	}
	if entry := byID["openai"]; entry.Name != "OpenAI" || entry.EnvVar != "OPENAI_API_KEY" || !entry.OAuth || !entry.NeedsKey {
		t.Errorf("openai 提取不全: %+v", entry)
	}
	if entry := byID["groq"]; entry.BaseURL == "" || entry.OAuth {
		t.Errorf("groq 的 baseUrl / OAuth 判定不对: %+v", entry)
	}
	// 本地跑、不需要密钥的供应商不该被标成需要 key
	if entry := byID["ollama"]; entry.NeedsKey {
		t.Errorf("ollama 是本地供应商，不该要求密钥: %+v", entry)
	}

	// 降级：找不到 bundle 时返回空，而不是报错或崩溃
	ResetCache()
	t.Setenv("PI_AGENT_HOME", t.TempDir())
	if got := Builtin(); len(got) != 0 {
		t.Errorf("bundle 缺失时应该返回空列表，实际 %d 条", len(got))
	}
}
