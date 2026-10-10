package bridge

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// 生成绑定的位置随服务所在包走：PiService 在 internal/bridge，绑定就在 internal/bridge/ 子目录下。
// 本测试的工作目录是包目录，所以回两级到仓库根。
const serviceBindingFile = "../../frontend/bindings/zeroa/internal/bridge/piservice.ts"

// 这个测试守着一条很容易忽略的规则：Wails 是“按方法 ID 调用”的。
// piservice.ts 里每个函数都写着一个数字 ID（如 Call.ByID(958351840)），
// 它由 wails3 根据 Go 模块名 / 服务名 / 方法名生成。
//
// 所以改了 Service 的方法名却忘记重新生成绑定时，编译和类型检查都不会报错，
// 只有在 WebView 里点按钮的那一刻才失败。这个测试把那种“运行时才发现”的错误
// 提前到 `go test ./...`。
//
// 它只比对方法名，不校验 ID 本身：ID 与 wails3 版本相关，
// 版本一致性由 scripts/build.ps1 里的版本校验负责。
func TestGeneratedBindingsMatchService(t *testing.T) {
	data, err := os.ReadFile(serviceBindingFile)
	if err != nil {
		t.Fatalf("读取生成的绑定失败，请先运行 scripts/build.ps1: %v", err)
	}
	generated := make(map[string]bool)
	// 生成的绑定里，每个可调用方法都是一行 `export function 名称(...)`。
	pattern := regexp.MustCompile(`(?m)^export function (\w+)\(`)
	for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
		generated[match[1]] = true
	}

	service := reflect.TypeOf(&PiService{})
	for i := range service.NumMethod() {
		name := service.Method(i).Name
		if strings.HasPrefix(name, "Service") {
			continue // Service* 是 Wails 的生命周期钩子（如 ServiceShutdown），不暴露给前端
		}
		if !generated[name] {
			t.Errorf("PiService.%s 不在 %s 中，请运行 scripts/build.ps1 重新生成绑定", name, serviceBindingFile)
		}
		delete(generated, name)
	}
	// 反向检查：绑定里导出、但 Service 上已不存在的方法同样要报错，
	// 否则删掉的方法会以“调用才失败”的形式留在前端代码里。
	for name := range generated {
		t.Errorf("绑定导出的 %s 在 PiService 上已不存在，请重新生成绑定", name)
	}
}
