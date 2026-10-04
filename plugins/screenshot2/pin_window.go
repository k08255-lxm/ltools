package screenshot2

import (
	"encoding/base64"
	"fmt"
	"log"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// PinWindowManager 管理所有贴图窗口
type PinWindowManager struct {
	windows   map[int]*application.WebviewWindow
	imageData map[int]string // 存储 base64 图片数据
	mu        sync.RWMutex
	nextID    int
}

// Global pin window manager
var pinManager *PinWindowManager
var pinManagerOnce sync.Once

// GetPinWindowManager 获取全局贴图窗口管理器
func GetPinWindowManager() *PinWindowManager {
	pinManagerOnce.Do(func() {
		pinManager = &PinWindowManager{
			windows:   make(map[int]*application.WebviewWindow),
			imageData: make(map[int]string),
			nextID:    1,
		}
	})
	return pinManager
}

// CreatePinWindow 创建贴图窗口
func CreatePinWindow(app *application.App, pngData []byte, x, y, width, height int) error {
	manager := GetPinWindowManager()

	manager.mu.Lock()
	id := manager.nextID
	manager.nextID++
	// 存储图片数据
	base64Data := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)
	manager.imageData[id] = base64Data
	manager.mu.Unlock()

	// 计算初始窗口大小（如果图片太大则缩放）
	maxSize := 400
	displayW := width
	displayH := height
	if width > maxSize || height > maxSize {
		scale := float64(maxSize) / float64(max(width, height))
		displayW = int(float64(width) * scale)
		displayH = int(float64(height) * scale)
	}

	// 窗口大小 = 图片大小 + 边距
	windowW := displayW + 4
	windowH := displayH + 28

	// 最小窗口尺寸
	minSize := 200

	// 创建窗口
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:           fmt.Sprintf("pin-window-%d", id),
		Title:          "贴图",
		Width:          windowW,
		Height:         windowH,
		MinWidth:       minSize,
		MinHeight:      minSize,
		X:              x,
		Y:              y,
		Frameless:      true,
		AlwaysOnTop:    true,
		DisableResize:  false,
		BackgroundType: application.BackgroundTypeTransparent,
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBar{
				Hide: true,
			},
			Backdrop:           application.MacBackdropTransparent,
			WindowLevel:        application.MacWindowLevelFloating,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces,
		},
		URL: fmt.Sprintf("/pin-window?id=%d", id),
	})

	if window == nil {
		// 清理存储的图片数据
		manager.mu.Lock()
		delete(manager.imageData, id)
		manager.mu.Unlock()
		return fmt.Errorf("failed to create pin window")
	}

	window.Show()

	manager.mu.Lock()
	manager.windows[id] = window
	manager.mu.Unlock()

	log.Printf("[PinWindow] Created pin window %d at (%d, %d), size: %dx%d", id, x, y, windowW, windowH)
	return nil
}

// GetPinImageData 获取贴图图片数据
func GetPinImageData(id int) string {
	manager := GetPinWindowManager()
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.imageData[id]
}

// ClosePinWindow 关闭贴图窗口
func ClosePinWindow(id int) {
	manager := GetPinWindowManager()

	manager.mu.Lock()
	window, exists := manager.windows[id]
	if exists {
		delete(manager.windows, id)
		delete(manager.imageData, id) // 清理图片数据
	}
	manager.mu.Unlock()

	if exists && window != nil {
		window.Close()
		log.Printf("[PinWindow] Closed pin window %d", id)
	}
}
