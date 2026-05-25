package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	hub := NewHub()
	go hub.Run()

	// ---------- WebSocket 端点 ----------
	http.HandleFunc("/connect/computer", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, w, r, RoleComputer)
	})
	http.HandleFunc("/connect/phone", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, w, r, RolePhone)
	})

	// ---------- HTTP API / 静态文件 ----------
	// 提供前端构建产物（手机控制页 / Viewer 页等）
	staticDir := findStaticDir()
	if staticDir != "" {
		log.Printf("[HTTP] 从 %s 提供静态文件", staticDir)
		fs := http.FileServer(http.Dir(staticDir))
		http.Handle("/", fs)
	} else {
		log.Println("[HTTP] 未找到前端静态目录，仅提供 WebSocket 服务")
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("Remote Control Signaling Server\n\nWebSocket 端点:\n  /connect/computer\n  /connect/phone\n"))
		})
	}

	addr := ":8080"
	log.Printf("[Server] 信令服务器启动于 %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// findStaticDir 按优先级查找前端静态目录
func findStaticDir() string {
	candidates := []string{
		// 相对于 signaling-server 可执行文件
		filepath.Join("frontend", "dist"),
		// 相对于工作目录
		filepath.Join("..", "wails-client", "frontend", "dist"),
		// 同目录
		"dist",
	}
	for _, dir := range candidates {
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs
		}
	}
	return ""
}
