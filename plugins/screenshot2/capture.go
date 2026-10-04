package screenshot2

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/kbinani/screenshot"
)

// DisplayInfo contains information about a display
type DisplayInfo struct {
	Index   int    `json:"index"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Primary bool   `json:"primary"`
	Name    string `json:"name"`
	// Retina 缩放因子
	ScaleFactor float64 `json:"scaleFactor"`
}

// CaptureResult 单个显示器的截图结果
type CaptureResult struct {
	DisplayIndex int         `json:"displayIndex"`
	Base64Data   string      `json:"base64Data"`
	Width        int         `json:"width"`
	Height       int         `json:"height"`
	Image        *image.RGBA `json:"-"` // 内部使用，不序列化
}

// CaptureAllDisplaysSeparately 为每个显示器单独截图（并行模式）
// 返回每个显示器的截图数据，支持微信风格的多窗口显示
func (p *Screenshot2Plugin) CaptureAllDisplaysSeparately() (map[int]*CaptureResult, error) {
	numDisplays := screenshot.NumActiveDisplays()
	if numDisplays == 0 {
		return nil, &NoDisplayError{}
	}

	log.Printf("[Screenshot2] 开始并行捕获 %d 个显示器", numDisplays)

	results := make(map[int]*CaptureResult)
	var resultsMu sync.Mutex
	var wg sync.WaitGroup

	// 并行捕获所有显示器
	for i := 0; i < numDisplays; i++ {
		wg.Add(1)
		go func(displayIndex int) {
			defer wg.Done()

			log.Printf("[Screenshot2] 正在捕获显示器 %d...", displayIndex)

			img, err := p.captureDisplay(displayIndex)
			if err != nil {
				log.Printf("[Screenshot2] 捕获显示器 %d 失败: %v", displayIndex, err)
				return
			}

			// 转换为 PNG
			pngData, err := imageToPNG(img)
			if err != nil {
				log.Printf("[Screenshot2] 转换显示器 %d PNG 失败: %v", displayIndex, err)
				return
			}

			// 转换为 base64
			base64Data := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)

			result := &CaptureResult{
				DisplayIndex: displayIndex,
				Base64Data:   base64Data,
				Width:        img.Bounds().Dx(),
				Height:       img.Bounds().Dy(),
				Image:        img,
			}

			// 线程安全地存储结果
			resultsMu.Lock()
			results[displayIndex] = result
			p.displayImagesMu.Lock()
			p.displayImages[displayIndex] = pngData
			p.displayImagesMu.Unlock()
			resultsMu.Unlock()

			log.Printf("[Screenshot2] 显示器 %d 捕获成功: %dx%d", displayIndex, img.Bounds().Dx(), img.Bounds().Dy())
		}(i)
	}

	// 等待所有捕获完成
	wg.Wait()

	if len(results) == 0 {
		return nil, fmt.Errorf("failed to capture any display")
	}

	log.Printf("[Screenshot2] 并行捕获完成，成功捕获 %d 个显示器", len(results))
	return results, nil
}

// captureDisplay 捕获单个显示器
func (p *Screenshot2Plugin) captureDisplay(displayIndex int) (*image.RGBA, error) {
	numDisplays := screenshot.NumActiveDisplays()
	if displayIndex < 0 || displayIndex >= numDisplays {
		return nil, &DisplayNotFoundError{Index: displayIndex, Total: numDisplays}
	}

	bounds := screenshot.GetDisplayBounds(displayIndex)

	log.Printf("[Screenshot2] 捕获显示器 %d (OS: %s)", displayIndex, runtime.GOOS)
	log.Printf("[Screenshot2] 边界: %v", bounds)

	var img *image.RGBA
	var err error

	switch runtime.GOOS {
	case "darwin":
		// macOS: 使用 screencapture 命令获得物理像素分辨率
		img, err = p.captureWithScreencapture(displayIndex, bounds)
		if err != nil {
			log.Printf("[Screenshot2] screencapture 失败，回退到 screenshot 库: %v", err)
			img, err = screenshot.CaptureRect(bounds)
		}
	default:
		img, err = screenshot.CaptureRect(bounds)
	}

	if err != nil {
		return nil, err
	}

	return img, nil
}

// captureWithScreencapture 使用 macOS 原生命令捕获屏幕
func (p *Screenshot2Plugin) captureWithScreencapture(displayIndex int, bounds image.Rectangle) (*image.RGBA, error) {
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("screenshot2_%d_%d.png", displayIndex, time.Now().UnixNano()))
	defer os.Remove(tmpFile)

	// -D 指定显示器索引（1-based），-x 不播放声音，-t png 格式
	cmd := exec.Command("screencapture", "-D", fmt.Sprintf("%d", displayIndex+1), "-x", "-t", "png", tmpFile)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("screencapture failed: %w, output: %s", err, string(output))
	}

	file, err := os.Open(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open screenshot file: %w", err)
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("failed to decode PNG: %w", err)
	}

	var rgba *image.RGBA
	if rgbaImg, ok := img.(*image.RGBA); ok {
		rgba = rgbaImg
	} else {
		rgba = image.NewRGBA(img.Bounds())
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	}

	return rgba, nil
}

// GetDisplays returns information about all active displays
func (p *Screenshot2Plugin) GetDisplays() []DisplayInfo {
	numDisplays := screenshot.NumActiveDisplays()
	log.Printf("[Screenshot2] GetDisplays: found %d displays", numDisplays)
	displays := make([]DisplayInfo, numDisplays)

	for i := 0; i < numDisplays; i++ {
		bounds := screenshot.GetDisplayBounds(i)
		log.Printf("[Screenshot2] Display %d bounds: Min(%d,%d) Max(%d,%d) => size: %dx%d",
			i, bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y, bounds.Dx(), bounds.Dy())

		// 计算 Retina 缩放因子
		scaleFactor := 1.0
		if runtime.GOOS == "darwin" {
			// 在 macOS 上，尝试检测 Retina 屏幕
			// 如果物理像素尺寸是逻辑尺寸的 2 倍，则是 Retina
			scaleFactor = 2.0 // 简化处理，假设都是 Retina
		}

		displays[i] = DisplayInfo{
			Index:       i,
			Width:       bounds.Dx(),
			Height:      bounds.Dy(),
			X:           bounds.Min.X,
			Y:           bounds.Min.Y,
			Primary:     i == 0,
			Name:        getDisplayName(i),
			ScaleFactor: scaleFactor,
		}
	}

	return displays
}

// GetVirtualDesktopBounds 获取虚拟桌面的边界
func (p *Screenshot2Plugin) GetVirtualDesktopBounds() (x, y, width, height int) {
	displays := p.GetDisplays()
	if len(displays) == 0 {
		return 0, 0, 1920, 1080
	}

	minX, minY := displays[0].X, displays[0].Y
	maxX, maxY := displays[0].X+displays[0].Width, displays[0].Y+displays[0].Height

	for _, d := range displays[1:] {
		if d.X < minX {
			minX = d.X
		}
		if d.Y < minY {
			minY = d.Y
		}
		if d.X+d.Width > maxX {
			maxX = d.X + d.Width
		}
		if d.Y+d.Height > maxY {
			maxY = d.Y + d.Height
		}
	}

	return minX, minY, maxX - minX, maxY - minY
}

// GetDisplayImage 获取指定显示器的截图数据
func (p *Screenshot2Plugin) GetDisplayImage(displayIndex int) []byte {
	p.displayImagesMu.RLock()
	defer p.displayImagesMu.RUnlock()
	return p.displayImages[displayIndex]
}

// ClearDisplayImages 清除所有显示器截图数据
func (p *Screenshot2Plugin) ClearDisplayImages() {
	p.displayImagesMu.Lock()
	defer p.displayImagesMu.Unlock()
	p.displayImages = make(map[int][]byte)
}

// imageToPNG converts an image to PNG byte data
func imageToPNG(img *image.RGBA) ([]byte, error) {
	if img == nil {
		return nil, &InvalidImageError{}
	}

	var buf bytes.Buffer
	encoder := &png.Encoder{
		CompressionLevel: png.BestSpeed,
	}

	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// getDisplayName returns a human-readable name for a display
func getDisplayName(index int) string {
	switch index {
	case 0:
		return "主显示器"
	case 1:
		return "副显示器 1"
	case 2:
		return "副显示器 2"
	default:
		return fmt.Sprintf("显示器 %d", index+1)
	}
}

// MergeAllScreens 合并所有屏幕截图到一张大图
// 返回合并后的图片和虚拟桌面的边界信息
func (p *Screenshot2Plugin) MergeAllScreens(captureResults map[int]*CaptureResult) (*CaptureResult, int, int, error) {
	displays := p.GetDisplays()
	if len(displays) == 0 || len(captureResults) == 0 {
		return nil, 0, 0, fmt.Errorf("no displays or captures available")
	}

	// 计算虚拟桌面边界（使用物理像素）
	minX, minY := 0, 0
	maxX, maxY := 0, 0

	for _, d := range displays {
		// 物理像素坐标
		physX := int(float64(d.X) * d.ScaleFactor)
		physY := int(float64(d.Y) * d.ScaleFactor)
		physW := int(float64(d.Width) * d.ScaleFactor)
		physH := int(float64(d.Height) * d.ScaleFactor)

		if physX < minX {
			minX = physX
		}
		if physY < minY {
			minY = physY
		}
		if physX+physW > maxX {
			maxX = physX + physW
		}
		if physY+physH > maxY {
			maxY = physY + physH
		}
	}

	virtualWidth := maxX - minX
	virtualHeight := maxY - minY

	log.Printf("[Screenshot2] Virtual desktop: offset=(%d,%d), size=%dx%d", minX, minY, virtualWidth, virtualHeight)

	// 创建合并后的画布
	mergedImg := image.NewRGBA(image.Rect(0, 0, virtualWidth, virtualHeight))

	// 将每个屏幕的截图绘制到合并画布上
	for i, d := range displays {
		capture := captureResults[i]
		if capture == nil || capture.Image == nil {
			continue
		}

		// 计算该屏幕在虚拟桌面上的位置（物理像素）
		physX := int(float64(d.X) * d.ScaleFactor)
		physY := int(float64(d.Y) * d.ScaleFactor)

		// 转换为相对于虚拟桌面原点的坐标
		destX := physX - minX
		destY := physY - minY

		log.Printf("[Screenshot2] Merging display %d at virtual position (%d, %d)", i, destX, destY)

		// 绘制该屏幕的截图
		draw.Draw(mergedImg, image.Rect(destX, destY, destX+capture.Image.Bounds().Dx(), destY+capture.Image.Bounds().Dy()),
			capture.Image, image.Point{}, draw.Over)
	}

	// 转换为 base64
	mergedData, err := imageToPNG(mergedImg)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to encode merged image: %w", err)
	}

	base64Data := "data:image/png;base64," + base64.StdEncoding.EncodeToString(mergedData)

	result := &CaptureResult{
		Image:      mergedImg,
		Base64Data: base64Data,
		Width:      virtualWidth,
		Height:     virtualHeight,
	}

	return result, minX, minY, nil
}

// Custom error types

// DisplayNotFoundError is returned when the requested display is not found
type DisplayNotFoundError struct {
	Index int
	Total int
}

func (e *DisplayNotFoundError) Error() string {
	return fmt.Sprintf("display not found: index %d out of range (total: %d)", e.Index, e.Total)
}

// NoDisplayError is returned when no displays are available
type NoDisplayError struct{}

func (e *NoDisplayError) Error() string {
	return "no active displays found"
}

// InvalidImageError is returned when the image is invalid
type InvalidImageError struct{}

func (e *InvalidImageError) Error() string {
	return "invalid image data"
}
