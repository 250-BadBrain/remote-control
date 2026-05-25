package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/url"
	"sync"

	"github.com/go-vgo/robotgo"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"golang.org/x/sys/windows"
)

// ---------- protocol types ----------
type envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to,omitempty"`
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
	ctx       context.Context
	sessionID string
	sigConn   *websocket.Conn
	pc        *webrtc.PeerConnection
	dc        *webrtc.DataChannel
	mu        sync.Mutex

	// 屏幕参数（优化二：DPI 自适应）
	screenW    int     // 物理像素宽
	screenH    int     // 物理像素高
	logicalW   int     // 逻辑像素宽（DPI 缩放后）
	logicalH   int     // 逻辑像素高
	dpiScale   float64 // DPI 缩放系数 (1.0 / 1.25 / 1.5 / 2.0 …)
	captureW   int     // 屏幕捕获实际宽度（与截图一致）
	captureH   int     // 屏幕捕获实际高度
	insecureTLS bool   // wss 连接时是否跳过 TLS 证书校验（自签名证书）
}

func NewApp() *App {
	return &App{}
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

// ---------- Wails bindings ----------

func (a *App) GetSessionID() string {
	return a.sessionID
}

// Connect dials the signaling server and establishes WebRTC.
func (a *App) Connect(role, signalingURL, sessionID string) error {
	a.sessionID = sessionID

	u, _ := url.Parse(signalingURL)
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

	if role == "phone" {
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
		a.sigConn.WriteMessage(websocket.TextMessage, msg)
	})

	go a.readSignaling()

	if role == "phone" {
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
		a.sigConn.WriteMessage(websocket.TextMessage, msg)
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
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// 如果是文本消息，作为控制指令处理；二进制消息为屏幕帧（忽略，本端不需要渲染）
		if msg.IsText {
			a.handleCommand(string(msg.Data))
		}
	})

	// 作为被控端（computer），当 DataChannel 开启后开始推送屏幕帧
	dc.OnOpen(func() {
		log.Printf("[App] DataChannel 已开启，开始屏幕捕获推送")
		a.dc = dc
		go ScreenCapture(func(frame []byte) {
			if dc.ReadyState() == webrtc.DataChannelStateOpen {
				if err := dc.Send(frame); err != nil {
					log.Printf("[App] 发送帧失败: %v", err)
				}
			}
		})
	})
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
			robotgo.ScrollMouse(1, "down")
		}
	} else {
		for i := 0; i < -clicks; i++ {
			robotgo.ScrollMouse(1, "up")
		}
	}
}

func (a *App) readSignaling() {
	defer a.sigConn.Close()
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
			a.sigConn.WriteMessage(websocket.TextMessage, msg)

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
			json.Unmarshal(env.Payload, &cand)
			a.pc.AddICECandidate(cand)

		case "peer_joined":
			log.Printf("[App] peer joined session %s", a.sessionID)

		// ── 优化：WebSocket 回退通道的控制指令 ──
		// 当浏览器端 WebRTC DataChannel 未能打通时，
		// 控制指令通过信令 WebSocket 转发到达此处。
		// 复用 handleCommand 确保与 DataChannel 走同一套 DPI 换算逻辑。
		case "MOUSE_MOVE", "MOUSE_CLICK", "KEY_PRESS", "SCROLL":
			a.handleCommand(string(raw))
		}
	}
}