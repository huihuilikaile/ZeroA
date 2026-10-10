package bridge

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 粘贴图片：浏览器给的是 data URL，Go 侧要落成临时文件并返回绝对路径
// （附件机制统一走"绝对路径交给模型读"，粘贴的图必须走同一条路）。
func TestSavePastedImage(t *testing.T) {
	service := &PiService{}
	// 1x1 PNG 的 base64
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

	result, err := service.SavePastedImage("data:image/png;base64," + png)
	if err != nil || !result.Success {
		t.Fatalf("SavePastedImage: %v %+v", err, result)
	}
	if !strings.Contains(result.Data, `"path"`) {
		t.Fatalf("应返回 path：%s", result.Data)
	}
	// 从 JSON 里取路径
	pathStart := strings.Index(result.Data, `"path":"`) + len(`"path":"`)
	pathEnd := strings.Index(result.Data[pathStart:], `"`) + pathStart
	path := result.Data[pathStart:pathEnd]
	t.Cleanup(func() { _ = os.Remove(path) })

	if !filepath.IsAbs(path) {
		t.Errorf("必须是绝对路径（模型按绝对路径读）：%s", path)
	}
	if !strings.HasSuffix(strings.ToLower(path), ".png") {
		t.Errorf("扩展名应来自 mime：%s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("文件应真的存在: %v", err)
	}
	want, _ := base64.StdEncoding.DecodeString(png)
	if string(data) != string(want) {
		t.Errorf("落盘内容应与粘贴的一致（%d vs %d 字节）", len(data), len(want))
	}

	// jpeg 的 mime → .jpg
	jpegResult, err := service.SavePastedImage("data:image/jpeg;base64," + png)
	if err != nil || !jpegResult.Success {
		t.Fatalf("SavePastedImage(jpeg): %v %+v", err, jpegResult)
	}
	if !strings.Contains(jpegResult.Data, ".jpg") {
		t.Errorf("image/jpeg 应落成 .jpg：%s", jpegResult.Data)
	}

	// 异常输入要被挡住
	for _, bad := range []string{"", "   ", "data:image/png;base64,!!!!", "data:no-comma"} {
		if _, err := service.SavePastedImage(bad); err == nil {
			t.Errorf("非法输入 %q 应该报错", bad)
		}
	}

	// 不常见的图片类型现在**放行**（改成"任意文件都能粘贴/拖进来"之后的行为）。
	// 旧实现只认 png/jpg/webp/gif，把 tiff 当非法输入拒绝——对一个"上传附件"的
	// 功能来说那是没必要的限制：附件机制本来就是"给模型一个绝对路径让它读"，
	// 不认识的扩展名落成 .bin 也比直接拒绝有用。
	if result, err := service.SavePastedImage("data:image/tiff;base64,AAAA"); err != nil {
		t.Errorf("不常见的图片类型应当放行，实际报错: %v", err)
	} else if !strings.Contains(result.Data, ".bin") {
		t.Errorf("认不出的类型应落成 .bin，实际 %s", result.Data)
	}
}
