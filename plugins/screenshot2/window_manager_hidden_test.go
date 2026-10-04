package screenshot2

import (
	"errors"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ---- 隐藏确认失败：不得采集，会话完整收尾 ----

// 注入隐藏确认失败，验证：
//  1. StartCapture 返回 ErrWindowsNotHidden（明确错误）；
//  2. 采集器（capturer）从未被调用；
//  3. 会话完整收尾：currentSession 清空、isCapturing=false、覆盖窗口映射清空。
func TestStartCaptureAbortsWhenHideConfirmationFails(t *testing.T) {
	m := NewWindowManager(NewScreenshot2Plugin(), nil)

	captureCalled := false
	m.captureFn = func() (map[int]*CaptureResult, error) {
		captureCalled = true
		return map[int]*CaptureResult{}, nil
	}

	origWait := waitForWindowsHiddenFn
	waitForWindowsHiddenFn = func(_ []*application.WebviewWindow, _ time.Duration) error {
		return errors.New("injected: window still visible")
	}
	defer func() { waitForWindowsHiddenFn = origWait }()

	_, err := m.StartCapture()
	if err == nil {
		t.Fatal("StartCapture must fail when hide confirmation fails")
	}
	if !errors.Is(err, ErrWindowsNotHidden) {
		t.Fatalf("error should wrap ErrWindowsNotHidden, got: %v", err)
	}
	if captureCalled {
		t.Fatal("capturer must not be called when hide confirmation failed")
	}
	if s := m.currentSessionSnapshot(); s != nil {
		t.Fatalf("session must be cleared after failed start, got %v", s.ID())
	}
	if m.IsCapturing() {
		t.Fatal("isCapturing must be false after failed start")
	}
	if len(m.windows) != 0 {
		t.Fatalf("overlay windows map must be empty after failed start, got %d", len(m.windows))
	}
}

// 隐藏确认失败后的收尾只执行一次恢复：再次结束会话不得重复恢复辅助窗口。
func TestEndSessionRestoresAuxWindowsExactlyOnce(t *testing.T) {
	m := NewWindowManager(NewScreenshot2Plugin(), nil)

	restoreCalls := 0
	hideCalls := 0
	m.SetAuxWindowHooks(
		func(_ *application.WebviewWindow) bool {
			hideCalls++
			return false // 由通用路径 Hide
		},
		func(_ *application.WebviewWindow) bool {
			restoreCalls++
			return true // 外部服务已恢复，跳过通用 Show
		},
	)

	w := &application.WebviewWindow{}
	m.mu.Lock()
	m.auxHidden = []*application.WebviewWindow{w}
	session := newCaptureSession("restore-once")
	m.currentSession = session
	m.mu.Unlock()

	m.endSessionLocked(session, false)
	if restoreCalls != 1 {
		t.Fatalf("restore must run exactly once after first end, got %d", restoreCalls)
	}
	if hideCalls != 0 {
		t.Fatalf("hide hook must not run during endSession, got %d", hideCalls)
	}
	m.mu.Lock()
	restored := m.auxHidden
	m.mu.Unlock()
	if restored != nil {
		t.Fatal("auxHidden must be cleared after restore")
	}

	// 幂等：重复收尾不重复恢复
	m.endSessionLocked(session, false)
	if restoreCalls != 1 {
		t.Fatalf("restore must not run again on repeated end, got %d", restoreCalls)
	}
}

// ---- 旧覆盖层：hide 后 close 并纳入隐藏确认 ----

func TestIsOverlayAndPinWindowName(t *testing.T) {
	if !isOverlayWindowName("screenshot2-overlay-0") {
		t.Fatal("overlay prefix should match")
	}
	if isOverlayWindowName("screenshot2-overlay") {
		t.Fatal("bare prefix should not match")
	}
	if isOverlayWindowName("pin-window-1") || isOverlayWindowName("ltools") {
		t.Fatal("non-overlay names should not match")
	}
	if !isPinWindowName("pin-window-1") {
		t.Fatal("pin prefix should match")
	}
	if isPinWindowName("pin-window") {
		t.Fatal("bare prefix should not match")
	}
}
