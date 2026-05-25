package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"log"
	"time"

	"github.com/kbinani/screenshot"
)

const (
	captureFPS    = 18
	captureJpegQl = 70
)

// ScreenCapture 驱动屏幕捕获循环，每帧 JPEG 通过 sendFrame 回调发送
func ScreenCapture(sendFrame func([]byte)) {
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
		sendFrame(frame.Bytes())
	}
}

// compressJPEG 将 RGBA 图像编码为 JPEG 字节
func compressJPEG(img *image.RGBA) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: captureJpegQl}); err != nil {
		return nil, err
	}
	return &buf, nil
}
