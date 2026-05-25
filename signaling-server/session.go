package main

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// ---------- role constants ----------
const (
	RoleComputer = "computer"
	RolePhone    = "phone"
)

// ---------- wire protocol ----------
type envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to,omitempty"`
}

// ---------- 优化四：类型感知的消息单元 ----------
type outgoingMsg struct {
	msgType int    // websocket.TextMessage or websocket.BinaryMessage
	data    []byte // 零拷贝：直接引用原始 byte 切片
}

// ---------- peer ----------
type peer struct {
	role string
	conn *websocket.Conn
	send chan outgoingMsg
}

// ---------- session ----------
type session struct {
	id       string
	computer *peer
	phone    *peer
	mu       sync.RWMutex
}

// ---------- hub ----------
type Hub struct {
	sessions map[string]*session
	mu       sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{sessions: make(map[string]*session)}
}

func (h *Hub) Run() {}

// generateRoomCode 生成 6 位纯数字房间码（如 888999）
func generateRoomCode() string {
	const digits = "0123456789"
	code := make([]byte, 6)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			code[i] = digits[i%10]
			continue
		}
		code[i] = digits[n.Int64()]
	}
	return string(code)
}

func (h *Hub) getOrCreateSession(sid string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sid]
	if !ok {
		s = &session{id: sid}
		h.sessions[sid] = s
		log.Printf("[Hub] 创建新会话 %s", sid)
	}
	return s
}

// getSession 只读获取 session，不创建（用于 forward 路径）
func (h *Hub) getSession(sid string) *session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[sid]
}

func (h *Hub) removeSession(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, sid)
	log.Printf("[Hub] 删除会话 %s", sid)
}

// assignPeer 将 peer 注册到会话中
func (h *Hub) assignPeer(sid string, p *peer) {
	s := h.getOrCreateSession(sid)
	s.mu.Lock()
	defer s.mu.Unlock()

	switch p.role {
	case RoleComputer:
		s.computer = p
	case RolePhone:
		s.phone = p
	}

	log.Printf("[Hub] 会话 %s: %s 加入 (computer=%v phone=%v)",
		sid, p.role, s.computer != nil, s.phone != nil)

	var partner *peer
	if p.role == RoleComputer && s.phone != nil {
		partner = s.phone
	} else if p.role == RolePhone && s.computer != nil {
		partner = s.computer
	}
	if partner != nil {
		notif, _ := json.Marshal(envelope{Type: "peer_joined", Payload: nil})
		select {
		case partner.send <- outgoingMsg{msgType: websocket.TextMessage, data: notif}:
		default:
		}
		log.Printf("[Hub] 会话 %s: 双方均已就绪，发送 peer_joined", sid)
	}
}

func (h *Hub) unassignPeer(sid string, p *peer) {
	s := h.getOrCreateSession(sid)
	s.mu.Lock()
	defer s.mu.Unlock()

	switch p.role {
	case RoleComputer:
		s.computer = nil
	case RolePhone:
		s.phone = nil
	}

	log.Printf("[Hub] 会话 %s: %s 离开", sid, p.role)

	if s.computer == nil && s.phone == nil {
		h.removeSession(sid)
	}
}

// forwardWithType 将消息转发给会话中的对端（优化四：保留消息类型，零拷贝 byte 切片）
func (h *Hub) forwardWithType(sid, fromRole string, msg []byte, msgType int) {
	s := h.getSession(sid)
	if s == nil {
		log.Printf("[Hub] 会话 %s 不存在，丢弃消息", sid)
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var target *peer
	if fromRole == RoleComputer {
		target = s.phone
	} else {
		target = s.computer
	}
	if target == nil {
		log.Printf("[Hub] 会话 %s: 无目标 (%s 源)，丢弃 %d 字节", sid, fromRole, len(msg))
		return
	}

	select {
	case target.send <- outgoingMsg{msgType: msgType, data: msg}:
	default:
		log.Printf("[Hub] 会话 %s: 目标发送缓冲区满，丢弃 %d 字节", sid, len(msg))
	}
}

// ---------- WebSocket wiring ----------
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// serveWS 处理 WebSocket 升级与消息循环
func serveWS(hub *Hub, w http.ResponseWriter, r *http.Request, role string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] 升级失败: %v", err)
		return
	}

	// ---- 处理 Session ID ----
	sid := r.URL.Query().Get("sid")

	if role == RoleComputer && sid == "" {
		sid = generateRoomCode()
		log.Printf("[Hub] 为新电脑分配房间码: %s", sid)
	}

	if sid == "" {
		log.Printf("[WS] 拒绝连接: 未提供 sid")
		conn.WriteJSON(envelope{Type: "error", Payload: []byte(`"missing session ID"`)})
		conn.Close()
		return
	}

	p := &peer{
		role: role,
		conn: conn,
		send: make(chan outgoingMsg, 128), // 增大缓冲区适应二进制帧
	}

	hub.assignPeer(sid, p)

	// ---- 如果是电脑端且刚分配了房间码，发送给客户端 ----
	if role == RoleComputer {
		sidPayload, _ := json.Marshal(sid)
		assigned, _ := json.Marshal(envelope{Type: "session_assigned", Payload: sidPayload})
		select {
		case p.send <- outgoingMsg{msgType: websocket.TextMessage, data: assigned}:
		default:
		}
	}

	// ---- write pump: 类型感知写入（优化四） ----
	go func() {
		defer conn.Close()
		for msg := range p.send {
			if err := conn.WriteMessage(msg.msgType, msg.data); err != nil {
				log.Printf("[WS] 写入错误 (%s %s): %v", sid, role, err)
				return
			}
		}
	}()

	// ---- read pump: 读取并转发（优化四：保留二进制类型） ----
	defer func() {
		hub.unassignPeer(sid, p)
		conn.Close()
		close(p.send)
	}()

	for {
		msgType, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[WS] 读取错误 (%s %s): %v", sid, role, err)
			break
		}

		// 二进制消息直接转发（优化四：零拷贝，无 JSON 开销）
		if msgType == websocket.BinaryMessage {
			hub.forwardWithType(sid, role, raw, websocket.BinaryMessage)
			continue
		}

		// TextMessage: 检测 forward 信封
		var msg envelope
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Type == "forward" {
			var inner struct {
				From    string          `json:"from"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(msg.Payload, &inner) == nil {
				// 内层 payload 以原始 JSON 字节转发（零拷贝 RawMessage）
				hub.forwardWithType(sid, inner.From, []byte(inner.Payload), websocket.TextMessage)
				continue
			}
		}
		hub.forwardWithType(sid, role, raw, websocket.TextMessage)
	}
}
