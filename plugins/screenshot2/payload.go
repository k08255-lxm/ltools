package screenshot2

import (
	"encoding/json"
	"fmt"
)

// imageDeliveryFnName 前端覆盖层在 window 上挂载的图像接收函数名。
// Overlay.tsx 在注册事件监听器之后、上报 FrontendReady 之前挂载，
// 因此 ready 信号保证该函数已就位；ExecJS 在运行时未就绪时进入
// Wails 的 pendingJS 队列，同样不会丢失。
const imageDeliveryFnName = "__screenshot2SetImage"

// buildImageDeliveryJS 构造窗口定向的图像投递 JS。
//
// 使用 json.Marshal 对参数做字符串转义：其输出是合法的 JS 字符串字面量
// （Go 的 encoding/json 会转义 U+2028/U+2029 等字符），对任意载荷安全。
// 图像为 data URL（base64，字符集 [A-Za-z0-9+/=:]），本就 JS 安全；
// 会话 ID 由 generateSessionId 生成，同样是受限字符集。
func buildImageDeliveryJS(sessionID, dataURL string) string {
	sessionJSON, err := json.Marshal(sessionID)
	if err != nil {
		// 会话 ID 来自受限字符集，理论上不可能失败；防御性兜底
		sessionJSON = []byte(`""`)
	}
	dataJSON, err := json.Marshal(dataURL)
	if err != nil {
		dataJSON = []byte(`""`)
	}
	return fmt.Sprintf(
		"(function(){try{if(typeof window.%s==='function'){window.%s(%s,%s);}else{console.warn('[Screenshot2Overlay] receiver not ready, dropping image');}}catch(e){console.error('[Screenshot2Overlay] image delivery failed:',e);}})();",
		imageDeliveryFnName, imageDeliveryFnName, sessionJSON, dataJSON,
	)
}
