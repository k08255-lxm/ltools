//go:build windows

package screenshot2

import (
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	dwmapiDLL                   = syscall.NewLazyDLL("dwmapi.dll")
	procDwmFlush                = dwmapiDLL.NewProc("DwmFlush")
	procDwmIsCompositionEnabled = dwmapiDLL.NewProc("DwmIsCompositionEnabled")

	// 测试注入点：生产实现由 init 后的默认值提供，测试可临时替换。
	dwmFlushFn              = dwmFlush
	dwmCompositionEnabledFn = dwmCompositionEnabled
)

// dwmFlush 等待 DWM 完成下一次桌面合成。
// DwmFlush 无参数、不依赖窗口句柄：它阻塞到当前合成周期结束，
// 因此在 ShowWindow(SW_HIDE) 返回后调用可以确保隐藏结果已被 DWM 呈现，
// 之后 GDI BitBlt 抓屏才不会包含该窗口的残影。
//
// 注意：DwmFlush 没有超时参数，实际阻塞时长由 DWM 合成节奏决定
// （正常为一个刷新周期，系统负载高时可能更久），调用方不应对该步
// 假设硬超时上界；可见性轮询部分的有界性由 waitForWindowsHidden 保证。
//
// 返回值是 Win32 HRESULT：按 FAILED 宏语义（有符号 < 0）判定失败，
// 并把真实 HRESULT 报告在错误信息中（不用 LastError 冒充 HRESULT）。
func dwmFlush() error {
	ret, _, _ := procDwmFlush.Call()
	if int32(ret) < 0 { // FAILED(hr): 严重性位（bit 31）置位即失败；S_OK(0)/S_FALSE(1) 均为成功
		return fmt.Errorf("DwmFlush failed: HRESULT 0x%08X", uint32(ret))
	}
	return nil
}

// dwmCompositionEnabled 查询 DWM 合成是否开启（Win10+ 恒为开启，但做防御性检查）。
func dwmCompositionEnabled() bool {
	var enabled int32
	ret, _, _ := procDwmIsCompositionEnabled.Call(uintptr(unsafe.Pointer(&enabled)))
	return ret == 0 && enabled != 0
}

// confirmDWMQuiet 在 DWM 合成开启时等待合成呈现（两个合成周期）。
//
// 返回 nil 仅代表：DWM 关闭（无合成可等待，按平台能力此时只做
// 可见性确认）或两次 DwmFlush 均成功（隐藏后的帧已被呈现到屏幕）。
// DwmFlush 失败时返回携带真实 HRESULT 的错误 —— 此时不得声称
// "已确认合成更新"，由调用方决定是否放弃采集。
func confirmDWMQuiet() error {
	if !dwmCompositionEnabledFn() {
		// DWM 关闭：平台能力所限，仅可见性确认（IsVisible 轮询），无合成可等待。
		log.Printf("[Screenshot2] DWM composition disabled: visibility-only confirmation")
		return nil
	}
	// 等两个合成周期，确保隐藏后的帧已被 DWM 呈现到屏幕
	for i := 0; i < 2; i++ {
		if err := dwmFlushFn(); err != nil {
			// DwmFlush 无超时参数；失败时不继续第二次调用，
			// 也不得向调用方报告"合成已确认"。
			return err
		}
	}
	return nil
}

// waitForWindowsHidden 等待给定窗口真正从屏幕上消失，并确认 DWM 合成已更新。
//
// 语义说明（基于 Wails v3.0.0-alpha.74 源码核实）：
//   - WebviewWindow.Hide() 内部经 InvokeSync 分派到主线程执行 ShowWindow(SW_HIDE)，
//     返回时 Win32 调用已完成，但 DWM 合成帧可能尚未更新；
//   - 本函数以 10ms 间隔轮询 IsVisible()（Wails 封装的 IsWindowVisible），
//     这一段受 timeout 有界；全部不可见后再等 DWM 合成呈现；
//   - DwmFlush 本身没有超时参数（见 dwmFlush 注释），因此整段等待
//     不是硬有界的：可见性轮询有界，合成确认由 DWM 节奏决定。
//   - 所有等待发生在调用方 goroutine，不阻塞 UI 线程。
//
// 返回 nil 表示确认完成；返回错误表示超时仍有窗口可见，或 DWM 开启时
// 合成确认失败（两种情况都不应开始采集）。
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
			// IsVisible 内部经 InvokeSyncWithResult 查询主线程（已销毁窗口返回 false），
			// 每次调用开销小（一次跨线程往返），轮询安全。
			if w.IsVisible() {
				allHidden = false
				break
			}
		}

		if allHidden {
			return confirmDWMQuiet()
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
