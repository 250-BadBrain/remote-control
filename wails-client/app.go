package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/go-vgo/robotgo"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// ---------- protocol types ----------
type envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to,omitempty"`
}

const (
	RoleComputer = "computer"
	RolePhone    = "phone"
)

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

// ---------- 系统 DPI / 分辨率 工具 ----------
// getDPIScale 通过 Windows API 获取主显示器 DPI 缩放比（如 1.0, 1.25, 1.5, 2.0）
func getDPIScale() float64 {
	// 需要 Windows 8.1 以上，回退为 1.0
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

// ---------- App ----------
type App struct {
	ctx                 context.Context
	sessionID           string
	role                string
	sigConn             *websocket.Conn
	pc                  *webrtc.PeerConnection
	dc                  *webrtc.DataChannel
	mu                  sync.Mutex
	peerReady           bool
	relayCaptureRunning bool

	// 屏幕参数（优化二：DPI 自适应）
	screenW     int     // 物理像素宽
	screenH     int     // 物理像素高
	logicalW    int     // 逻辑像素宽（DPI 缩放后）
	logicalH    int     // 逻辑像素高
	dpiScale    float64 // DPI 缩放系数 (1.0 / 1.25 / 1.5 / 2.0 …)
	captureW    int     // 屏幕捕获实际宽度（与截图一致）
	captureH    int     // 屏幕捕获实际高度
	insecureTLS bool    // wss 连接时是否跳过 TLS 证书校验（自签名证书）
}

func NewApp() *App {
	return &App{insecureTLS: true}
}

// SetInsecureTLS 当信令服务器使用自签名 TLS 证书时调用（仅限 wss://）
func (a *App) SetInsecureTLS(insecure bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.insecureTLS = insecure
}

// GetInsecureTLS 返回当前 TLS 证书校验跳过状态
func (a *App) GetInsecureTLS() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.insecureTLS
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	setupClientLog()

	// 获取物理分辨率
	a.screenW, a.screenH = robotgo.GetScreenSize()
	// 获取 DPI 缩放
	a.dpiScale = getDPIScale()
	// 计算逻辑分辨率
	a.logicalW = int(float64(a.screenW) / a.dpiScale)
	a.logicalH = int(float64(a.screenH) / a.dpiScale)

	log.Printf("[App] 物理=%dx%d  逻辑=%dx%d  DPI=%.2f",
		a.screenW, a.screenH, a.logicalW, a.logicalH, a.dpiScale)
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
	log.Printf("[App] 日志文件: %s", logPath)
}

// ---------- Wails bindings ----------

func (a *App) GetSessionID() string {
	return a.sessionID
}

func (a *App) getRole() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.role
}

func (a *App) GetPeerConnected() bool {
	return a.getPeerConnected()
}

func (a *App) getPeerConnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.peerReady
}

func (a *App) setPeerConnected(ready bool) {
	a.mu.Lock()
	a.peerReady = ready
	a.mu.Unlock()
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "peer_status", ready)
	}
}

func (a *App) writeSignalMessage(msgType int, msg []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sigConn == nil {
		return nil
	}
	return a.sigConn.WriteMessage(msgType, msg)
}

func (a *App) writeSignal(msg []byte) error {
	return a.writeSignalMessage(websocket.TextMessage, msg)
}

func (a *App) dataChannelOpen() bool {
	a.mu.Lock()
	dc := a.dc
	a.mu.Unlock()
	return dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen
}

// Connect dials the signaling server and establishes WebRTC.
func (a *App) Connect(role, signalingURL, sessionID string) error {
	a.sessionID = sessionID
	a.mu.Lock()
	a.role = role
	a.peerReady = false
	a.relayCaptureRunning = false
	a.mu.Unlock()

	u, _ := url.Parse(signalingURL)
	u.Path = path.Join("/connect", role)
	q := u.Query()
	if sessionID != "" {
		q.Set("sid", sessionID)
	}
	u.RawQuery = q.Encode()

	dialer := *websocket.DefaultDialer
	if u.Scheme == "wss" {
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: a.insecureTLS,
		}
	}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	a.sigConn = conn

	// ---- 优化一：ICE 配置（LAN / IPv6 / TURN 预留） ----
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
			// ── TURN 预留接口 ──
			// 在 Oracle Cloud 部署 coturn 后取消注释即可一键接入
			// {
			// 	URLs:           []string{"turn:你的甲骨文服务器IP:3478"},
			// 	Username:       "coturn用户名",
			// 	Credential:     "coturn密码",
			// 	CredentialType: webrtc.CredentialTypePassword,
			// },
		},
	}

	// 使用 SettingEngine 显式开启局域网和 IPv6 候选地址收集
	// 校园网环境下 IPv4 打洞困难，IPv6 直连 + LAN 候选可大幅提高成功率
	engine := webrtc.SettingEngine{}
	engine.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4,
		webrtc.NetworkTypeUDP6,
		webrtc.NetworkTypeTCP4,
		webrtc.NetworkTypeTCP6,
	})

	api := webrtc.NewAPI(webrtc.WithSettingEngine(engine))
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return err
	}
	a.pc = pc

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[App] PeerConnection state: %s", state.String())
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateDisconnected ||
			state == webrtc.PeerConnectionStateClosed {
			if a.getRole() == RoleComputer && state != webrtc.PeerConnectionStateClosed {
				a.startRelayCapture()
			} else {
				a.setPeerConnected(false)
			}
		}
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[App] ICE state: %s", state.String())
	})

	if role == RolePhone {
		dc, err := pc.CreateDataChannel("control", nil)
		if err != nil {
			return err
		}
		a.dc = dc
		a.setupDataChannel(dc)
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("[App] remote DataChannel: %s", dc.Label())
		a.dc = dc
		a.setupDataChannel(dc)
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		candJSON, _ := json.Marshal(c.ToJSON())
		msg, _ := json.Marshal(envelope{
			Type:    "ice_candidate",
			Payload: candJSON,
		})
		_ = a.writeSignal(msg)
	})

	go a.readSignaling()

	if role == RolePhone {
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			return err
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			return err
		}
		offerJSON, _ := json.Marshal(offer)
		msg, _ := json.Marshal(envelope{
			Type:    "offer",
			Payload: offerJSON,
		})
		_ = a.writeSignal(msg)
	}

	return nil
}

func (a *App) SendCommand(cmdJSON string) error {
	if a.dc == nil {
		return nil
	}
	return a.dc.SendText(cmdJSON)
}

// ---------- internal ----------

func (a *App) setupDataChannel(dc *webrtc.DataChannel) {
	dc.OnClose(func() {
		log.Printf("[App] DataChannel closed")
		if a.getRole() == RoleComputer && a.getPeerConnected() {
			a.startRelayCapture()
			return
		}
		a.setPeerConnected(false)
	})

	dc.OnError(func(err error) {
		log.Printf("[App] DataChannel error: %v", err)
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// 如果是文本消息，作为控制指令处理；二进制消息为屏幕帧（忽略，本端不需要渲染）
		if msg.IsString && a.getRole() == RoleComputer {
			a.handleCommand(string(msg.Data))
		}
	})

	// 作为被控端（computer），当 DataChannel 开启后开始推送屏幕帧
	dc.OnOpen(func() {
		a.setPeerConnected(true)
		if a.getRole() != RoleComputer {
			log.Printf("[App] DataChannel 已开启，当前角色无需推送屏幕")
			return
		}
		log.Printf("[App] DataChannel 已开启，开始屏幕捕获推送")
		a.dc = dc
		go func() {
			sent := 0
			ScreenCapture(func(frame []byte) bool {
				if dc.ReadyState() != webrtc.DataChannelStateOpen {
					return false
				}
				if dc.BufferedAmount()+uint64(len(frame)) > 2*1024*1024 {
					return true
				}
				if err := dc.Send(frame); err != nil {
					log.Printf("[App] 发送帧失败: %v", err)
					return false
				}
				sent++
				if sent == 1 || sent%50 == 0 {
					log.Printf("[App] 已发送屏幕帧 %d，当前帧 %d bytes", sent, len(frame))
				}
				return true
			})
		}()
	})
}

func (a *App) startRelayCapture() {
	if a.getRole() != RoleComputer {
		return
	}

	a.mu.Lock()
	if a.relayCaptureRunning {
		a.mu.Unlock()
		return
	}
	a.relayCaptureRunning = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.relayCaptureRunning = false
			a.mu.Unlock()
			log.Printf("[App] WebSocket relay capture stopped")
		}()

		sent := 0
		log.Printf("[App] WebSocket relay capture started")
		ScreenCapture(func(frame []byte) bool {
			if !a.getPeerConnected() {
				return false
			}
			if a.dataChannelOpen() {
				return false
			}
			if err := a.writeSignalMessage(websocket.BinaryMessage, frame); err != nil {
				log.Printf("[App] relay frame send failed: %v", err)
				return false
			}
			sent++
			if sent == 1 || sent%50 == 0 {
				log.Printf("[App] relay frames sent %d, frame %d bytes", sent, len(frame))
			}
			return true
		})
	}()
}

func (a *App) handleCommand(raw string) {
	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		log.Printf("[App] unmarshal error: %v", err)
		return
	}
	switch env.Type {
	case "MOUSE_MOVE":
		var d mouseMoveData
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return
		}
		a.execMouseMove(d)
	case "MOUSE_CLICK":
		var d mouseClickData
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return
		}
		a.execMouseClick(d)
	case "KEY_PRESS":
		var d keyPressData
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return
		}
		a.execKeyPress(d)
	case "SCROLL":
		var d scrollData
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return
		}
		a.execScroll(d)
	}
}

func (a *App) execMouseMove(d mouseMoveData) {
	// 核查点二：动态分辨率感知 + 物理边界钳位
	//
	// 受控端可能正在拔插显示器、切换分辨率，我们不能依赖
	// startup 时缓存的 screenW/screenH，必须在每次执行前
	// 获取当前主屏幕的物理尺寸。
	currentW, currentH := robotgo.GetScreenSize()

	x := int(d.XRatio * float64(currentW))
	y := int(d.YRatio * float64(currentH))

	// 严格钳位：max(0, min(x, currentW-1))
	// 杜绝负数或越界坐标，防止驱动层崩溃
	if x < 0 {
		x = 0
	} else if x >= currentW {
		x = currentW - 1
	}
	if y < 0 {
		y = 0
	} else if y >= currentH {
		y = currentH - 1
	}

	robotgo.MoveMouse(x, y)
}

func (a *App) execMouseClick(d mouseClickData) {
	switch d.Button {
	case "left":
		robotgo.MouseClick("left")
	case "right":
		robotgo.MouseClick("right")
	case "middle":
		robotgo.MouseClick("center")
	}
}

func (a *App) execKeyPress(d keyPressData) {
	robotgo.KeyTap(d.Key)
}

func (a *App) execScroll(d scrollData) {
	// deltaY > 0 向下滚, < 0 向上滚
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

func (a *App) readSignaling() {
	defer func() {
		if !a.dataChannelOpen() {
			a.setPeerConnected(false)
		}
		a.sigConn.Close()
	}()
	for {
		_, raw, err := a.sigConn.ReadMessage()
		if err != nil {
			log.Printf("[App] signaling read error: %v", err)
			return
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		switch env.Type {
		case "session_assigned":
			var sid string
			json.Unmarshal(env.Payload, &sid)
			a.sessionID = sid
			log.Printf("[App] 服务器分配房间码: %s", sid)

		case "offer":
			if a.pc == nil {
				log.Printf("[App] pc is nil, skipping")
				continue
			}

			var desc webrtc.SessionDescription
			json.Unmarshal(env.Payload, &desc)
			a.pc.SetRemoteDescription(desc)
			answer, _ := a.pc.CreateAnswer(nil)
			a.pc.SetLocalDescription(answer)
			ansJSON, _ := json.Marshal(answer)
			msg, _ := json.Marshal(envelope{Type: "answer", Payload: ansJSON})
			_ = a.writeSignal(msg)

		case "answer":
			if a.pc == nil {
				continue
			}
			var desc webrtc.SessionDescription
			json.Unmarshal(env.Payload, &desc)
			a.pc.SetRemoteDescription(desc)

		case "ice_candidate":
			if a.pc == nil {
				continue
			}
			var cand webrtc.ICECandidateInit
			if err := json.Unmarshal(env.Payload, &cand); err != nil {
				var candText string
				if json.Unmarshal(env.Payload, &candText) == nil {
					_ = json.Unmarshal([]byte(candText), &cand)
				}
			}
			a.pc.AddICECandidate(cand)

		case "peer_joined":
			a.setPeerConnected(true)
			log.Printf("[App] peer joined session %s", a.sessionID)
			a.startRelayCapture()

		// ── 优化：WebSocket 回退通道的控制指令 ──
		// 当浏览器端 WebRTC DataChannel 未能打通时，
		// 控制指令通过信令 WebSocket 转发到达此处。
		// 复用 handleCommand 确保与 DataChannel 走同一套 DPI 换算逻辑。
		case "MOUSE_MOVE", "MOUSE_CLICK", "KEY_PRESS", "SCROLL":
			if a.getRole() == RoleComputer {
				a.handleCommand(string(raw))
			}
		}
	}
}
