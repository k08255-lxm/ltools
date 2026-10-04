package screenshot2

import (
	"log"
	"path/filepath"
	"sync"

	"ltools/internal/plugins"

	"github.com/wailsapp/wails/v3/pkg/application"
	"ltools/internal/plugins/clipboard"
)

const (
	PluginID      = "screenshot2.builtin"
	PluginName    = "截图"
	PluginVersion = "1.0.0"
)

// Screenshot2Plugin 微信风格截图插件
// 核心特性：
// - 多窗口架构：每个显示器一个独立窗口
// - 全屏遮罩：0.4 透明度
// - 窗口识别：悬停自动识别，蓝色边框高亮
// - 选区交互：拖拽创建、8 手柄调整、移动
// - 标注工具：矩形、椭圆、箭头、画笔、文字、马赛克、模糊
type Screenshot2Plugin struct {
	*plugins.BasePlugin
	app       *application.App
	storage   *Storage
	clipboard *clipboard.ImageClipboard
	tempDir   string

	// 多显示器截图数据（每个显示器单独存储）
	displayImagesMu sync.RWMutex
	displayImages   map[int][]byte // displayIndex -> PNG data
}

// NewScreenshot2Plugin creates a new screenshot2 plugin instance
func NewScreenshot2Plugin() *Screenshot2Plugin {
	metadata := &plugins.PluginMetadata{
		ID:          PluginID,
		Name:        PluginName,
		Version:     PluginVersion,
		Author:      "LTools Team",
		Description: "微信风格截图工具，支持多显示器、窗口识别、选区调整和标注",
		Icon:        "screenshot",
		Type:        plugins.PluginTypeBuiltIn,
		State:       plugins.PluginStateInstalled,
		Permissions: []plugins.Permission{
			plugins.PermissionClipboard,
			plugins.PermissionFileSystem,
		},
		Keywords: []string{"截图", "屏幕", "捕获", "screenshot", "screen", "capture"},
	}

	base := plugins.NewBasePlugin(metadata)
	return &Screenshot2Plugin{
		BasePlugin:    base,
		displayImages: make(map[int][]byte),
	}
}

// Init initializes the plugin
func (p *Screenshot2Plugin) Init(app *application.App) error {
	if err := p.BasePlugin.Init(app); err != nil {
		return err
	}
	p.app = app

	// Get user data directory for temp files
	userDataDir, err := GetDefaultSavePath()
	if err != nil {
		log.Printf("[Screenshot2] Failed to get default save path: %v", err)
		p.tempDir = filepath.Join(userDataDir, "ltools", "screenshots")
	} else {
		p.tempDir = userDataDir
	}

	// Initialize storage
	p.storage = NewStorage(p.tempDir)

	// Initialize clipboard using shared module
	p.clipboard = clipboard.NewImageClipboard(app)

	log.Printf("[Screenshot2] Plugin initialized with temp dir: %s", p.tempDir)
	return nil
}

// ServiceStartup is called when the application starts
func (p *Screenshot2Plugin) ServiceStartup(app *application.App) error {
	log.Printf("[Screenshot2] Service startup")
	return p.BasePlugin.ServiceStartup(app)
}

// ServiceShutdown is called when the application shuts down
func (p *Screenshot2Plugin) ServiceShutdown(app *application.App) error {
	log.Printf("[Screenshot2] Service shutdown")
	return p.BasePlugin.ServiceShutdown(app)
}

// Enabled returns true if the plugin is enabled
func (p *Screenshot2Plugin) Enabled() bool {
	return p.BasePlugin.Enabled()
}

// SetEnabled enables or disables the plugin
func (p *Screenshot2Plugin) SetEnabled(enabled bool) error {
	return p.BasePlugin.SetEnabled(enabled)
}

// Helper method to emit events
func (p *Screenshot2Plugin) emitEvent(eventName, data string) {
	if p.app != nil {
		p.app.Event.Emit("screenshot2:"+eventName, data)
	}
}

// GetStorage returns the storage instance
func (p *Screenshot2Plugin) GetStorage() *Storage {
	return p.storage
}

// GetClipboard returns the clipboard instance
func (p *Screenshot2Plugin) GetClipboard() *clipboard.ImageClipboard {
	return p.clipboard
}
