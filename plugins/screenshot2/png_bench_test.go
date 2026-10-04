package screenshot2

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// PNG/base64 编码基准：记录会话启动路径上每显示器的编码成本。
// 编码实现本身未在本轮修改（imageToPNG 保持 png.BestSpeed），
// 基准用于证明编码路径无回归，并为后续优化提供基线数据。

// makeTestImage 生成 1920x1080 渐变测试图（压缩行为接近真实截图：
// 大面积平滑渐变 + 高频边缘）
func makeTestImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := uint8((x * 255) / w)
			g := uint8((y * 255) / h)
			b := uint8(((x + y) * 255) / (w + h))
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img
}

func BenchmarkImageToPNG_1080p(b *testing.B) {
	img := makeTestImage(1920, 1080)
	b.SetBytes(int64(1920 * 1080 * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := imageToPNG(img); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkImageToPNG_4K(b *testing.B) {
	img := makeTestImage(3840, 2160)
	b.SetBytes(int64(3840 * 2160 * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := imageToPNG(img); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBase64Encode 测量 PNG → base64 的成本（投递路径的另一半）
func BenchmarkBase64Encode(b *testing.B) {
	img := makeTestImage(1920, 1080)
	pngData, err := imageToPNG(img)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(pngData)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = base64.StdEncoding.EncodeToString(pngData)
	}
}

// TestImageToPNGRoundTrip 确认编码正确性未回归
func TestImageToPNGRoundTrip(t *testing.T) {
	img := makeTestImage(320, 240)
	data, err := imageToPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 320 || decoded.Bounds().Dy() != 240 {
		t.Fatalf("decoded size mismatch: %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}
}

// TestBuildImageDeliveryJSPayloadNoDoubleEncode 确认投递路径直接复用
// base64 字符串（不发生二次编码：payload 中是原始 base64 的 JSON 转义，
// 长度增幅仅为转义引号 + 包装代码）
func TestBuildImageDeliveryJSPayloadNoDoubleEncode(t *testing.T) {
	base64Data := "data:image/png;base64," + strings.Repeat("A", 1000)
	js := buildImageDeliveryJS("sess", base64Data)
	// 若发生二次 base64 编码，长度会近似 ×4/3；这里允许 JSON 转义与
	// 包装函数的固定开销（<300B），不允许数量级差异
	if len(js) > len(base64Data)+300 {
		t.Fatalf("payload unexpectedly larger than source: %d > %d", len(js), len(base64Data)+300)
	}
}
