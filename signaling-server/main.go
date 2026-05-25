package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	// ---------- 日志初始化 ----------
	execPath, err := os.Executable()
	if err != nil {
		log.Fatalf("获取可执行文件路径失败: %v", err)
	}
	logDir := filepath.Dir(execPath)
	logPath := filepath.Join(logDir, "server.log")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("创建日志文件失败: %v", err)
	}
	defer logFile.Close()

	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	log.Printf("[Server] 日志文件: %s", logPath)

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
	log.Printf("[Server] 信令服务器启动于 %s (HTTPS)", addr)
	log.Fatalf("服务器启动失败: %v", http.ListenAndServeTLS(addr, "server.crt", "server.key", nil))
}

// findStaticDir 按优先级查找前端静态目录
func findStaticDir() string {
	candidates := []string{
		filepath.Join("frontend", "dist"),
		filepath.Join("..", "wails-client", "frontend", "dist"),
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
