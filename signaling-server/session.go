package main

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	RoleComputer = "computer"
	RolePhone    = "phone"
)

type envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to,omitempty"`
}

type outgoingMsg struct {
	msgType int
	data    []byte
}

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 70 * time.Second
	wsPingPeriod = 25 * time.Second
)

type peer struct {
	role        string
	conn        *websocket.Conn
	send        chan outgoingMsg
	latestFrame chan []byte
}

type session struct {
	id       string
	computer *peer
	phone    *peer
	mu       sync.RWMutex
}

type Hub struct {
	sessions map[string]*session
	mu       sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{sessions: make(map[string]*session)}
}

func (h *Hub) Run() {}

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
		log.Printf("[Hub] create session %s", sid)
	}
	return s
}

func (h *Hub) getSession(sid string) *session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[sid]
}

func (h *Hub) removeSession(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, sid)
	log.Printf("[Hub] remove session %s", sid)
}

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

	log.Printf("[Hub] session %s: %s joined (computer=%v phone=%v)",
		sid, p.role, s.computer != nil, s.phone != nil)

	var partner *peer
	if p.role == RoleComputer && s.phone != nil {
		partner = s.phone
	} else if p.role == RolePhone && s.computer != nil {
		partner = s.computer
	}

	if partner != nil {
		notif, _ := json.Marshal(envelope{Type: "peer_joined", Payload: nil})
		trySend(partner, websocket.TextMessage, notif)
		trySend(p, websocket.TextMessage, notif)
		log.Printf("[Hub] session %s: both peers ready, sent peer_joined", sid)
	}
}

func (h *Hub) unassignPeer(sid string, p *peer) {
	s := h.getOrCreateSession(sid)
	s.mu.Lock()
	defer s.mu.Unlock()

	var partner *peer
	switch p.role {
	case RoleComputer:
		s.computer = nil
		partner = s.phone
	case RolePhone:
		s.phone = nil
		partner = s.computer
	}

	log.Printf("[Hub] session %s: %s left", sid, p.role)

	if partner != nil {
		payload, _ := json.Marshal(p.role)
		notif, _ := json.Marshal(envelope{Type: "peer_left", Payload: payload})
		if !trySend(partner, websocket.TextMessage, notif) {
			log.Printf("[Hub] session %s: peer_left dropped because target queue is full", sid)
		}
	}

	if s.computer == nil && s.phone == nil {
		h.removeSession(sid)
	}
}

func (h *Hub) forwardWithType(sid, fromRole string, msg []byte, msgType int) {
	s := h.getSession(sid)
	if s == nil {
		log.Printf("[Hub] session %s not found, drop message", sid)
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
		log.Printf("[Hub] session %s: no target for source=%s, drop %d bytes", sid, fromRole, len(msg))
		return
	}

	if msgType == websocket.BinaryMessage {
		h.forwardLatestFrame(sid, target, msg)
		return
	}

	var env envelope
	if json.Unmarshal(msg, &env) == nil && env.Type != "" {
		log.Printf("[Hub] session %s: forward text type=%s from=%s bytes=%d", sid, env.Type, fromRole, len(msg))
	}
	if !trySend(target, msgType, msg) {
		log.Printf("[Hub] session %s: target control queue full, drop %d bytes", sid, len(msg))
	}
}

func (h *Hub) forwardLatestFrame(sid string, target *peer, frame []byte) {
	select {
	case target.latestFrame <- frame:
		return
	default:
	}

	select {
	case <-target.latestFrame:
	default:
	}

	select {
	case target.latestFrame <- frame:
	default:
		log.Printf("[Hub] session %s: target frame slot full, drop %d bytes", sid, len(frame))
	}
}

func trySend(p *peer, msgType int, data []byte) bool {
	select {
	case p.send <- outgoingMsg{msgType: msgType, data: data}:
		return true
	default:
		return false
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func serveWS(hub *Hub, w http.ResponseWriter, r *http.Request, role string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] upgrade failed: %v", err)
		return
	}

	sid := r.URL.Query().Get("sid")
	if role == RoleComputer && sid == "" {
		sid = generateRoomCode()
		log.Printf("[Hub] assigned room code for computer: %s", sid)
	}

	if sid == "" {
		log.Printf("[WS] reject connection: missing sid")
		_ = conn.WriteJSON(envelope{Type: "error", Payload: []byte(`"missing session ID"`)})
		_ = conn.Close()
		return
	}

	p := &peer{
		role:        role,
		conn:        conn,
		send:        make(chan outgoingMsg, 64),
		latestFrame: make(chan []byte, 1),
	}

	hub.assignPeer(sid, p)

	if role == RoleComputer {
		sidPayload, _ := json.Marshal(sid)
		assigned, _ := json.Marshal(envelope{Type: "session_assigned", Payload: sidPayload})
		trySend(p, websocket.TextMessage, assigned)
	}

	go writePump(conn, p, sid, role)

	defer func() {
		hub.unassignPeer(sid, p)
		_ = conn.Close()
		close(p.send)
		close(p.latestFrame)
	}()

	conn.SetReadLimit(4 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	for {
		msgType, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[WS] read error (%s %s): %v", sid, role, err)
			break
		}

		if msgType == websocket.BinaryMessage {
			hub.forwardWithType(sid, role, raw, websocket.BinaryMessage)
			continue
		}

		var msg envelope
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[WS] invalid text message (%s %s): %v", sid, role, err)
			continue
		}

		if msg.Type == "forward" {
			var inner struct {
				From    string          `json:"from"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(msg.Payload, &inner) == nil {
				payload := []byte(inner.Payload)
				if len(payload) > 0 && payload[0] == '"' {
					var text string
					if json.Unmarshal(inner.Payload, &text) == nil {
						payload = []byte(text)
					}
				}
				log.Printf("[WS] forward envelope sid=%s from=%s bytes=%d", sid, inner.From, len(payload))
				hub.forwardWithType(sid, inner.From, payload, websocket.TextMessage)
				continue
			}
		}

		hub.forwardWithType(sid, role, raw, websocket.TextMessage)
	}
}

func writePump(conn *websocket.Conn, p *peer, sid, role string) {
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()
	defer conn.Close()

	for {
		select {
		case msg, ok := <-p.send:
			if !ok {
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if !writeWS(conn, sid, role, msg.msgType, msg.data) {
				return
			}
			continue
		default:
		}

		select {
		case msg, ok := <-p.send:
			if !ok {
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if !writeWS(conn, sid, role, msg.msgType, msg.data) {
				return
			}
		case frame, ok := <-p.latestFrame:
			if !ok {
				return
			}
			if !writeWS(conn, sid, role, websocket.BinaryMessage, frame) {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[WS] ping error (%s %s): %v", sid, role, err)
				return
			}
		}
	}
}

func writeWS(conn *websocket.Conn, sid, role string, msgType int, data []byte) bool {
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	if err := conn.WriteMessage(msgType, data); err != nil {
		log.Printf("[WS] write error (%s %s): %v", sid, role, err)
		return false
	}
	return true
}
