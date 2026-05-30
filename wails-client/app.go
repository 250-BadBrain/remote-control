package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
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

const (
	frameChunkMagic   uint32 = 0x52434631 // "RCF1"
	frameChunkHeader         = 12
	frameChunkPayload        = 60 * 1024
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

// ---------- system DPI helpers ----------
func getDPIScale() float64 {
	// 闂傚倸娲犻崑鎾绘偡?Windows 8.1 婵炲濮伴崕鎵箔閸岀偞鏅悘鐐跺Г缁€鈧梻渚囧亐閸嬫挸鈽?1.0
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
		log.Printf("[ICE] TURN disabled: TURN_PASSWORD is not set")
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
	log.Printf("[ICE] TURN enabled: urls=%v username=%s", urls, username)
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

	// 闂佸吋鍎抽崲鑼躲亹閸ヮ剚鍋嬮柍鍝勫暞閸婄偤鏌涢幒鎴烆棥濡炲瓨鎮傞幃?	a.screenW, a.screenH = robotgo.GetScreenSize()
	// 闂佸吋鍎抽崲鑼躲亹?DPI 缂傚倸鍊甸弲婊堝棘?	a.dpiScale = getDPIScale()
	// 闁荤姳绶ょ槐鏇㈡偩婵犳碍鐒婚柡鍕箳鐢棝鏌涢幒鎴烆棥濡炲瓨鎮傞幃?	a.logicalW = int(float64(a.screenW) / a.dpiScale)
	a.logicalH = int(float64(a.screenH) / a.dpiScale)

	log.Printf("[App] screen physical=%dx%d logical=%dx%d dpiScale=%.2f",
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
	log.Printf("[App] log file: %s", logPath)
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

	// ---- 婵炴潙鍚嬮敋閻庨潧寮剁粙澶愬焵椤掑嫭鏅繛鍡欑E 闂備焦婢樼粔鍫曟偪閸℃稒鏅柛锔惧殑N / IPv6 / TURN 婵☆偅婢樼€氼噣寮抽埀顒勬煥?----
	config := webrtc.Configuration{
		ICEServers: buildICEServers(),
	}

	// 婵炶揪缍€濞夋洟寮?SettingEngine 闂佸搫瀚崕宕囨閳ユ剚鍤曢柍褜鍓熷畷銉╊敍濮樿鲸鎷遍梺绯曟櫇閸犳挾绱炴径鎰そ?IPv6 闂佺锕ラ悷鈺呭焵椤掆偓椤︻垰锕㈤幘顔奸敜闁逞屽墴瀵劑宕奸弴鐔诲亖
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
			log.Printf("[ICE] local candidate gathering complete")
			return
		}
		candidate := c.ToJSON()
		log.Printf("[ICE] local candidate: %s", candidate.Candidate)
		candJSON, _ := json.Marshal(candidate)
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
		if msg.IsString && a.getRole() == RoleComputer {
			log.Printf("[Command] received via DataChannel: %s", string(msg.Data))
			a.handleCommand(string(msg.Data))
		}
	})

	// 婵炶揪绲剧划鍫㈡嫻閻旂儤鍋栨い鎰剁到娴犳绱掗弮鎴濈伈缂佽鲸鐛憃mputer闂佹寧绋戦¨鈧紒杈ㄧ箚閵?DataChannel 閻庢鍠掗崑鎾绘煕濮樼厧鐏犻柟顔筋殔椤曪綁鍩€椤掍焦鍙忛悗锝庡亜閼靛綊姊洪锛勵槮闁活偄妫濋悰顕€寮撮悙鏉戭潛
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
			frameID := uint32(0)
			ScreenCapture(func(frame []byte) bool {
				if dc.ReadyState() != webrtc.DataChannelStateOpen {
					return false
				}
				if dc.BufferedAmount()+uint64(len(frame)) > 2*1024*1024 {
					return true
				}
				frameID++
				if err := sendFrameDataChannel(dc, frameID, frame); err != nil {
					log.Printf("[App] datachannel frame send failed: %v", err)
					return false
				}
				sent++
				if sent == 1 || sent%50 == 0 {
					log.Printf("[App] datachannel frames sent=%d frameBytes=%d", sent, len(frame))
				}
				return true
			})
		}()
	})
}

func sendFrameDataChannel(dc *webrtc.DataChannel, frameID uint32, frame []byte) error {
	if len(frame) <= frameChunkPayload {
		return dc.Send(frame)
	}

	total := (len(frame) + frameChunkPayload - 1) / frameChunkPayload
	if total > 65535 {
		return dc.Send(frame)
	}

	for index := 0; index < total; index++ {
		start := index * frameChunkPayload
		end := start + frameChunkPayload
		if end > len(frame) {
			end = len(frame)
		}
		chunk := make([]byte, frameChunkHeader+end-start)
		binary.BigEndian.PutUint32(chunk[0:4], frameChunkMagic)
		binary.BigEndian.PutUint32(chunk[4:8], frameID)
		binary.BigEndian.PutUint16(chunk[8:10], uint16(total))
		binary.BigEndian.PutUint16(chunk[10:12], uint16(index))
		copy(chunk[frameChunkHeader:], frame[start:end])
		if err := dc.Send(chunk); err != nil {
			return err
		}
	}
	return nil
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
			log.Printf("[Signal] session assigned: %s", sid)

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
			log.Printf("[ICE] remote candidate: %s", cand.Candidate)
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

		case "MOUSE_MOVE", "MOUSE_CLICK", "KEY_PRESS", "SCROLL":
			if a.getRole() == RoleComputer {
				log.Printf("[Command] received via WebSocket relay: %s", env.Type)
				a.handleCommand(string(raw))
			}
		}
	}
}
