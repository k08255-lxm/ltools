package screenshot2

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// WindowManager 管理多显示器截图窗口
// 微信风格：每个显示器一个全屏窗口
//
// 并发模型（本轮改造）：
//   - sessionMu：串行化"会话启动 / 收尾"两个状态迁移入口（StartCapture、
//     CancelSession、CloseAllWindows）。前端就绪等待、图像编码与投递等
//     慢操作不持有任何全局锁，取消信号（session.cancel）可随时打断。
//   - mu：短临界区保护 windows / mainWindow / isCapturing / currentSession /
//     auxHidden / monitorCancel。
//   - screensMu：保护 wailsScreens（鼠标监控 goroutine 只在启动时读取一次快照）。
type WindowManager struct {
	app    *application.App
	plugin *Screenshot2Plugin

	mu             sync.RWMutex
	windows        map[int]*application.WebviewWindow // displayIndex -> window
	mainWindow     *application.WebviewWindow         // 主窗口引用
	isCapturing    bool
	currentSession *captureSession
	auxHidden      []*application.WebviewWindow // 本次会话中被本服务隐藏的辅助窗口（结束时恢复）

	// 辅助窗口定向隐藏/恢复回调（main.go 注入，如搜索窗口）：
	// 返回 true 表示该窗口由外部服务处理——隐藏/恢复动作与其内部可见状态
	// 由回调完成，本服务跳过通用 Hide/Show，但仍计入隐藏确认与恢复列表。
	auxHide    func(*application.WebviewWindow) bool
	auxRestore func(*application.WebviewWindow) bool

	sessionMu sync.Mutex // 会话状态迁移互斥（StartCapture / CancelSession / CloseAllWindows）

	// captureFn 测试注入点：非 nil 时取代 plugin.CaptureAllDisplaysSeparately，
	// 用于验证"隐藏确认失败时不调用采集器"等失败路径。
	captureFn func() (map[int]*CaptureResult, error)

	screensMu    sync.RWMutex
	wailsScreens []*application.Screen // Wails 屏幕信息（正确的坐标系）

	monitorCancel chan struct{} // 停止鼠标监控的信号
	monitorWG     sync.WaitGroup
}

// 会话错误
var (
	ErrCreateWindowFailed = errors.New("failed to create window")
	ErrCaptureCancelled   = errors.New("capture cancelled")
	// ErrWindowsNotHidden 隐藏确认未成功（仍有窗口可见或合成确认失败），
	// 会话已完整收尾，本次不进行采集。
	ErrWindowsNotHidden = errors.New("windows not confirmed hidden before capture")
)

// waitForWindowsHiddenFn 隐藏确认入口（测试可替换，注入确认失败场景）。
var waitForWindowsHiddenFn = waitForWindowsHidden

// NewWindowManager creates a new window manager
func NewWindowManager(plugin *Screenshot2Plugin, app *application.App) *WindowManager {
	return &WindowManager{
		app:     app,
		plugin:  plugin,
		windows: make(map[int]*application.WebviewWindow),
	}
}

// SetMainWindow sets the main window reference
func (m *WindowManager) SetMainWindow(window *application.WebviewWindow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mainWindow = window
}

// SetAuxWindowHooks 注入辅助窗口的定向隐藏/恢复回调（如搜索窗口）。
// hide(w)/restore(w) 返回 true 表示该窗口由外部服务处理：隐藏/恢复动作与
// 其内部可见状态由回调完成，本服务跳过通用的 Hide/Show，但窗口仍计入
// 隐藏确认列表（waitForWindowsHidden）与会话恢复列表（auxHidden）。
// 这样既复用统一的生命周期管理，又能同步外部服务自持的可见状态
// （SearchWindowService.isVisible 等），消除"截图隐藏后搜索热键判定错乱"。
// main.go 在窗口创建后注入；未注入时行为与纯通用遍历完全一致。
func (m *WindowManager) SetAuxWindowHooks(hide, restore func(*application.WebviewWindow) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auxHide = hide
	m.auxRestore = restore
}

// ServiceStartup is called when the application starts
func (m *WindowManager) ServiceStartup(app *application.App) error {
	log.Printf("[WindowManager] Service startup")
	return nil
}

// ServiceShutdown is called when the application shuts down
func (m *WindowManager) ServiceShutdown(app *application.App) error {
	m.CloseAllWindows()
	return nil
}

// currentSessionSnapshot 返回当前活跃会话（可能为 nil）。
func (m *WindowManager) currentSessionSnapshot() *captureSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentSession
}

// OnFrontendReady 前端调用此方法通知已加载完成。
// sessionId 用于会话隔离：不属于当前会话的迟到/过期 ready 直接丢弃，
// 同一显示器的重复 ready 只计一次。
func (m *WindowManager) OnFrontendReady(sessionId string, displayIndex int) {
	session := m.currentSessionSnapshot()
	if session == nil {
		log.Printf("[WindowManager] Dropping stale FrontendReady(display=%d): no active session", displayIndex)
		return
	}
	if session.ID() != sessionId {
		log.Printf("[WindowManager] Dropping stale FrontendReady(display=%d, session=%q): current session is %q",
			displayIndex, sessionId, session.ID())
		return
	}
	if session.markReady(displayIndex) {
		got, expected := session.expectedReady()
		log.Printf("[WindowManager] Display %d ready (%d/%d)", displayIndex, got, expected)
	}
}

// waitForFrontendReady 等待所有期望的前端就绪，带超时，并随时响应取消。
// 返回 (已就绪数, 期望数, 是否被取消)。
func (m *WindowManager) waitForFrontendReady(session *captureSession, timeout time.Duration) (got, expected int, cancelled bool) {
	got, expected = session.expectedReady()
	if got >= expected && expected > 0 {
		return got, expected, false
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		select {
		case <-session.readyCh():
			if session.isReadyComplete() {
				got, expected = session.expectedReady()
				return got, expected, false
			}
		case <-session.cancelledCh():
			got, expected = session.expectedReady()
			return got, expected, true
		case <-deadline.C:
			got, expected = session.expectedReady()
			return got, expected, false
		}
	}
}

// readyTimeout 根据窗口数量计算就绪等待上限：
// 首窗 2s（与旧行为一致），每多一个窗口追加 500ms，总上限 6s。
func readyTimeout(windowCount int) time.Duration {
	timeout := 2*time.Second + time.Duration(windowCount-1)*500*time.Millisecond
	if timeout > 6*time.Second {
		timeout = 6 * time.Second
	}
	return timeout
}

// StartCapture 开始截图流程。
//
// 与旧实现的关键差异：
//  1. 开始前的预清理不再复用"用户结束"路径 —— 不会在启动时把主窗口
//     Show 出来再 Hide（消除主窗口闪回）。
//  2. 截图前显式确认主窗口、辅助窗口（搜索窗口等）与旧覆盖层真正隐藏，
//     并等待 DWM 桌面合成更新（Windows 上有界等待，不使用固定 sleep，
//     不阻塞 UI 线程）。
//  3. 全局锁不再贯穿前端就绪等待与图像投递：取消/就绪信号随时可达。
//  4. 重复启动幂等：会话进行中再次调用返回当前会话 ID。
//  5. 零窗口 / 部分就绪失败 / 取消都会完整收尾（关窗、恢复辅助窗口、
//     恢复主窗口），不会留下空白覆盖层。
func (m *WindowManager) StartCapture() (string, error) {
	startTime := time.Now()
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()

	log.Printf("[WindowManager] [TIMING] Starting capture...")

	// 幂等防重：已有活跃会话 → 返回其会话 ID（防热键连按/双击重复启动）
	if active := m.currentSessionSnapshot(); active != nil && !active.isDone() {
		if !active.IsCancelled() {
			log.Printf("[WindowManager] Capture already active (session %s), returning existing session", active.ID())
			return active.ID(), nil
		}
		// 已标记取消但尚未收尾完成 → 等待收尾（有界），超时则强制收尾
		if !active.waitDone(2 * time.Second) {
			log.Printf("[WindowManager] Previous cancelled session %s did not finish in time, forcing cleanup", active.ID())
			m.endSessionLocked(active, false)
		}
	}

	// 预清理旧覆盖层/监控（若有残留）—— 注意：此处绝不显示主窗口
	m.cleanupWindowsLocked()
	log.Printf("[WindowManager] [TIMING] After cleanup: %v", time.Since(startTime))

	// 开启新会话
	session := newCaptureSession(generateSessionId())
	m.mu.Lock()
	m.currentSession = session
	m.mu.Unlock()

	// ---- 隐藏阶段（截图前） ----
	hideTargets := m.hideAllWindowsForCaptureLocked(session)

	log.Printf("[WindowManager] [TIMING] Before waiting hidden: %v", time.Since(startTime))
	if err := waitForWindowsHiddenFn(hideTargets, 500*time.Millisecond); err != nil {
		// 隐藏确认未成功：绝不采集已知仍可见的窗口（旧帧/主窗口残影）。
		// 完整收尾本会话（结束会话、恢复由会话隐藏的窗口），并返回明确错误。
		log.Printf("[WindowManager] Hide confirmation failed: %v — aborting capture, ending session", err)
		m.endSessionLocked(session, true)
		return "", fmt.Errorf("%w: %v", ErrWindowsNotHidden, err)
	}
	log.Printf("[WindowManager] [TIMING] After windows hidden (confirmed=%d): %v", len(hideTargets), time.Since(startTime))

	// ---- 屏幕解析 ----
	screens := m.resolveScreensLocked(session)
	if len(screens) == 0 {
		m.endSessionLocked(session, true)
		return "", fmt.Errorf("no active displays found")
	}
	for i, screen := range screens {
		log.Printf("[WindowManager] Screen %d: Name=%s, Position=(%d,%d), Size=%dx%d, Scale=%.2f, Primary=%v",
			i, screen.Name, screen.X, screen.Y, screen.Size.Width, screen.Size.Height, screen.ScaleFactor, screen.IsPrimary)
	}

	// 响应取消
	if session.IsCancelled() {
		m.endSessionLocked(session, true)
		return "", ErrCaptureCancelled
	}

	// ---- 截图 ----
	log.Printf("[WindowManager] [TIMING] Before CaptureAllDisplaysSeparately: %v", time.Since(startTime))
	captureResults, err := m.captureAll()
	if err != nil {
		m.endSessionLocked(session, true)
		return "", err
	}
	log.Printf("[WindowManager] [TIMING] After CaptureAllDisplaysSeparately: %v", time.Since(startTime))

	// ---- 进入 Kiosk 模式并创建覆盖窗口 ----
	EnterKioskMode()

	// 在窗口创建前声明期望显示器集合：任何在创建间隙早到的 ready
	// 都会被正确计数（不存在事件早到丢失的窗口期）；创建失败的索引
	// 随后从集合中剔除。
	expectedIndexes := make([]int, len(screens))
	for i := range screens {
		expectedIndexes[i] = i
	}
	session.setExpectedDisplays(expectedIndexes)

	created := 0
	for i, screen := range screens {
		// macOS Retina 显示器需要使用物理像素坐标定位窗口
		// 物理坐标 = 逻辑坐标 * ScaleFactor
		physicalX := int(float64(screen.X) * float64(screen.ScaleFactor))
		physicalY := int(float64(screen.Y) * float64(screen.ScaleFactor))

		// 创建与截图库索引对应的 DisplayInfo
		// 窗口尺寸使用逻辑尺寸，位置使用物理坐标
		display := DisplayInfo{
			Index:       i,
			Width:       screen.Size.Width,
			Height:      screen.Size.Height,
			X:           physicalX,
			Y:           physicalY,
			Primary:     screen.IsPrimary,
			Name:        screen.Name,
			ScaleFactor: float64(screen.ScaleFactor),
		}

		captureResult := captureResults[i]
		if captureResult == nil {
			log.Printf("[WindowManager] Warning: no capture result for display %d", i)
			session.removeExpectedDisplay(i)
			continue
		}

		if err := m.createWindowForDisplay(display, captureResult, session.ID()); err != nil {
			log.Printf("[WindowManager] Failed to create window for display %d: %v", i, err)
			session.removeExpectedDisplay(i)
			continue
		}
		created++
	}
	log.Printf("[WindowManager] [TIMING] After create all windows (created=%d): %v", created, time.Since(startTime))

	// 零窗口：无法展示任何覆盖层 → 完整收尾并报错（不进入截图会话）
	if created == 0 {
		m.endSessionLocked(session, true)
		return "", fmt.Errorf("failed to create any overlay window (displays=%d)", len(screens))
	}

	// 响应取消
	if session.IsCancelled() {
		m.endSessionLocked(session, true)
		return "", ErrCaptureCancelled
	}

	// ---- 等待前端就绪（不持有任何全局锁，取消随时可达） ----
	log.Printf("[WindowManager] Waiting for frontend to load...")
	waitStart := time.Now()
	got, expected, cancelled := m.waitForFrontendReady(session, readyTimeout(created))
	log.Printf("[WindowManager] [TIMING] waitForFrontendReady took %v (got %d/%d, cancelled=%v), total: %v",
		time.Since(waitStart), got, expected, cancelled, time.Since(startTime))

	if cancelled {
		m.endSessionLocked(session, true)
		return "", ErrCaptureCancelled
	}
	if got < expected {
		// 部分失败/超时：收尾关闭全部覆盖层，绝不让空白覆盖层留在屏幕上
		m.endSessionLocked(session, true)
		return "", fmt.Errorf("overlay windows not ready in time: %d/%d", got, expected)
	}

	// ---- 广播显示器信息与会话开始 ----
	displays := m.plugin.GetDisplays()
	displaysJSON, _ := json.Marshal(displays)
	m.broadcastEvent("displays-info", string(displaysJSON))
	m.broadcastEvent("session-start", session.ID())

	// ---- 窗口定向推送图像（避免全局大图广播） ----
	m.sendImagesToWindows(captureResults, session.ID())

	m.mu.Lock()
	m.isCapturing = true
	m.mu.Unlock()
	m.emitEvent("started", "capture started")

	// ---- 启动全局鼠标监控（仅活动截图期间运行） ----
	m.startGlobalMouseMonitor(screens)

	log.Printf("[WindowManager] [TIMING] Capture started with %d windows, TOTAL: %v", created, time.Since(startTime))
	return session.ID(), nil
}

// captureAll 执行多显示器截图（测试可注入 captureFn 以观测调用）。
func (m *WindowManager) captureAll() (map[int]*CaptureResult, error) {
	if m.captureFn != nil {
		return m.captureFn()
	}
	return m.plugin.CaptureAllDisplaysSeparately()
}

// hideAllWindowsForCaptureLocked 隐藏主窗口与其它可见辅助窗口（含搜索窗口），
// 返回需要确认隐藏的窗口列表。记录被本服务隐藏的辅助窗口以便会话结束时恢复。
// 调用方需持有 sessionMu。
func (m *WindowManager) hideAllWindowsForCaptureLocked(session *captureSession) []*application.WebviewWindow {
	var targets []*application.WebviewWindow

	// 主窗口
	m.mu.Lock()
	mainWindow := m.mainWindow
	if mainWindow != nil {
		log.Printf("[WindowManager] Hiding main window before capture...")
		mainWindow.Hide()
		targets = append(targets, mainWindow)
	}
	m.mu.Unlock()

	// 其它可见辅助窗口（搜索窗口、贴图窗口等）。
	// 旧覆盖层（上次会话残留的 overlay）不跳过：先 Hide 再 Close，并纳入
	// 隐藏确认列表 —— 若仅按名称排除，Close 未完成时旧帧可能被采集。
	// 贴图（pin）窗口不属于覆盖层，保持原样（其内容本就是屏幕的一部分）。
	// 回调快照后调用：不在持有 m.mu 的情况下执行外部回调（回调内部会获取
	// 外部服务的锁，如 SearchWindowService.mu）。
	m.mu.RLock()
	auxHide := m.auxHide
	m.mu.RUnlock()

	if app := application.Get(); app != nil && app.Window != nil {
		var hidden []*application.WebviewWindow
		for _, win := range app.Window.GetAll() {
			w, ok := win.(*application.WebviewWindow)
			if !ok || w == nil || w == mainWindow {
				continue
			}
			name := w.Name()
			if isPinWindowName(name) {
				continue
			}
			if isOverlayWindowName(name) {
				// 旧覆盖层残留：先隐藏再关闭，并纳入采集前的隐藏确认
				w.Hide()
				w.Close()
				targets = append(targets, w)
				log.Printf("[WindowManager] Hid and closed leftover overlay %q before capture", name)
				continue
			}
			if w.IsVisible() {
				// 定向回调优先（同步外部服务自持的可见状态）；
				// 无论谁执行隐藏动作，窗口都计入确认与恢复列表
				if !(auxHide != nil && auxHide(w)) {
					w.Hide()
				}
				hidden = append(hidden, w)
				targets = append(targets, w)
			}
		}
		if len(hidden) > 0 {
			m.mu.Lock()
			m.auxHidden = hidden
			m.mu.Unlock()
			for _, w := range hidden {
				log.Printf("[WindowManager] Hid aux window %q for capture", w.Name())
			}
		}
	}

	return targets
}

// isOverlayWindowName 判断窗口名是否属于截图覆盖层窗口。
func isOverlayWindowName(name string) bool {
	const prefix = "screenshot2-overlay-"
	return len(name) >= len(prefix) && name[:len(prefix)] == prefix
}

// isPinWindowName 判断窗口名是否属于贴图窗口。
func isPinWindowName(name string) bool {
	const prefix = "pin-window-"
	return len(name) >= len(prefix) && name[:len(prefix)] == prefix
}

// resolveScreensLocked 解析屏幕信息：优先 Wails ScreenManager（含 macOS 手动
// 初始化变通），完全失败时回退截图库构造伪屏幕（保持旧回退路径语义）。
// 同时把结果快照给鼠标监控使用。调用方需持有 sessionMu。
func (m *WindowManager) resolveScreensLocked(session *captureSession) []*application.Screen {
	// 1) application.Get() 优先
	var screens []*application.Screen
	app := application.Get()
	if app != nil && app.Screen != nil {
		screens = app.Screen.GetAll()
		if len(screens) == 0 {
			// Wails v3 macOS 已知问题：ScreenManager 未自动填充
			log.Printf("[WindowManager] Attempting to manually initialize ScreenManager...")
			screens = m.initScreensManually(app)
		}
	}

	// 2) 回退 m.app
	if len(screens) == 0 && m.app != nil && m.app.Screen != nil {
		screens = m.app.Screen.GetAll()
		if len(screens) == 0 {
			log.Printf("[WindowManager] Attempting to manually initialize ScreenManager via m.app...")
			screens = m.initScreensManually(m.app)
		}
	}

	// 3) 截图库回退：构造伪屏幕（等价旧 fallback 路径的窗口创建语义）
	if len(screens) == 0 {
		log.Printf("[WindowManager] Warning: Wails returned 0 screens, falling back to screenshot library")
		displays := m.plugin.GetDisplays()
		if len(displays) == 0 {
			log.Printf("[WindowManager] initScreensManually: no displays from screenshot library")
			return nil
		}
		screens = buildScreensFromDisplays(displays)
	}

	m.screensMu.Lock()
	m.wailsScreens = screens
	m.screensMu.Unlock()
	return screens
}

// buildScreensFromDisplays 用截图库显示器信息构造 Wails Screen 列表。
// 重要：PhysicalBounds 必须是物理像素尺寸（逻辑尺寸 * ScaleFactor）。
func buildScreensFromDisplays(displays []DisplayInfo) []*application.Screen {
	screenCount := len(displays)
	screens := make([]*application.Screen, screenCount)
	for i, display := range displays {
		physicalWidth := int(float64(display.Width) * display.ScaleFactor)
		physicalHeight := int(float64(display.Height) * display.ScaleFactor)

		screens[i] = &application.Screen{
			ID:               fmt.Sprintf("display-%d", display.Index),
			Name:             display.Name,
			X:                display.X,
			Y:                display.Y,
			Size:             application.Size{Width: display.Width, Height: display.Height},
			Bounds:           application.Rect{X: display.X, Y: display.Y, Width: display.Width, Height: display.Height},
			PhysicalBounds:   application.Rect{X: display.X, Y: display.Y, Width: physicalWidth, Height: physicalHeight},
			WorkArea:         application.Rect{X: display.X, Y: display.Y, Width: display.Width, Height: display.Height},
			PhysicalWorkArea: application.Rect{X: display.X, Y: display.Y, Width: physicalWidth, Height: physicalHeight},
			IsPrimary:        display.Primary,
			ScaleFactor:      float32(display.ScaleFactor),
		}
		log.Printf("[WindowManager] buildScreensFromDisplays: Screen %d - Name=%s, Position=(%d,%d), LogicalSize=%dx%d, PhysicalSize=%dx%d, Scale=%.2f, Primary=%v",
			i, screens[i].Name, screens[i].X, screens[i].Y,
			screens[i].Bounds.Width, screens[i].Bounds.Height,
			screens[i].PhysicalBounds.Width, screens[i].PhysicalBounds.Height,
			screens[i].ScaleFactor, screens[i].IsPrimary)
	}
	return screens
}

// createWindowForDisplay 为指定显示器创建覆盖窗口
func (m *WindowManager) createWindowForDisplay(display DisplayInfo, captureResult *CaptureResult, sessionID string) error {
	log.Printf("[WindowManager] Creating window for display %d at (%d, %d), size: %dx%d, scale: %.1f",
		display.Index, display.X, display.Y, display.Width, display.Height, display.ScaleFactor)

	// 每个窗口使用唯一的名称
	windowName := fmt.Sprintf("screenshot2-overlay-%d", display.Index)

	// URL 携带会话 ID：前端凭它发送 FrontendReady 并校验定向图像，
	// 保证过期会话的窗口不会污染当前会话
	window := m.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:           windowName,
		Title:          fmt.Sprintf("Screenshot - Display %d", display.Index),
		Width:          display.Width,
		Height:         display.Height,
		X:              display.X,
		Y:              display.Y,
		Frameless:      true,
		AlwaysOnTop:    true,
		DisableResize:  true, // 禁止缩放
		BackgroundType: application.BackgroundTypeTransparent,
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBar{
				Hide: true,
			},
			Backdrop:           application.MacBackdropTransparent,
			WindowLevel:        application.MacWindowLevelScreenSaver,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces,
		},
		URL: fmt.Sprintf("/screenshot2-overlay?display=%d&scale=%.1f&session=%s", display.Index, display.ScaleFactor, sessionID),
	})

	if window == nil {
		return ErrCreateWindowFailed
	}

	m.mu.Lock()
	m.windows[display.Index] = window
	m.mu.Unlock()

	// 显示窗口
	window.Show()

	// 显示后再次设置位置（确保在正确的显示器上）
	window.SetPosition(display.X, display.Y)

	// 验证最终位置
	actualX, actualY := window.Position()
	log.Printf("[WindowManager] Window for display %d - requested: (%d, %d), actual after SetPosition: (%d, %d)",
		display.Index, display.X, display.Y, actualX, actualY)

	// 不在这里调用 Focus()，让所有窗口都能接收鼠标事件
	// Focus() 会导致只有一个窗口获得焦点

	log.Printf("[WindowManager] Window created and shown for display %d", display.Index)
	return nil
}

// GetDisplayWindow 获取指定显示器的窗口
func (m *WindowManager) GetDisplayWindow(displayIndex int) *application.WebviewWindow {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.windows[displayIndex]
}

// initScreensManually 手动初始化 ScreenManager
// 这是 Wails v3 macOS 的一个已知问题的变通方案：
// ScreenManager 在 macOS 上没有自动填充屏幕数据
func (m *WindowManager) initScreensManually(app *application.App) []*application.Screen {
	// 使用截图库获取显示器信息
	displays := m.plugin.GetDisplays()
	if len(displays) == 0 {
		log.Printf("[WindowManager] initScreensManually: no displays from screenshot library")
		return nil
	}

	// 调用 LayoutScreens 填充 ScreenManager
	if err := app.Screen.LayoutScreens(buildScreensFromDisplays(displays)); err != nil {
		log.Printf("[WindowManager] initScreensManually: LayoutScreens failed: %v", err)
		return nil
	}

	log.Printf("[WindowManager] initScreensManually: Successfully initialized %d screens", len(displays))
	return app.Screen.GetAll()
}

// sendImagesToWindows 将每个显示器的截图定向推送到对应窗口。
// 使用 WebviewWindow.ExecJS（窗口定向、运行时未就绪时进入 pendingJS 队列，
// 不会丢失），取代旧的 screenshot2:image-data 全局 base64 广播 ——
// 每份图像只发给需要它的那一个窗口，其他窗口不再接收数 MB 的无关数据。
func (m *WindowManager) sendImagesToWindows(captureResults map[int]*CaptureResult, sessionID string) {
	m.mu.RLock()
	windowsSnapshot := make(map[int]*application.WebviewWindow, len(m.windows))
	for idx, w := range m.windows {
		windowsSnapshot[idx] = w
	}
	m.mu.RUnlock()

	for displayIndex, result := range captureResults {
		window, ok := windowsSnapshot[displayIndex]
		if !ok || window == nil {
			log.Printf("[WindowManager] Warning: no window for display %d", displayIndex)
			continue
		}
		payload := buildImageDeliveryJS(sessionID, result.Base64Data)
		window.ExecJS(payload)
		log.Printf("[WindowManager] Sent image for display %d to its window (payload %d bytes)",
			displayIndex, len(payload))
	}
}

// broadcastEvent 向所有窗口广播事件（app 未就绪时跳过，仅记录日志）
func (m *WindowManager) broadcastEvent(eventName, data string) {
	if m.app == nil || m.app.Event == nil {
		log.Printf("[WindowManager] broadcastEvent(%s) skipped: app not ready", eventName)
		return
	}
	m.app.Event.Emit("screenshot2:"+eventName, data)
}

// CancelSession 用户请求取消/结束当前截图会话。
//
// 两个时序：
//  1. 会话尚在启动中（StartCapture 未返回）：仅发出取消信号并等待启动方
//     自行收尾（它持有 sessionMu 且在关键阶段检查取消），避免死锁。
//  2. 会话已启动完成：由本方法直接收尾（sessionMu 立即可得）。
//  3. 无活跃会话：no-op（保持幂等）。
func (m *WindowManager) CancelSession() {
	session := m.currentSessionSnapshot()
	if session == nil {
		return
	}
	session.cancel()

	// 快路径：等待启动方感知取消并完成收尾
	if session.waitDone(500 * time.Millisecond) {
		return
	}

	// 会话已启动完成或启动方未响应：由这里收尾。
	// sessionMu 可能被启动方短暂持有（截图/GDI 阶段，毫秒级），阻塞有界。
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	if session.isDone() {
		return
	}
	m.endSessionLocked(session, true)
}

// CloseAllWindows 关闭所有截图窗口并结束会话（不显示主窗口，用于应用退出）。
func (m *WindowManager) CloseAllWindows() {
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	if session := m.currentSessionSnapshot(); session != nil {
		session.cancel()
		if !session.isDone() {
			m.endSessionLocked(session, false)
		}
	}
	// 兜底清理无会话的残留窗口
	m.cleanupWindowsLocked()
}

// endSessionLocked 结束指定会话（幂等）。showMain 决定是否恢复主窗口：
// 用户结束（取消/完成）为 true；应用退出为 false。
// 调用方需持有 sessionMu。
func (m *WindowManager) endSessionLocked(session *captureSession, showMain bool) {
	session.finish() // 幂等：仅第一次调用生效

	// 停止鼠标监控并等待 goroutine 退出（释放资源）
	m.stopGlobalMouseMonitor()

	// 广播会话结束（前端清理状态）
	m.broadcastEvent("session-end", "session ended")

	// 关闭所有覆盖窗口
	m.closeOverlayWindowsLocked()

	// 退出 Kiosk 模式
	ExitKioskMode()

	// 清理截图缓存
	m.plugin.ClearDisplayImages()

	// 恢复被本服务隐藏的辅助窗口（保持捕获前状态）
	m.restoreAuxWindowsLocked()

	// 恢复主窗口
	if showMain {
		m.showMainWindow()
	}

	m.mu.Lock()
	m.isCapturing = false
	if m.currentSession == session {
		m.currentSession = nil
	}
	m.mu.Unlock()

	log.Printf("[WindowManager] Session %s ended (showMain=%v)", session.ID(), showMain)
}

// cleanupWindowsLocked 清理残留覆盖层与监控（开始新会话前的预清理）。
// 注意：与 endSessionLocked 不同，这里不广播 session-end、不恢复辅助窗口、
// 不显示主窗口 —— 避免"主窗口先 Show 后 Hide"的闪回。
func (m *WindowManager) cleanupWindowsLocked() {
	// 停止鼠标监控
	m.stopGlobalMouseMonitor()

	// 关闭所有覆盖窗口
	m.closeOverlayWindowsLocked()

	// 退出 Kiosk 模式
	ExitKioskMode()

	m.mu.Lock()
	m.isCapturing = false
	m.mu.Unlock()

	m.plugin.ClearDisplayImages()
}

// closeOverlayWindowsLocked 关闭全部覆盖窗口并重置映射（需要持有 sessionMu）。
func (m *WindowManager) closeOverlayWindowsLocked() {
	m.mu.Lock()
	windows := m.windows
	m.windows = make(map[int]*application.WebviewWindow)
	m.mu.Unlock()

	for i, window := range windows {
		if window != nil {
			window.Close()
			log.Printf("[WindowManager] Closed window %d", i)
		}
	}
}

// restoreAuxWindowsLocked 恢复会话开始时被隐藏的辅助窗口（需要持有 sessionMu）。
// 定向回调优先：由外部服务恢复并同步其内部可见状态（与 hide 路径配对）。
func (m *WindowManager) restoreAuxWindowsLocked() {
	m.mu.Lock()
	hidden := m.auxHidden
	m.auxHidden = nil
	m.mu.Unlock()

	// 回调快照后调用：不在持有 m.mu 的情况下执行外部回调
	m.mu.RLock()
	auxRestore := m.auxRestore
	m.mu.RUnlock()

	for _, w := range hidden {
		if w == nil {
			continue
		}
		if auxRestore != nil && auxRestore(w) {
			log.Printf("[WindowManager] Restored aux window %q via aux hook", w.Name())
			continue
		}
		w.Show()
		log.Printf("[WindowManager] Restored aux window %q", w.Name())
	}
}

// showMainWindow 显示主窗口
func (m *WindowManager) showMainWindow() {
	m.mu.RLock()
	window := m.mainWindow
	m.mu.RUnlock()
	if window != nil {
		window.Show()
	}
}

// GetMainWindow 获取主窗口
func (m *WindowManager) GetMainWindow() *application.WebviewWindow {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mainWindow
}

// IsCapturing 返回是否正在截图
func (m *WindowManager) IsCapturing() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isCapturing
}

// Helper methods
func (m *WindowManager) emitEvent(eventName, data string) {
	if m.app != nil {
		m.app.Event.Emit("screenshot2:"+eventName, data)
	}
}

// startGlobalMouseMonitor 启动全局鼠标监控。
// 屏幕列表以快照传入（goroutine 内不再读取共享状态，消除数据竞争）；
// goroutine 通过 monitorCancel 停止并经 monitorWG 确认退出。
// 仅在活动截图会话期间运行。
func (m *WindowManager) startGlobalMouseMonitor(screens []*application.Screen) {
	m.stopGlobalMouseMonitor() // 确保无残留监控
	if len(screens) == 0 {
		return
	}

	cancel := make(chan struct{})
	m.mu.Lock()
	m.monitorCancel = cancel
	m.mu.Unlock()

	m.monitorWG.Add(1)
	go func(screens []*application.Screen) {
		defer m.monitorWG.Done()
		ticker := time.NewTicker(50 * time.Millisecond) // 每 50ms 检查一次鼠标位置
		defer ticker.Stop()

		currentFocusDisplay := -1 // goroutine 私有状态

		for {
			select {
			case <-cancel:
				log.Printf("[WindowManager] Mouse monitor stopped")
				return
			case <-ticker.C:
				// 确定鼠标在哪个显示器上（平台相关坐标系处理见各平台实现）
				targetDisplay := globalMouseDisplayIndex(screens)

				// 如果鼠标移动到了不同的显示器，聚焦对应的窗口
				if targetDisplay == -1 || targetDisplay == currentFocusDisplay {
					continue
				}
				window := m.GetDisplayWindow(targetDisplay)
				if window != nil {
					window.Focus()
					currentFocusDisplay = targetDisplay
					log.Printf("[WindowManager] Mouse moved to display %d, focusing window", targetDisplay)
				}
			}
		}
	}(screens)

	log.Printf("[WindowManager] Global mouse monitor started (screens=%d)", len(screens))
}

// stopGlobalMouseMonitor 停止全局鼠标监控并等待 goroutine 退出。
func (m *WindowManager) stopGlobalMouseMonitor() {
	m.mu.Lock()
	cancel := m.monitorCancel
	m.monitorCancel = nil
	m.mu.Unlock()

	if cancel != nil {
		close(cancel)
	}
	m.monitorWG.Wait()
}
