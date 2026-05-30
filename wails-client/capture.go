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
	captureFPS      = 5
	captureJpegQl   = 76
	captureMaxWidth = 1920
)

// ScreenCapture 驱动屏幕捕获循环，每帧 JPEG 通过 sendFrame 回调发送。
// sendFrame 返回 false 时停止捕获，避免 DataChannel 关闭后泄漏后台 goroutine。
func ScreenCapture(sendFrame func([]byte) bool) {
	n := screenshot.NumActiveDisplays()
	if n == 0 {
		log.Fatal("[Capture] no active display found")
	}
	log.Printf("[Capture] active displays: %d", n)

	bounds := screenshot.GetDisplayBounds(0)
	log.Printf("[Capture] primary display: %dx%d", bounds.Dx(), bounds.Dy())

	frameInterval := time.Second / captureFPS
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	for range ticker.C {
		img, err := screenshot.CaptureRect(bounds)
		if err != nil {
			log.Printf("[Capture] capture error: %v", err)
			continue
		}
		frame, err := compressJPEG(img)
		if err != nil {
			log.Printf("[Capture] jpeg encode error: %v", err)
			continue
		}
		if !sendFrame(frame.Bytes()) {
			log.Printf("[Capture] stop screen capture")
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
