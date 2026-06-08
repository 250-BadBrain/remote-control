package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	execPath, err := os.Executable()
	if err != nil {
		log.Fatalf("[FATAL] get executable path failed: %v", err)
	}
	logDir := filepath.Dir(execPath)
	logPath := filepath.Join(logDir, "server.log")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("[FATAL] open log file failed: %v", err)
	}
	defer logFile.Close()

	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	hub := NewHub()
	go hub.Run()

	http.HandleFunc("/connect/computer", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, w, r, RoleComputer)
	})
	http.HandleFunc("/connect/phone", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, w, r, RolePhone)
	})

	staticDir := findStaticDir()
	if staticDir != "" {
		fs := http.FileServer(http.Dir(staticDir))
		http.Handle("/", fs)
	} else {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("Remote Control Signaling Server\n\nWebSocket endpoints:\n  /connect/computer\n  /connect/phone\n"))
		})
	}

	addr := ":8443"
	log.Fatalf("[FATAL] server stopped: %v", http.ListenAndServeTLS(addr, "server.crt", "server.key", nil))
}

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
