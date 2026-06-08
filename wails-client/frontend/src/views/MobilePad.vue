<template>
  <div class="mobile-pad" @contextmenu.prevent>
    <div v-if="!connected" class="connect-form">
      <div class="connect-panel">
        <h1>远程控制</h1>

        <label class="field-label" for="room-code">房间码</label>
        <input
          id="room-code"
          v-model="inputCode"
          placeholder="输入 6 位房间码"
          maxlength="6"
          inputmode="numeric"
          autocomplete="one-time-code"
          class="code-input"
          :class="{ 'has-code': inputCode.length > 0 }"
        />

        <label class="field-label" for="signal-server">信令服务器</label>
        <input
          id="signal-server"
          v-model="inputServer"
          placeholder="wss://signal.h2seo4.win:8443"
          autocomplete="url"
          autocapitalize="off"
          spellcheck="false"
          class="server-input"
        />

        <button @click="doConnect" class="btn-connect">连接</button>
      </div>
      <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
    </div>

    <div v-else class="control-area">
      <div class="status-bar">
        <span class="room-badge">{{ sessionId }}</span>
        <span v-if="connectionMode === 'relay'" class="relay-badge">中继控制</span>
        <span v-else-if="connectionMode === 'connecting'" class="wait-badge">握手中...</span>
        <span v-else class="direct-badge">视频直连</span>
      </div>

      <div class="preview-bar">
        <video ref="remoteVideo" class="remote-video" autoplay playsinline muted />
      </div>

      <div class="pad-row">
        <div ref="joystickZone" class="joystick-zone" />
        <div class="btn-group">
          <button class="touch-btn btn-left" @touchstart.prevent="clickLeft" @touchend.prevent>
            <span>左键</span>
          </button>
          <button class="touch-btn btn-right" @touchstart.prevent="clickRight" @touchend.prevent>
            <span>右键</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import nipplejs from 'nipplejs'
import { buildIceServers } from '../utils/ice'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

type ConnMode = 'connecting' | 'webrtc' | 'relay'

const connectionMode = ref<ConnMode>('connecting')
const inputCode = ref('')
const inputServer = ref(getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER)
const errorMsg = ref('')
const connected = ref(false)
const sessionId = ref('')
const disconnectReason = ref('')

const remoteVideo = ref<HTMLVideoElement | null>(null)
const joystickZone = ref<HTMLDivElement | null>(null)

let ws: WebSocket | null = null
let pc: RTCPeerConnection | null = null
let dc: RTCDataChannel | null = null
let joystick: nipplejs.JoystickManager | null = null
let remoteStream: MediaStream | null = null

let cursorX = 0.5
let cursorY = 0.5

function buildEnv(type: string, payload: unknown) {
  return JSON.stringify({ type, payload })
}

function sendToSignal(msg: string) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(msg)
}

function doConnect() {
  const code = inputCode.value.trim()
  if (!code || code.length !== 6) {
    errorMsg.value = '请输入有效的 6 位房间码'
    return
  }
  const addr = (inputServer.value.trim() || DEFAULT_SIGNAL_SERVER).replace(/\/+$/, '')
  errorMsg.value = ''
  connect(code, addr)
}

function connect(code: string, addr: string) {
  cleanupPeer()
  sessionId.value = code
  disconnectReason.value = ''
  const url = `${addr}/connect/phone?sid=${encodeURIComponent(code)}`
  ws = new WebSocket(url)

  ws.onopen = () => {
    connected.value = true
  }

  ws.onmessage = (evt: MessageEvent) => {
    if (typeof evt.data !== 'string') return
    try {
      onSignalMessage(JSON.parse(evt.data))
    } catch { /* ignore */ }
  }

  ws.onclose = () => {
    handleDisconnected(disconnectReason.value || '信令连接已断开')
  }

  ws.onerror = () => {
    errorMsg.value = '无法连接信令服务器，请检查地址'
  }
}

function onSignalMessage(msg: { type: string; payload?: any }) {
  switch (msg.type) {
    case 'peer_joined':
      startWebRTC()
      return
    case 'answer':
      handleAnswer(msg.payload)
      return
    case 'ice_candidate':
      if (pc && msg.payload) {
        const candidate = typeof msg.payload === 'string' ? JSON.parse(msg.payload) : msg.payload
        pc.addIceCandidate(candidate).catch((err) => console.warn('[Mobile] add ICE failed:', err))
      }
      return
    case 'peer_left':
      handleDisconnected('电脑受控端已断开')
      return
  }
}

function handleDisconnected(reason: string) {
  disconnectReason.value = reason
  errorMsg.value = reason
  connected.value = false
  connectionMode.value = 'connecting'
  cleanupPeer()
}

function startWebRTC() {
  cleanupPeer(false, false)
  const cfg: RTCConfiguration = {
    iceServers: buildIceServers(),
    iceTransportPolicy: 'all',
  }
  pc = new RTCPeerConnection(cfg)
  remoteStream = new MediaStream()
  if (remoteVideo.value) remoteVideo.value.srcObject = remoteStream

  pc.addTransceiver('video', { direction: 'recvonly' })

  pc.ontrack = (evt) => {
    for (const track of evt.streams[0]?.getTracks() || [evt.track]) {
      if (!remoteStream!.getTracks().some((item) => item.id === track.id)) {
        remoteStream!.addTrack(track)
      }
    }
    if (remoteVideo.value) {
      remoteVideo.value.srcObject = remoteStream
      remoteVideo.value.play().catch(() => {})
    }
  }

  pc.onconnectionstatechange = () => {
    const state = pc?.connectionState
    console.info('[Mobile] connection state:', state)
    if (state === 'connected') {
      connectionMode.value = 'webrtc'
    } else if (state === 'failed' || state === 'disconnected') {
      console.warn('[Mobile] WebRTC unavailable; control may use WebSocket relay')
      connectionMode.value = 'relay'
    }
  }

  pc.oniceconnectionstatechange = () => {
    console.info('[Mobile] ICE state:', pc?.iceConnectionState)
  }

  pc.onicecandidate = (evt) => {
    if (evt.candidate) {
      console.info('[Mobile] local ICE candidate:', evt.candidate.type, evt.candidate.protocol, evt.candidate.address, evt.candidate.port)
      sendToSignal(buildEnv('ice_candidate', evt.candidate.toJSON()))
    } else {
      console.info('[Mobile] ICE gathering completed')
    }
  }

  dc = pc.createDataChannel('control', { ordered: false, maxRetransmits: 0 })
  dc.onopen = () => {
    console.info('[Mobile] control DataChannel opened')
    connectionMode.value = 'webrtc'
  }
  dc.onclose = () => {
    console.warn('[Mobile] control DataChannel closed')
    connectionMode.value = 'relay'
  }

  pc.createOffer()
    .then((offer) => pc!.setLocalDescription(offer))
    .then(() => sendToSignal(buildEnv('offer', pc!.localDescription)))
    .catch(console.error)
}

function handleAnswer(desc: any) {
  if (!pc) return
  pc.setRemoteDescription(new RTCSessionDescription(desc)).catch(console.error)
}

function sendCommand(type: string, data: unknown) {
  const payload = { type, payload: data }
  if (dc?.readyState === 'open') {
    dc.send(JSON.stringify(payload))
  } else {
    sendToSignal(buildEnv('forward', { from: 'phone', payload }))
  }
}

const THROTTLE_MS = 28
const MIN_DELTA = 0.002
let lastSendTime = 0
let lastJoyX = -1
let lastJoyY = -1

async function initJoystick() {
  await nextTick()
  if (!joystickZone.value || joystick) return

  joystick = nipplejs.create({
    zone: joystickZone.value,
    mode: 'static',
    position: { left: '50%', top: '50%' },
    color: '#4f46e5',
    size: 140,
  })

  joystick.on('move', (_evt, data: nipplejs.JoystickOutputData) => {
    if (data.force < 0.04) return

    const speed = 0.0065
    const dx = Math.cos(data.angle!.radian) * data.force * speed
    const dy = -Math.sin(data.angle!.radian) * data.force * speed

    cursorX = Math.max(0, Math.min(1, cursorX + dx))
    cursorY = Math.max(0, Math.min(1, cursorY + dy))

    const now = Date.now()
    if (now - lastSendTime < THROTTLE_MS) return
    lastSendTime = now

    if (lastJoyX >= 0 && lastJoyY >= 0) {
      const ddx = Math.abs(cursorX - lastJoyX)
      const ddy = Math.abs(cursorY - lastJoyY)
      if (ddx < MIN_DELTA && ddy < MIN_DELTA) return
    }
    lastJoyX = cursorX
    lastJoyY = cursorY

    sendCommand('MOUSE_MOVE', { xRatio: cursorX, yRatio: cursorY })
  })
}

function cleanupPeer(closeWS = true, resetControls = true) {
  if (resetControls) {
    joystick?.destroy()
    joystick = null
  }
  dc?.close()
  dc = null
  pc?.close()
  pc = null
  remoteStream = null
  if (remoteVideo.value) remoteVideo.value.srcObject = null
  if (closeWS) {
    ws?.close()
    ws = null
  }
}

onMounted(() => {
  if (connected.value) initJoystick()
})

watch(connected, (value) => {
  if (value) {
    initJoystick()
  } else {
    joystick?.destroy()
    joystick = null
  }
})

onUnmounted(() => {
  cleanupPeer()
})

function clickLeft() {
  sendCommand('MOUSE_CLICK', { button: 'left', action: 'click' })
  if (navigator.vibrate) navigator.vibrate(20)
}

function clickRight() {
  sendCommand('MOUSE_CLICK', { button: 'right', action: 'click' })
  if (navigator.vibrate) navigator.vibrate(20)
}
</script>

<style scoped>
.mobile-pad {
  width: 100vw;
  min-height: 100vh;
  min-height: 100dvh;
  background: #0f172a;
  color: #e2e8f0;
  display: flex;
  flex-direction: column;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  overflow: hidden;
  user-select: none;
  -webkit-user-select: none;
}

.connect-form {
  width: 100%;
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: max(1rem, env(safe-area-inset-top)) max(1rem, env(safe-area-inset-right)) max(1rem, env(safe-area-inset-bottom)) max(1rem, env(safe-area-inset-left));
  box-sizing: border-box;
  gap: 0.85rem;
}

.connect-panel {
  width: min(100%, 390px);
  display: flex;
  flex-direction: column;
  gap: 0.65rem;
}

.connect-panel h1 {
  margin: 0 0 0.75rem;
  font-size: clamp(1.75rem, 8vw, 2.35rem);
  line-height: 1.1;
  text-align: center;
  font-weight: 750;
}

.field-label {
  font-size: 0.78rem;
  color: #94a3b8;
  line-height: 1.2;
}

.code-input,
.server-input {
  width: 100%;
  min-width: 0;
  box-sizing: border-box;
  border: 1px solid #334155;
  background: #1e293b;
  color: #e2e8f0;
  outline: none;
}

.code-input {
  height: 3.55rem;
  padding: 0 1rem;
  border-radius: 10px;
  font-size: clamp(1.15rem, 6vw, 1.55rem);
  text-align: center;
  letter-spacing: 0;
}

.code-input.has-code {
  letter-spacing: 0.38em;
  text-indent: 0.38em;
}

.server-input {
  height: 3rem;
  padding: 0 0.9rem;
  border-radius: 8px;
  font-size: 0.95rem;
}

.code-input::placeholder,
.server-input::placeholder {
  color: #94a3b8;
  opacity: 1;
  letter-spacing: 0;
  text-indent: 0;
}

.btn-connect {
  width: 100%;
  height: 3.15rem;
  margin-top: 0.35rem;
  background: #4f46e5;
  color: #fff;
  border: none;
  border-radius: 10px;
  font-size: 1.05rem;
  font-weight: 700;
}

.error {
  width: min(100%, 390px);
  margin: 0;
  color: #fca5a5;
  font-size: 0.88rem;
  text-align: center;
}

.control-area {
  display: flex;
  flex-direction: column;
  height: 100vh;
  height: 100dvh;
}

.status-bar {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  min-height: 2.25rem;
  padding: calc(0.35rem + env(safe-area-inset-top)) max(0.75rem, env(safe-area-inset-right)) 0.35rem max(0.75rem, env(safe-area-inset-left));
  box-sizing: border-box;
  background: #1e293b;
  font-size: 0.78rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.room-badge,
.direct-badge {
  color: #22c55e;
}

.relay-badge {
  color: #f59e0b;
}

.wait-badge {
  color: #60a5fa;
}

.preview-bar {
  position: relative;
  flex: 0 1 clamp(180px, 52dvh, 520px);
  min-height: 160px;
  background: #000;
  border-bottom: 1px solid #1e293b;
}

.remote-video {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.pad-row {
  flex: 1 1 auto;
  min-height: 170px;
  display: flex;
  align-items: center;
  justify-content: space-around;
  gap: clamp(1rem, 8vw, 3rem);
  padding: 0.75rem max(1rem, env(safe-area-inset-right)) max(0.75rem, env(safe-area-inset-bottom)) max(1rem, env(safe-area-inset-left));
  box-sizing: border-box;
}

.joystick-zone {
  width: clamp(135px, 38vw, 170px);
  height: clamp(135px, 38vw, 170px);
  position: relative;
  touch-action: none;
  flex: 0 0 auto;
}

.btn-group {
  display: flex;
  flex-direction: column;
  gap: 0.85rem;
  flex: 0 0 auto;
}

.touch-btn {
  width: clamp(76px, 22vw, 92px);
  height: clamp(76px, 22vw, 92px);
  border-radius: 50%;
  border: none;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.92rem;
  font-weight: 700;
  color: #fff;
  touch-action: manipulation;
}

.touch-btn:active {
  transform: scale(0.94);
}

.btn-left {
  background: #2563eb;
}

.btn-right {
  background: #dc2626;
}

@media (max-height: 680px) {
  .preview-bar {
    flex-basis: clamp(145px, 44dvh, 320px);
    min-height: 135px;
  }

  .pad-row {
    min-height: 145px;
    padding-top: 0.5rem;
    padding-bottom: max(0.5rem, env(safe-area-inset-bottom));
  }

  .joystick-zone {
    width: clamp(118px, 34vw, 150px);
    height: clamp(118px, 34vw, 150px);
  }

  .touch-btn {
    width: clamp(66px, 19vw, 82px);
    height: clamp(66px, 19vw, 82px);
  }
}
</style>
