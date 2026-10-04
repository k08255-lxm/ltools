//go:build !darwin && !windows

package screenshot2

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// StartGlobalMouseMonitor 开始全局鼠标监控（非 macOS/Windows 平台的空实现）
func StartGlobalMouseMonitor(callback func(x, y float64)) {
	log.Printf("[GlobalMouse] Global mouse monitor not implemented on this platform")
}

// StopGlobalMouseMonitor 停止全局鼠标监控
func StopGlobalMouseMonitor() {
	log.Printf("[GlobalMouse] Global mouse monitor stopped")
}

// GetGlobalMousePosition 获取全局鼠标位置
func GetGlobalMousePosition() (x, y float64) {
	return 0, 0
}

// globalMouseDisplayIndex 判断鼠标当前所在的显示器索引（空实现恒返回 -1）
func globalMouseDisplayIndex(screens []*application.Screen) int {
	return -1
}
