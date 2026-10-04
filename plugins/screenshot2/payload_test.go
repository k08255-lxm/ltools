package screenshot2

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildImageDeliveryJS 必须生成语法安全、参数完整的窗口定向 JS。

func TestBuildImageDeliveryJSBasic(t *testing.T) {
	session := "screenshot2-20261004120000-1"
	dataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="

	js := buildImageDeliveryJS(session, dataURL)

	if !strings.Contains(js, imageDeliveryFnName) {
		t.Fatalf("JS should reference receiver function %q", imageDeliveryFnName)
	}
	// 两次出现：typeof 检查 + 调用
	if got := strings.Count(js, imageDeliveryFnName); got != 2 {
		t.Fatalf("expected receiver function to appear twice, got %d", got)
	}
	if !strings.Contains(js, jsonEscape(t, session)) {
		t.Fatalf("JS should contain escaped session id")
	}
	if !strings.Contains(js, jsonEscape(t, dataURL)) {
		t.Fatalf("JS should contain escaped data url")
	}
	if !strings.HasPrefix(js, "(function(){") || !strings.HasSuffix(js, "})();") {
		t.Fatalf("JS should be a self-executing function, got prefix/suffix: %q / %q", js[:14], js[len(js)-6:])
	}
}

func TestBuildImageDeliveryJSEscapesSpecialChars(t *testing.T) {
	// 恶意/边界输入：引号、反斜杠、换行、JS 行终止符 U+2028/U+2029
	// （这些字符在 JS 字符串字面量中合法但 JSON 转义后同样安全）
	nasty := "a\"b\\c\nd\te\u2028f\u2029g</script>h"

	js := buildImageDeliveryJS(nasty, nasty)

	// 转义后的载荷不得包含未转义的双引号包裹之外的原生换行/行终止符
	if strings.Contains(js, "\u2028") || strings.Contains(js, "\u2029") {
		t.Fatal("raw U+2028/U+2029 must be escaped in payload")
	}
	if !strings.Contains(js, jsonEscape(t, nasty)) {
		t.Fatalf("payload must contain JSON-escaped form of input")
	}
}

func TestBuildImageDeliveryJSLargePayloadShape(t *testing.T) {
	// 模拟多 MB base64 载荷：确认不发生复制截断/结构破坏
	big := "data:image/png;base64," + strings.Repeat("QUJD", 700*1024) // ~2.8MB
	js := buildImageDeliveryJS("sess", big)
	if !strings.Contains(js, jsonEscape(t, big)) {
		t.Fatal("large payload must be embedded verbatim (escaped)")
	}
	if len(js) < len(big) {
		t.Fatal("payload length sanity check failed")
	}
}

func TestImageDeliveryFnNameStable(t *testing.T) {
	// 前端 Overlay.tsx 挂载同名函数；此测试锁定契约
	if imageDeliveryFnName != "__screenshot2SetImage" {
		t.Fatalf("receiver function name contract changed: %q", imageDeliveryFnName)
	}
}

// jsonEscape 用 encoding/json 生成期望的转义形式（与实现同一机制，
// 这里校验的是"实现使用了 JSON 转义"这一安全属性与拼接结构正确性）
func jsonEscape(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	return string(b)
}
