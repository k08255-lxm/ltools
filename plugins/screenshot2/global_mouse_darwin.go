//go:build darwin

package screenshot2

/*
#cgo darwin CFLAGS: -x objective-c
#cgo darwin LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

static id globalMonitor = nil;

void StartGlobalMouseMonitor() {
    if (globalMonitor != nil) return;

    globalMonitor = [NSEvent addGlobalMonitorForEventsMatchingMask:NSEventMaskMouseMoved
        handler:^(NSEvent *event) {
            NSPoint location = [event locationInWindow];
            // 发送通知到 Go
            NSDictionary *userInfo = @{@"x": @(location.x), @"y": @(location.y)};
            [[NSNotificationCenter defaultCenter] postNotificationName:@"GlobalMouseMove"
                                                                object:nil
                                                              userInfo:userInfo];
        }];
}

void StopGlobalMouseMonitor() {
    if (globalMonitor != nil) {
        [NSEvent removeMonitor:globalMonitor];
        globalMonitor = nil;
    }
}

void GetGlobalMousePosition(double *x, double *y) {
    NSPoint location = [NSEvent mouseLocation];
    *x = location.x;
    *y = location.y;
}
*/
import "C"

import (
	"log"
	"sync"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	globalMouseCallback     func(x, y float64)
	globalMouseCallbackLock sync.Mutex
	notificationObserver    unsafe.Pointer
)

// StartGlobalMouseMonitor 开始全局鼠标监控
func StartGlobalMouseMonitor(app *application.App, callback func(x, y float64)) {
	globalMouseCallbackLock.Lock()
	globalMouseCallback = callback
	globalMouseCallbackLock.Unlock()

	C.StartGlobalMouseMonitor()
	log.Printf("[GlobalMouse] Started global mouse monitor")

	// 监听 macOS 通知
	if app != nil {
		// 使用 Wails 事件系统定期检查鼠标位置
		// 这里我们通过轮询方式实现
	}
}

// StopGlobalMouseMonitor 停止全局鼠标监控
func StopGlobalMouseMonitor() {
	C.StopGlobalMouseMonitor()

	globalMouseCallbackLock.Lock()
	globalMouseCallback = nil
	globalMouseCallbackLock.Unlock()

	log.Printf("[GlobalMouse] Stopped global mouse monitor")
}

// GetGlobalMousePosition 获取全局鼠标位置
func GetGlobalMousePosition() (x, y float64) {
	var cx, cy C.double
	C.GetGlobalMousePosition(&cx, &cy)
	return float64(cx), float64(cy)
}

// CheckMousePositionAndGetDisplay 检查鼠标位置并返回所在显示器索引
func CheckMousePositionAndGetDisplay(displays []DisplayInfo) int {
	x, y := GetGlobalMousePosition()

	// macOS 坐标系：原点在左下角
	// 需要将显示器信息转换为 macOS 坐标系进行比较
	for i, d := range displays {
		// 检查鼠标是否在该显示器范围内
		if x >= float64(d.X) && x < float64(d.X+d.Width) &&
			y >= float64(d.Y) && y < float64(d.Y+d.Height) {
			return i
		}
	}
	return -1
}

// globalMouseDisplayIndex 判断鼠标当前所在的显示器索引（-1 表示不在任何已知屏幕上）。
// macOS：沿用既有逻辑坐标比较（NSEvent mouseLocation 为逻辑坐标），
// 保持与改造前 window_manager.go 内联逻辑一致的行为。
func globalMouseDisplayIndex(screens []*application.Screen) int {
	mouseX, mouseY := GetGlobalMousePosition()

	for i, screen := range screens {
		if screen == nil {
			continue
		}
		// NSEvent mouseLocation 返回的是逻辑坐标，screen.X/Y 也是逻辑坐标
		// 直接比较逻辑坐标，不需要乘以 ScaleFactor
		right := float64(screen.X + screen.Size.Width)
		top := float64(screen.Y + screen.Size.Height)
		if mouseX >= float64(screen.X) && mouseX < right &&
			mouseY >= float64(screen.Y) && mouseY < top {
			return i
		}
	}
	return -1
}
