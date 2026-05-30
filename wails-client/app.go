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
	"strings"
	"sync"

	"github.com/go-vgo/robotgo"
	"github.com/gorilla/websocket"
	"github.com/kbinani/screenshot"
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

// ---------- system DPI helpers ----------
func getDPIScale() float64 {
	// 闂団偓鐟?Windows 8.1 娴犮儰绗傞敍灞芥礀闁偓娑?1.0
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
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
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

func buildICEServers() []webrtc.ICEServer {
	servers := []webrtc.ICEServer{
		{URLs: defaultSTUNURLs},
	}

	username := strings.TrimSpace(os.Getenv("TURN_USERNAME"))
	if username == "" {
		username = "remoteuser"
	}
	credential := strings.TrimSpace(os.Getenv("TURN_PASSWORD"))
	if credential == "" {
		return servers
	}

	urls := splitEnvList(os.Getenv("TURN_URLS"))
	if len(urls) == 0 {
		urls = defaultTURNURLs
	}

	servers = append(servers, webrtc.ICEServer{
		URLs:           urls,
		Username:       username,
		Credential:     credential,
		CredentialType: webrtc.ICECredentialTypePassword,
	})
	return servers
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

	screenW     int
	screenH     int
	logicalW    int
	logicalH    int
	dpiScale    float64
	captureW    int
	captureH    int
	insecureTLS bool
}

func NewApp() *App {
	return &App{insecureTLS: true}
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

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	setupClientLog()
	enableDPIAwareness()

	// 閼惧嘲褰囬悧鈺冩倞閸掑棜椴搁悳?	a.screenW, a.screenH = robotgo.GetScreenSize()
	// 閼惧嘲褰?DPI 缂傗晜鏂?	a.dpiScale = getDPIScale()
	// 鐠侊紕鐣婚柅鏄忕帆閸掑棜椴搁悳?	a.logicalW = int(float64(a.screenW) / a.dpiScale)
	a.logicalH = int(float64(a.screenH) / a.dpiScale)

	log.Printf("[App] 閻椻晝鎮?%dx%d  闁槒绶?%dx%d  DPI=%.2f",
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
	log.Printf("[App] 閺冦儱绻旈弬鍥︽: %s", logPath)
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

	// ---- 娴兼ê瀵叉稉鈧敍娆糃E 闁板秶鐤嗛敍鍦燗N / IPv6 / TURN 妫板嫮鏆€閿?----
	config := webrtc.Configuration{
		ICEServers: buildICEServers(),
	}

	// 娴ｈ法鏁?SettingEngine 閺勬儳绱″鈧崥顖氱湰閸╃喓缍夐崪?IPv6 閸婃瑩鈧婀撮崸鈧弨鍫曟肠
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
		// 婵″倹鐏夐弰顖涙瀮閺堫剚绉烽幁顖ょ礉娴ｆ粈璐熼幒褍鍩楅幐鍥︽姢婢跺嫮鎮婇敍娑楃癌鏉╂稑鍩楀☉鍫熶紖娑撳搫鐫嗛獮鏇炴姎閿涘牆鎷烽悾銉礉閺堫剛顏稉宥夋付鐟曚焦瑕嗛弻鎿勭礆
		if msg.IsString && a.getRole() == RoleComputer {
			a.handleCommand(string(msg.Data))
		}
	})

	// 娴ｆ粈璐熺悮顐ｅ付缁旑垽绱檆omputer閿涘绱濊ぐ?DataChannel 瀵偓閸氼垰鎮楀鈧慨瀣腹闁礁鐫嗛獮鏇炴姎
	dc.OnOpen(func() {
		a.setPeerConnected(true)
		if a.getRole() != RoleComputer {
			log.Printf("[App] DataChannel opened, no screen push needed for this role")
			return
		}
		log.Printf("[App] DataChannel opened, start screen capture")
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
					log.Printf("[App] 閸欐垿鈧礁鎶氭径杈Е: %v", err)
					return false
				}
				sent++
				if sent == 1 || sent%50 == 0 {
					log.Printf("[App] 瀹告彃褰傞柅浣哥潌楠炴洖鎶?%d閿涘苯缍嬮崜宥呮姎 %d bytes", sent, len(frame))
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
			log.Printf("[App] 閺堝秴濮熼崳銊ュ瀻闁板秵鍩ч梻瀵哥垳: %s", sid)

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

			// 閳光偓閳光偓 娴兼ê瀵查敍姝恊bSocket 閸ョ偤鈧偓闁岸浜鹃惃鍕付閸掕埖瀵氭禒?閳光偓閳光偓
			// 瑜版挻绁荤憴鍫濇珤缁?WebRTC DataChannel 閺堫亣鍏橀幍鎾烩偓姘閿?		// 閹貉冨煑閹稿洣鎶ら柅姘崇箖娣団€叉姢 WebSocket 鏉烆剙褰傞崚鎷屾彧濮濄倕顦╅妴?		// 婢跺秶鏁?handleCommand 绾喕绻氭稉?DataChannel 鐠ф澘鎮撴稉鈧總?DPI 閹广垻鐣婚柅鏄忕帆閵?		case "MOUSE_MOVE", "MOUSE_CLICK", "KEY_PRESS", "SCROLL":
			if a.getRole() == RoleComputer {
				a.handleCommand(string(raw))
			}
		}
	}
}
