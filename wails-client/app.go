package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-vgo/robotgo"
	"github.com/kbinani/screenshot"
	"golang.org/x/sys/windows"
)

type envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to,omitempty"`
}

var defaultSTUNURLs = []string{
	"stun:stun.l.google.com:19302",
	"stun:turn.h2seo4.win:3478",
}

var defaultTURNURLs = []string{
	"turn:turn.h2seo4.win:3478?transport=udp",
	"turn:turn.h2seo4.win:3478?transport=tcp",
}

type mouseMoveData struct {
	XRatio float64 `json:"xRatio"`
	YRatio float64 `json:"yRatio"`
}

type mouseClickData struct {
	Button string `json:"button"`
	Action string `json:"action"`
}

type keyPressData struct {
	Key       string   `json:"key"`
	Modifiers []string `json:"modifiers"`
}

type scrollData struct {
	DeltaY float64 `json:"deltaY"`
}

type frontendICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type App struct {
	ctx         context.Context
	mu          sync.Mutex
	sessionID   string
	peerReady   bool
	insecureTLS bool

	screenW  int
	screenH  int
	logicalW int
	logicalH int
	dpiScale float64
}

func NewApp() *App {
	return &App{insecureTLS: true}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	setupClientLog()
	enableDPIAwareness()
	a.refreshScreenInfo()
	log.Printf("[App] screen physical=%dx%d logical=%dx%d dpiScale=%.2f",
		a.screenW, a.screenH, a.logicalW, a.logicalH, a.dpiScale)
}

func (a *App) shutdown(ctx context.Context) {
	log.Printf("[App] shutdown requested")
	_ = a.Disconnect()
}

func setupClientLog() {
	execPath, err := os.Executable()
	if err != nil {
		return
	}
	logPath := filepath.Join(filepath.Dir(execPath), "client.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("[App] log file: %s", logPath)
}

func (a *App) refreshScreenInfo() {
	a.screenW, a.screenH = robotgo.GetScreenSize()
	if a.screenW <= 0 || a.screenH <= 0 {
		bounds := screenshot.GetDisplayBounds(0)
		a.screenW = bounds.Dx()
		a.screenH = bounds.Dy()
		log.Printf("[App] robotgo screen size unavailable, fallback to screenshot bounds")
	}
	a.dpiScale = getDPIScale()
	if a.dpiScale <= 0 {
		a.dpiScale = 1.0
	}
	a.logicalW = int(float64(a.screenW) / a.dpiScale)
	a.logicalH = int(float64(a.screenH) / a.dpiScale)
}

func getDPIScale() float64 {
	dll := windows.NewLazySystemDLL("user32.dll")
	procDPI := dll.NewProc("GetDpiForWindow")
	procDesktop := dll.NewProc("GetDesktopWindow")

	hwnd, _, _ := procDesktop.Call()
	if hwnd == 0 {
		return 1.0
	}
	dpi, _, _ := procDPI.Call(hwnd)
	if dpi == 0 {
		return 1.0
	}
	return float64(dpi) / 96.0
}

func enableDPIAwareness() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	if proc := user32.NewProc("SetProcessDpiAwarenessContext"); proc.Find() == nil {
		proc.Call(^uintptr(3))
		return
	}
	if proc := user32.NewProc("SetProcessDPIAware"); proc.Find() == nil {
		proc.Call()
	}
}

func setCursorPos(x, y int) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("SetCursorPos")
	proc.Call(uintptr(x), uintptr(y))
}

func splitEnvList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func buildFrontendICEServers() []frontendICEServer {
	servers := []frontendICEServer{
		{URLs: defaultSTUNURLs},
	}

	username := strings.TrimSpace(os.Getenv("TURN_USERNAME"))
	if username == "" {
		username = "remoteuser"
	}
	credential := strings.TrimSpace(os.Getenv("TURN_PASSWORD"))
	if credential == "" {
		log.Printf("[ICE] frontend TURN disabled: TURN_PASSWORD is not set")
		return servers
	}

	urls := splitEnvList(os.Getenv("TURN_URLS"))
	if len(urls) == 0 {
		urls = defaultTURNURLs
	}

	servers = append(servers, frontendICEServer{
		URLs:       urls,
		Username:   username,
		Credential: credential,
	})
	log.Printf("[ICE] frontend TURN enabled: urls=%v username=%s", urls, username)
	return servers
}

func (a *App) GetSessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID
}

func (a *App) GetPeerConnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.peerReady
}

func (a *App) GetFrontendICEServers() []frontendICEServer {
	return buildFrontendICEServers()
}

func (a *App) SetInsecureTLS(insecure bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.insecureTLS = insecure
}

func (a *App) GetInsecureTLS() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.insecureTLS
}

func (a *App) Disconnect() error {
	a.mu.Lock()
	a.peerReady = false
	a.mu.Unlock()
	log.Printf("[App] disconnected")
	return nil
}

func (a *App) ExecuteCommand(cmdJSON string) error {
	log.Printf("[Command] execute: %s", cmdJSON)
	a.handleCommand(cmdJSON)
	return nil
}

func (a *App) handleCommand(raw string) {
	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		log.Printf("[Command] unmarshal error: %v", err)
		return
	}
	switch env.Type {
	case "MOUSE_MOVE":
		var d mouseMoveData
		if err := json.Unmarshal(env.Payload, &d); err == nil {
			a.execMouseMove(d)
		}
	case "MOUSE_CLICK":
		var d mouseClickData
		if err := json.Unmarshal(env.Payload, &d); err == nil {
			a.execMouseClick(d)
		}
	case "KEY_PRESS":
		var d keyPressData
		if err := json.Unmarshal(env.Payload, &d); err == nil {
			a.execKeyPress(d)
		}
	case "SCROLL":
		var d scrollData
		if err := json.Unmarshal(env.Payload, &d); err == nil {
			a.execScroll(d)
		}
	}
}

func (a *App) execMouseMove(d mouseMoveData) {
	bounds := screenshot.GetDisplayBounds(0)
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= 0 || h <= 0 {
		w, h = robotgo.GetScreenSize()
		bounds.Min.X = 0
		bounds.Min.Y = 0
		bounds.Max.X = w
		bounds.Max.Y = h
	}
	if w <= 0 || h <= 0 {
		return
	}

	x := bounds.Min.X + int(d.XRatio*float64(w))
	y := bounds.Min.Y + int(d.YRatio*float64(h))

	if x < bounds.Min.X {
		x = bounds.Min.X
	} else if x >= bounds.Max.X {
		x = bounds.Max.X - 1
	}
	if y < bounds.Min.Y {
		y = bounds.Min.Y
	} else if y >= bounds.Max.Y {
		y = bounds.Max.Y - 1
	}

	setCursorPos(x, y)
}

func (a *App) execMouseClick(d mouseClickData) {
	button := d.Button
	if button == "middle" {
		button = "center"
	}

	switch d.Action {
	case "down":
		_ = robotgo.Toggle(button)
	case "up":
		_ = robotgo.Toggle(button, "up")
	default:
		robotgo.MouseClick(button)
	}
}

func (a *App) execKeyPress(d keyPressData) {
	robotgo.KeyTap(d.Key)
}

func (a *App) execScroll(d scrollData) {
	clicks := int(d.DeltaY / 100)
	if clicks == 0 {
		if d.DeltaY > 0 {
			clicks = 1
		} else {
			clicks = -1
		}
	}
	if clicks > 0 {
		for i := 0; i < clicks; i++ {
			robotgo.ScrollDir(1, "down")
		}
	} else {
		for i := 0; i < -clicks; i++ {
			robotgo.ScrollDir(1, "up")
		}
	}
}
