package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"log"
	"time"

	"github.com/kbinani/screenshot"
	"golang.org/x/image/draw"
)

const (
	captureFPS      = 10
	captureJpegQl   = 55
	captureMaxWidth = 1280
)

// ScreenCapture 驱动屏幕捕获循环，每帧 JPEG 通过 sendFrame 回调发送。
// sendFrame 返回 false 时停止捕获，避免 DataChannel 关闭后泄漏后台 goroutine。
func ScreenCapture(sendFrame func([]byte) bool) {
	n := screenshot.NumActiveDisplays()
	if n == 0 {
		log.Fatal("[Capture] 未发现活动显示器")
	}
	log.Printf("[Capture] %d 个活动显示器", n)

	bounds := screenshot.GetDisplayBounds(0)
	log.Printf("[Capture] 主显示器 %dx%d", bounds.Dx(), bounds.Dy())

	frameInterval := time.Second / captureFPS
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	for range ticker.C {
		img, err := screenshot.CaptureRect(bounds)
		if err != nil {
			log.Printf("[Capture] 捕获错误: %v", err)
			continue
		}
		frame, err := compressJPEG(img)
		if err != nil {
			log.Printf("[Capture] JPEG 编码错误: %v", err)
			continue
		}
		if !sendFrame(frame.Bytes()) {
			log.Printf("[Capture] 停止屏幕捕获")
			return
		}
	}
}

// compressJPEG 将 RGBA 图像编码为 JPEG 字节
func compressJPEG(img *image.RGBA) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	out := resizeForTransport(img)
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: captureJpegQl}); err != nil {
		return nil, err
	}
	return &buf, nil
}

func resizeForTransport(img image.Image) image.Image {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= captureMaxWidth {
		return img
	}

	newW := captureMaxWidth
	newH := h * newW / w
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	return dst
}
