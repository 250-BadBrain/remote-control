<template>
  <div class="mobile-pad" @contextmenu.prevent>
    <!-- 连接表单 -->
    <div v-if="!connected" class="connect-form">
      <h1>远程控制</h1>
      <input v-model="inputCode" placeholder="输入 6 位房间码" maxlength="6" class="code-input" />
      <input v-model="inputServer" placeholder="信令服务器地址 (默认 wss://signal.h2seo4.win:8443)" class="server-input" />
      <button @click="doConnect" class="btn-connect">连接</button>
      <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
    </div>

    <!-- 控制面板 -->
    <div v-else class="control-area">
      <!-- 退化模式状态条 -->
      <div class="status-bar">
        <span class="room-badge">{{ sessionId }}</span>
        <span v-if="connectionMode === 'relay'" class="relay-badge">⚡ 中继模式</span>
        <span v-else-if="connectionMode === 'connecting'" class="wait-badge">⏳ 握手...</span>
        <span v-else class="direct-badge">直连</span>
      </div>

      <!-- 屏幕预览 (小窗) -->
      <div class="preview-bar">
        <canvas ref="previewCanvas" class="preview-canvas" />
      </div>

      <!-- 摇杆 + 按键 -->
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
import { ref, onMounted, onUnmounted, nextTick, watch } from 'vue'
import nipplejs from 'nipplejs'
import { buildIceServers } from '../utils/ice'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

/* ---- 退化模式 ---- */
type ConnMode = 'connecting' | 'webrtc' | 'relay'
const connectionMode = ref<ConnMode>('connecting')

/* ---- 连接参数 ---- */
const inputCode = ref('')
const inputServer = ref(getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER)
const errorMsg = ref('')
const connected = ref(false)
const sessionId = ref('')
const disconnectReason = ref('')

/* ---- DOM 引用 ---- */
const previewCanvas = ref<HTMLCanvasElement | null>(null)
const joystickZone = ref<HTMLDivElement | null>(null)

/* ---- WebRTC 相关 ---- */
let ws: WebSocket | null = null
let pc: RTCPeerConnection | null = null
let dc: RTCDataChannel | null = null
let joystick: nipplejs.JoystickManager | null = null
let relayTimeout: ReturnType<typeof setTimeout> | null = null

/* ---- 虚拟光标 (摇杆控制) ---- */
let cursorX = 0.5
let cursorY = 0.5

/* ---- 工具函数 ---- */
function buildEnv(type: string, payload: unknown) {
  return JSON.stringify({ type, payload })
}
function sendToSignal(msg: string) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(msg)
}

/* ---- 连接信令服务器 ---- */
function doConnect() {
  const code = inputCode.value.trim()
  if (!code || code.length !== 6) {
    errorMsg.value = '请输入有效的 6 位房间码'
    return
  }
  let addr = (inputServer.value.trim() || DEFAULT_SIGNAL_SERVER).replace(/\/+$/, '')
  errorMsg.value = ''
  connect(code, addr)
}

function connect(code: string, addr: string) {
  sessionId.value = code
  disconnectReason.value = ''
  const url = `${addr}/connect/phone?sid=${encodeURIComponent(code)}`
  ws = new WebSocket(url)
  ws.binaryType = 'blob'

  ws.onopen = () => {
    connected.value = true
  }

  ws.onmessage = (evt: MessageEvent) => {
    if (evt.data instanceof Blob) {
      connectionMode.value = 'relay'
      onVideoFrame(evt.data)
      return
    }
    if (evt.data instanceof ArrayBuffer) {
      connectionMode.value = 'relay'
      onVideoFrame(new Blob([evt.data], { type: 'image/jpeg' }))
      return
    }
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
    case 'offer':
      handleOffer(msg.payload)
      return
    case 'answer':
      handleAnswer(msg.payload)
      return
    case 'ice_candidate':
      if (pc && msg.payload) {
        const candidate = typeof msg.payload === 'string' ? JSON.parse(msg.payload) : msg.payload
        pc.addIceCandidate(candidate).catch(() => {})
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
  if (relayTimeout) { clearTimeout(relayTimeout); relayTimeout = null }
  joystick?.destroy()
  joystick = null
  dc?.close()
  dc = null
  pc?.close()
  pc = null
}

function onIncomingFrame(data: unknown) {
  if (data instanceof Blob) {
    onVideoFrame(data)
    return
  }
  if (data instanceof ArrayBuffer) {
    onVideoFrame(new Blob([data], { type: 'image/jpeg' }))
  }
}

/* ---- WebRTC (phone 端，黄金退化顺位 1-3) ---- */
function startWebRTC() {
  const cfg: RTCConfiguration = {
    iceServers: buildIceServers(),
    iceTransportPolicy: 'all',
  }
  pc = new RTCPeerConnection(cfg)

  pc.onconnectionstatechange = () => {
    const state = pc!.connectionState
    console.log('[Mobile] 连接状态:', state)
    if (state === 'connected' || state === 'completed') {
      connectionMode.value = 'webrtc'
      if (relayTimeout) { clearTimeout(relayTimeout); relayTimeout = null }
    } else if (state === 'failed' || state === 'disconnected') {
      console.warn('[Mobile] WebRTC 连接失败，退化到中继')
      connectionMode.value = 'relay'
    }
  }

  pc.onicecandidate = (evt) => {
    if (evt.candidate) {
      sendToSignal(buildEnv('ice_candidate', evt.candidate.toJSON()))
    }
  }

  dc = pc.createDataChannel('control')
  dc.binaryType = 'blob'
  dc.onopen = () => {
    console.log('[Mobile] DataChannel 已打开')
    connectionMode.value = 'webrtc'
  }
  dc.onclose = () => {
    console.warn('[Mobile] DataChannel 关闭，退化到中继')
    connectionMode.value = 'relay'
  }
  dc.onmessage = (evt: MessageEvent) => {
    onIncomingFrame(evt.data)
  }

  /* 5 秒握手超时 -> 中继 */
  relayTimeout = setTimeout(() => {
    if (connectionMode.value === 'connecting') {
      console.warn('[Mobile] WebRTC 握手超时，退化到中继')
      connectionMode.value = 'relay'
    }
  }, 12000)

  pc.createOffer()
    .then((offer) => pc!.setLocalDescription(offer))
    .then(() => sendToSignal(buildEnv('offer', pc!.localDescription)))
    .catch(console.error)
}

function handleOffer(desc: any) {
  if (!pc) return
  pc.setRemoteDescription(new RTCSessionDescription(desc))
  pc.createAnswer()
    .then((answer) => pc!.setLocalDescription(answer))
    .then(() => sendToSignal(buildEnv('answer', pc!.localDescription)))
    .catch(console.error)
}

function handleAnswer(desc: any) {
  if (!pc) return
  pc.setRemoteDescription(new RTCSessionDescription(desc)).catch(console.error)
}

/* ---- 核查点一：Canvas 渲染防爆仓锁 ---- */
let isRendering = false

async function onVideoFrame(blob: Blob) {
  if (isRendering) return
  isRendering = true
  try {
    const bitmap = await createImageBitmap(blob, { colorSpaceConversion: 'none' })
    try {
      const canvas = previewCanvas.value
      if (!canvas) { bitmap.close(); return }
      const ctx = canvas.getContext('2d')
      if (!ctx) { bitmap.close(); return }

      if (canvas.width !== canvas.clientWidth || canvas.height !== canvas.clientHeight) {
        canvas.width = canvas.clientWidth
        canvas.height = canvas.clientHeight
      }
      ctx.imageSmoothingEnabled = true
      ctx.imageSmoothingQuality = 'high'
      const scale = Math.min(canvas.width / bitmap.width, canvas.height / bitmap.height)
      const dx = (canvas.width - bitmap.width * scale) / 2
      const dy = (canvas.height - bitmap.height * scale) / 2
      ctx.clearRect(0, 0, canvas.width, canvas.height)
      ctx.drawImage(bitmap, dx, dy, bitmap.width * scale, bitmap.height * scale)
      bitmap.close()
    } catch { bitmap.close() }
  } catch { /* createImageBitmap 失败，无需释放 */ }
  finally { isRendering = false }
}

/* ---- 发送控制指令（退化感知） ---- */
function sendCommand(type: string, data: unknown) {
  const payload = { type, payload: data }
  if (connectionMode.value === 'relay' || dc?.readyState !== 'open') {
    sendToSignal(buildEnv('forward', { from: 'phone', payload }))
  } else {
    dc.send(JSON.stringify(payload))
  }
}

/* ---- 优化三：摇杆节流 + 最小位移阈值 ---- */
/* 摇杆事件频率极高（每帧触发），必须节流 */
const THROTTLE_MS = 50
const MIN_DELTA = 0.005
let lastSendTime = 0
let lastJoyX = -1
let lastJoyY = -1

/* ---- 摇杆逻辑 (nipplejs) ---- */
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
    if (data.force < 0.05) return

    const speed = 0.008
    const dx = Math.cos(data.angle!.radian) * data.force * speed
    const dy = -Math.sin(data.angle!.radian) * data.force * speed

    cursorX = Math.max(0, Math.min(1, cursorX + dx))
    cursorY = Math.max(0, Math.min(1, cursorY + dy))

    /* 节流：每 THROTTLE_MS ms 最多发送一次 */
    const now = Date.now()
    if (now - lastSendTime < THROTTLE_MS) return
    lastSendTime = now

    /* 最小位移阈值 */
    if (lastJoyX >= 0 && lastJoyY >= 0) {
      const ddx = Math.abs(cursorX - lastJoyX)
      const ddy = Math.abs(cursorY - lastJoyY)
      if (ddx < MIN_DELTA && ddy < MIN_DELTA) return
    }
    lastJoyX = cursorX
    lastJoyY = cursorY

    sendCommand('MOUSE_MOVE', { xRatio: cursorX, yRatio: cursorY })
  })

  joystick.on('end', () => {
    /* 摇杆归中时不做操作 */
  })
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
  if (relayTimeout) clearTimeout(relayTimeout)
  joystick?.destroy()
  dc?.close()
  pc?.close()
  ws?.close()
})

/* ---- 触摸按键 ---- */
function clickLeft() {
  sendCommand('MOUSE_CLICK', { button: 'left', action: 'click' })
  /* 触发触觉反馈 */
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
  height: 100vh;
  background: #0f172a;
  color: #e2e8f0;
  display: flex;
  flex-direction: column;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  overflow: hidden;
  user-select: none;
  -webkit-user-select: none;
}

/* ---- 连接表单 ---- */
.connect-form {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  padding: 2rem;
  gap: 1rem;
}
.connect-form h1 {
  font-size: 1.8rem;
  margin-bottom: 1rem;
}
.code-input {
  width: 200px;
  padding: 0.75rem 1rem;
  font-size: 1.5rem;
  text-align: center;
  letter-spacing: 0.5em;
  border: 2px solid #334155;
  border-radius: 8px;
  background: #1e293b;
  color: #e2e8f0;
  outline: none;
}
.code-input:focus {
  border-color: #4f46e5;
}
.server-input {
  width: 90%;
  max-width: 320px;
  padding: 0.6rem 1rem;
  font-size: 0.85rem;
  border: 1px solid #334155;
  border-radius: 6px;
  background: #1e293b;
  color: #94a3b8;
  outline: none;
  text-align: center;
}
.btn-connect {
  padding: 0.75rem 3rem;
  background: #4f46e5;
  color: #fff;
  border: none;
  border-radius: 8px;
  font-size: 1.1rem;
  cursor: pointer;
}
.btn-connect:active {
  background: #4338ca;
}
.error {
  color: #ef4444;
  font-size: 0.85rem;
}

/* ---- 控制面板 ---- */
.control-area {
  display: flex;
  flex-direction: column;
  height: 100%;
}

/* 状态条 */
.status-bar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.35rem 0.75rem;
  background: #1e293b;
  font-size: 0.75rem;
  font-family: monospace;
}
.room-badge {
  color: #22c55e;
}
.relay-badge {
  color: #f59e0b;
  animation: pulse 1.5s ease-in-out infinite;
}
.wait-badge {
  color: #3b82f6;
}
.direct-badge {
  color: #22c55e;
}
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.6; }
}

/* 预览条 */
.preview-bar {
  position: relative;
  height: clamp(220px, 52vh, 520px);
  min-height: 220px;
  background: #000;
  border-bottom: 2px solid #1e293b;
}
.preview-canvas {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

/* 摇杆 + 按键行 */
.pad-row {
  flex: 1;
  min-height: 190px;
  display: flex;
  align-items: center;
  justify-content: space-around;
  padding: 0.5rem;
}

@media (max-height: 680px) {
  .preview-bar {
    height: 44vh;
    min-height: 160px;
  }

  .pad-row {
    min-height: 170px;
  }
}

/* 摇杆容器 */
.joystick-zone {
  width: 160px;
  height: 160px;
  position: relative;
  touch-action: none;
}

/* 按键组 */
.btn-group {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.touch-btn {
  width: 90px;
  height: 90px;
  border-radius: 50%;
  border: none;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.9rem;
  font-weight: 600;
  color: #fff;
  cursor: pointer;
  touch-action: manipulation;
}
.touch-btn:active {
  transform: scale(0.92);
}
.btn-left {
  background: linear-gradient(135deg, #3b82f6, #2563eb);
  box-shadow: 0 4px 12px rgba(59,130,246,0.4);
}
.btn-right {
  background: linear-gradient(135deg, #ef4444, #dc2626);
  box-shadow: 0 4px 12px rgba(239,68,68,0.4);
}
</style>
