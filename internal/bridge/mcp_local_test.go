package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

// 本机三件套（IDA / Burp / jadx）的 MCP 配置必须能被解析成正确的传输方式。
//
// 为什么值得测：这三个服务端的传输方式各不相同（IDA 是 streamable HTTP、
// jadx 是 stdio、Burp 是 SSE 且要靠官方自带的 stdio 代理桥接），配置写错的表现
// 是"模型看不到工具"，而且不报错——最难查的一类问题。
func TestLocalMcpServersConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	config := `{
  "mcpServers": {
    "ida": {"url": "http://127.0.0.1:13391/mcp"},
    "jadx": {"command": "python.exe", "args": ["jadx_mcp_server.py", "--jadx-port", "8650"]},
    "burp": {"command": "java", "args": ["-jar", "mcp-proxy-all.jar", "--sse-url", "http://127.0.0.1:9876"]}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	servers, err := loadMcpServers()
	if err != nil {
		t.Fatalf("读取 mcp.json: %v", err)
	}
	byName := map[string]mcpServer{}
	for _, server := range servers {
		byName[server.Name] = server
	}
	for _, name := range []string{"ida", "jadx", "burp"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("缺少 %s 的配置，实际有 %v", name, byName)
		}
	}
	if got := byName["ida"].Transport; got != "http" {
		t.Errorf("ida 应当是 http（streamable HTTP），得到 %q", got)
	}
	if got := byName["ida"].URL; got != "http://127.0.0.1:13391/mcp" {
		t.Errorf("ida url 不对: %q", got)
	}
	if got := byName["jadx"].Transport; got != "stdio" {
		t.Errorf("jadx 应当是 stdio，得到 %q", got)
	}
	if len(byName["jadx"].Args) < 2 {
		t.Errorf("jadx 参数应当带着 server 脚本，得到 %v", byName["jadx"].Args)
	}
	if got := byName["burp"].Transport; got != "stdio" {
		t.Errorf("burp 应当走 stdio 代理（适配器不支持旧式 SSE），得到 %q", got)
	}
}
