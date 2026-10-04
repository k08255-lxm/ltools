//go:build windows

package screenshot2

import (
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	user32DLL        = syscall.NewLazyDLL("user32.dll")
	procGetCursorPos = user32DLL.NewProc("GetCursorPos")
)

type pointStruct struct {
	X, Y int32
}

// GetGlobalMousePosition 获取全局鼠标位置（Windows）。
// 返回物理像素坐标（Wails 在 Windows 上声明了 PerMonitorV2 DPI 感知），
// 与 application.Screen.PhysicalBounds 同一坐标系。
func GetGlobalMousePosition() (x, y float64) {
	var pt pointStruct
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if ret == 0 {
		return 0, 0
	}
	return float64(pt.X), float64(pt.Y)
}

// globalMouseDisplayIndex 判断鼠标当前所在的显示器索引（-1 表示不在任何已知屏幕上）。
// Windows：GetCursorPos 返回物理像素坐标，因此与 Screen.PhysicalBounds 比较。
func globalMouseDisplayIndex(screens []*application.Screen) int {
	mouseX, mouseY := GetGlobalMousePosition()

	for i, screen := range screens {
		if screen == nil {
			continue
		}
		left := float64(screen.PhysicalBounds.X)
		top := float64(screen.PhysicalBounds.Y)
		right := float64(screen.PhysicalBounds.X + screen.PhysicalBounds.Width)
		bottom := float64(screen.PhysicalBounds.Y + screen.PhysicalBounds.Height)
		if mouseX >= left && mouseX < right && mouseY >= top && mouseY < bottom {
			return i
		}
	}
	return -1
}
