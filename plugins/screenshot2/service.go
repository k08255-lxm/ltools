package screenshot2

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Screenshot2Service 暴露截图功能给前端
type Screenshot2Service struct {
	app           *application.App
	plugin        *Screenshot2Plugin
	windowManager *WindowManager
}

// NewScreenshot2Service creates a new screenshot2 service
func NewScreenshot2Service(plugin *Screenshot2Plugin, app *application.App) *Screenshot2Service {
	return &Screenshot2Service{
		app:    app,
		plugin: plugin,
	}
}

// SetWindowManager sets the window manager reference
func (s *Screenshot2Service) SetWindowManager(wm *WindowManager) {
	s.windowManager = wm
}

// ServiceStartup is called when the application starts
func (s *Screenshot2Service) ServiceStartup(app *application.App) error {
	log.Printf("[Screenshot2Service] Service startup")
	return nil
}

// ServiceShutdown is called when the application shuts down
func (s *Screenshot2Service) ServiceShutdown(app *application.App) error {
	log.Printf("[Screenshot2Service] Service shutdown")
	return nil
}

// StartCapture 开始截图流程
// 返回显示器信息和图片数据，由前端决定如何显示
func (s *Screenshot2Service) StartCapture() (string, error) {
	log.Printf("[Screenshot2Service] Starting capture...")

	if s.windowManager == nil {
		log.Printf("[Screenshot2Service] ERROR: window manager not initialized!")
		return "", fmt.Errorf("window manager not initialized")
	}

	// 通过窗口管理器启动截图
	result, err := s.windowManager.StartCapture()
	if err != nil {
		s.emitError(fmt.Sprintf("Capture failed: %v", err))
		return "", err
	}

	return result, nil
}

// GetDisplays 获取所有显示器信息
func (s *Screenshot2Service) GetDisplays() []DisplayInfo {
	return s.plugin.GetDisplays()
}

// GetDisplaysJSON 获取显示器信息的 JSON 字符串
func (s *Screenshot2Service) GetDisplaysJSON() string {
	displays := s.plugin.GetDisplays()
	data, err := json.Marshal(displays)
	if err != nil {
		log.Printf("[Screenshot2Service] Failed to marshal displays: %v", err)
		return "[]"
	}
	return string(data)
}

// GetVirtualDesktopBounds 获取虚拟桌面边界
func (s *Screenshot2Service) GetVirtualDesktopBounds() map[string]int {
	x, y, w, h := s.plugin.GetVirtualDesktopBounds()
	return map[string]int{
		"x":      x,
		"y":      y,
		"width":  w,
		"height": h,
	}
}

// CopyToClipboard 复制指定区域的图片到剪贴板
// imgData: base64 编码的图片数据
func (s *Screenshot2Service) CopyToClipboard(imgData string) error {
	if imgData == "" {
		return fmt.Errorf("no image data to copy")
	}

	pngData, err := s.decodeBase64Image(imgData)
	if err != nil {
		return err
	}

	if err := s.plugin.GetClipboard().SetImage(pngData); err != nil {
		s.emitError(fmt.Sprintf("copy to clipboard failed: %v", err))
		return err
	}

	s.emitEvent("copied", "image copied to clipboard")
	log.Printf("[Screenshot2Service] Image copied to clipboard")
	return nil
}

// SaveImage 保存图片到文件
func (s *Screenshot2Service) SaveImage(imgData string, filename string) (string, error) {
	if imgData == "" {
		return "", fmt.Errorf("no image data to save")
	}

	pngData, err := s.decodeBase64Image(imgData)
	if err != nil {
		return "", err
	}

	savedPath, err := s.plugin.GetStorage().SaveToFile(pngData, filename)
	if err != nil {
		s.emitError(fmt.Sprintf("Save failed: %v", err))
		return "", err
	}

	s.emitEvent("saved", savedPath)
	log.Printf("[Screenshot2Service] Image saved to: %s", savedPath)
	return savedPath, nil
}

// SaveImageWithDialog 显示保存对话框并保存图片
func (s *Screenshot2Service) SaveImageWithDialog(imgData string) (string, error) {
	if imgData == "" {
		return "", fmt.Errorf("no image data to save")
	}

	pngData, err := s.decodeBase64Image(imgData)
	if err != nil {
		return "", err
	}

	// 获取编辑器窗口（如果有）
	var parentWindow *application.WebviewWindow
	if s.windowManager != nil {
		parentWindow = s.windowManager.GetMainWindow()
	}

	options := SaveDialogOptions{
		DefaultFilename: "",
		AllowedTypes:    []string{"png"},
		Title:           "保存截图",
		ParentWindow:    parentWindow,
	}

	savedPath, err := s.plugin.GetStorage().SaveFileWithDialog(pngData, s.app, options)
	if err != nil {
		s.emitError(fmt.Sprintf("Save failed: %v", err))
		return "", err
	}

	s.emitEvent("saved", savedPath)
	log.Printf("[Screenshot2Service] Image saved to: %s", savedPath)
	return savedPath, nil
}

// CancelCapture 取消截图
func (s *Screenshot2Service) CancelCapture() {
	if s.windowManager != nil {
		// CancelSession 区分"启动中"与"已启动"两种时序：
		// 启动中仅发取消信号由启动方收尾（避免与 StartCapture 互相阻塞），
		// 已启动则直接收尾。收尾会恢复主窗口与被隐藏的辅助窗口。
		s.windowManager.CancelSession()
	}
	s.plugin.ClearDisplayImages()
	s.emitEvent("cancelled", "capture cancelled")
	log.Printf("[Screenshot2Service] Capture cancelled")
}

// FrontendReady 前端调用此方法通知已加载完成。
// sessionId 用于会话隔离：迟到/过期会话的 ready 会被丢弃，
// 同一显示器的重复 ready 只计一次。
func (s *Screenshot2Service) FrontendReady(sessionId string, displayIndex int) {
	log.Printf("[Screenshot2Service] FrontendReady called for display %d (session %q)", displayIndex, sessionId)
	if s.windowManager != nil {
		s.windowManager.OnFrontendReady(sessionId, displayIndex)
	} else {
		log.Printf("[Screenshot2Service] WARNING: windowManager is nil!")
	}
}

// FocusDisplayWindow 聚焦指定显示器的窗口
// 当鼠标移动到某个窗口时调用，确保该窗口可以接收鼠标事件
func (s *Screenshot2Service) FocusDisplayWindow(displayIndex int) {
	if s.windowManager != nil {
		window := s.windowManager.GetDisplayWindow(displayIndex)
		if window != nil {
			window.Focus()
			log.Printf("[Screenshot2Service] Focused window for display %d", displayIndex)
		}
	}
}

// GetWindowAtPoint 获取鼠标位置下的窗口信息
func (s *Screenshot2Service) GetWindowAtPoint(x, y int) (*WindowInfo, error) {
	return GetWindowAtPoint(x, y, true)
}

// PinImage 创建贴图窗口
// imgData: base64 编码的图片数据
// x, y: 窗口初始位置
// width, height: 窗口初始大小
func (s *Screenshot2Service) PinImage(imgData string, x, y, width, height int) error {
	log.Printf("[Screenshot2Service] PinImage called at (%d, %d), size: %dx%d", x, y, width, height)

	pngData, err := s.decodeBase64Image(imgData)
	if err != nil {
		return err
	}

	// 创建贴图窗口
	return CreatePinWindow(s.app, pngData, x, y, width, height)
}

// ClosePinWindow 关闭贴图窗口
func (s *Screenshot2Service) ClosePinWindow(id int) {
	log.Printf("[Screenshot2Service] ClosePinWindow called for id %d", id)
	ClosePinWindow(id)
}

// GetPinImageData 获取贴图图片数据
func (s *Screenshot2Service) GetPinImageData(id int) string {
	return GetPinImageData(id)
}

// decodeBase64Image 解码 base64 图片数据
func (s *Screenshot2Service) decodeBase64Image(imgData string) ([]byte, error) {
	var base64Str string
	if strings.HasPrefix(imgData, "data:image/png;base64,") {
		base64Str = strings.TrimPrefix(imgData, "data:image/png;base64,")
	} else if strings.HasPrefix(imgData, "data:image/jpeg;base64,") {
		base64Str = strings.TrimPrefix(imgData, "data:image/jpeg;base64,")
	} else {
		base64Str = imgData
	}

	return decodeBase64(base64Str)
}

// Helper methods
func (s *Screenshot2Service) emitEvent(eventName, data string) {
	if s.app != nil {
		s.app.Event.Emit("screenshot2:"+eventName, data)
	}
}

func (s *Screenshot2Service) emitError(message string) {
	s.emitEvent("error", message)
}
