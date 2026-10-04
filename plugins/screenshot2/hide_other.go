//go:build !windows

package screenshot2

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// waitForWindowsHidden 等待给定窗口真正隐藏（非 Windows 平台）。
//
// Wails 在 macOS/Linux 上同样通过 InvokeSync 在主线程执行 hide，
// 返回时窗口系统调用已完成；macOS 的 CGWindowListCreateImage 抓取的是
// 窗口服务器合成结果，隐藏调用返回后即不含该窗口。
// 这里仅做有界的 IsVisible 轮询确认（受 timeout 有界），不引入平台特定等待。
//
// 返回 nil 表示确认完成；返回错误表示超时仍有窗口可见（不应开始采集）。
func waitForWindowsHidden(windows []*application.WebviewWindow, timeout time.Duration) error {
	if len(windows) == 0 {
		return nil
	}

	deadline := time.Now().Add(timeout)
	const pollInterval = 10 * time.Millisecond

	for {
		allHidden := true
		for _, w := range windows {
			if w == nil {
				continue
			}
			// 已销毁窗口 IsVisible 返回 false
			if w.IsVisible() {
				allHidden = false
				break
			}
		}
		if allHidden {
			return nil
		}
		if time.Now().After(deadline) {
			visible := 0
			for _, w := range windows {
				if w != nil && w.IsVisible() {
					visible++
				}
			}
			return fmt.Errorf("windows still visible after %s wait: %d/%d", timeout, visible, len(windows))
		}
		time.Sleep(pollInterval)
	}
}
