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
	// 闂傚洠鍋撻悷?Windows 8.1 濞寸姰鍎扮粭鍌炴晬鐏炶姤绀€闂侇偀鍋撳☉?1.0
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

	// 闁兼儳鍢茶ぐ鍥偋閳哄啯鍊為柛鎺戞妞存悂鎮?	a.screenW, a.screenH = robotgo.GetScreenSize()
	// 闁兼儳鍢茶ぐ?DPI 缂傚倵鏅滈弬?	a.dpiScale = getDPIScale()
	// 閻犱緤绱曢悾濠氭焻閺勫繒甯嗛柛鎺戞妞存悂鎮?	a.logicalW = int(float64(a.screenW) / a.dpiScale)
	a.logicalH = int(float64(a.screenH) / a.dpiScale)

	log.Printf("[App] 闁绘せ鏅濋幃?%dx%d  闂侇偅妲掔欢?%dx%d  DPI=%.2f",
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
	log.Printf("[App] 闁哄啨鍎辩换鏃堝棘閸ワ附顐? %s", logPath)
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

	// ---- 濞村吋锚鐎靛弶绋夐埀顒勬晬濞嗙硟E 闂佹澘绉堕悿鍡涙晬閸︾嚄N / IPv6 / TURN 濡澘瀚弳鈧柨?----
	config := webrtc.Configuration{
		ICEServers: buildICEServers(),
	}

	// 濞达綀娉曢弫?SettingEngine 闁哄嫭鍎崇槐鈥愁嚕閳ь剟宕ラ姘辨拱闁糕晝鍠撶紞澶愬椽?IPv6 闁稿﹥鐟╅埀顒€顦﹢鎾锤閳ь剟寮ㄩ崼鏇熻偁
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
		// 濠碘€冲€归悘澶愬及椤栨稒鐎柡鍫墯缁夌兘骞侀銈囩濞达絾绮堢拹鐔煎箳瑜嶉崺妤呭箰閸ワ附濮㈠璺哄閹﹪鏁嶅☉妤冪檶閺夆晜绋戦崺妤€鈽夐崼鐔剁礀濞戞挸鎼惈鍡涚嵁閺囩偞濮庨柨娑樼墕閹风兘鎮鹃妷顖滅闁哄牜鍓涢顒佺▔瀹ュ浠橀悷鏇氱劍鐟曞棝寮婚幙鍕
		if msg.IsString && a.getRole() == RoleComputer {
			a.handleCommand(string(msg.Data))
		}
	})

	// 濞达絾绮堢拹鐔烘偖椤愶絽浠樼紒鏃戝灲缁辨獑omputer闁挎稑顧€缁辨繆銇?DataChannel 鐎殿喒鍋撻柛姘煎灠閹顕ｉ埀顒佹叏鐎ｎ偄鑵归梺顐＄閻棝鐛弴鐐村
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
					log.Printf("[App] 闁告瑦鍨块埀顑跨閹舵碍寰勬潏顐バ? %v", err)
					return false
				}
				sent++
				if sent == 1 || sent%50 == 0 {
					log.Printf("[App] 鐎瑰憡褰冭ぐ鍌炴焻娴ｅ摜娼屾鐐存礀閹?%d闁挎稑鑻紞瀣礈瀹ュ懏濮?%d bytes", sent, len(frame))
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
			log.Printf("[App] 闁哄牆绉存慨鐔煎闯閵娿儱鐎婚梺鏉跨У閸┭囨⒒鐎靛摜鍨? %s", sid)

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

		case "peer_left":
			var leftRole string
			_ = json.Unmarshal(env.Payload, &leftRole)
			log.Printf("[App] peer left session %s: %s", a.sessionID, leftRole)
			a.setPeerConnected(false)

			// 闁冲厜鍋撻柍鍏夊亾 濞村吋锚鐎垫煡鏁嶅鎭奲Socket 闁搞儳鍋ら埀顑藉亾闂侇偅宀告禍楣冩儍閸曨剙浠橀柛鎺曞煐鐎垫碍绂?闁冲厜鍋撻柍鍏夊亾
			// 鐟滅増鎸荤粊鑽ゆ喆閸繃鐝ょ紒?WebRTC DataChannel 闁哄牜浜ｉ崗姗€骞嶉幘鐑╁亾濮橆厽顦ч柨?		// 闁硅矇鍐ㄧ厬闁圭娲ｉ幎銈夋焻濮樺磭绠栧ǎ鍥ｂ偓鍙夊Б WebSocket 閺夌儐鍓欒ぐ鍌炲礆閹峰本褰ф慨婵勫€曢ˇ鈺呭Υ?		// 濠㈣泛绉堕弫?handleCommand 缁绢収鍠曠换姘▔?DataChannel 閻犙勬緲閹挻绋夐埀顒佺附?DPI 闁瑰箍鍨婚悾濠氭焻閺勫繒甯嗛柕?		case "MOUSE_MOVE", "MOUSE_CLICK", "KEY_PRESS", "SCROLL":
			if a.getRole() == RoleComputer {
				a.handleCommand(string(raw))
			}
		}
	}
}
