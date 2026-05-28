<template>
  <div class="viewer" @contextmenu.prevent>
    <canvas ref="canvasRef" class="remote-canvas"
      @mousemove="onMouseMove"
      @mousedown="onMouseDown"
      @mouseup="onMouseUp"
      @wheel.prevent="onScroll"
    />
    <div class="toolbar">
      <span class="badge" :class="connected ? 'status-ok' : 'status-err'">
        {{ connected ? `已连接 ${sessionId}` : '未连接' }}
      </span>
      <!-- 中继指示器 -->
      <span v-if="connectionMode === 'relay'" class="badge badge-relay">
        ⚡ 中继模式
      </span>
      <span v-else-if="connectionMode === 'connecting'" class="badge badge-wait">
        ⏳ 直连协商中...
      </span>
      <span class="badge coords">{{ coords }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

/* ---- 连接退化模式 ---- */
/*
  黄金退化顺序（WebRTC 标准保证）：
    第1顺位：LAN host candidate  — ICE 类型优先级 126（最高）
    第2顺位：IPv6 host candidate  — 同属 host 类型，自动并行探测
    第3顺位：STUN srflx candidate — 类型优先级 100
    第4顺位：WebSocket 回退中继   — connectionMode === 'relay'
  当 WebRTC 连接失败 / 超时时，自动退化到第4顺位。
*/
type ConnMode = 'connecting' | 'webrtc' | 'relay'
const connectionMode = ref<ConnMode>('connecting')

const params = new URLSearchParams(window.location.hash.split('?')[1] || '')
const roomCode = params.get('code') || ''
const signalAddr = (params.get('signal') || getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER).replace(/\/+$/, '')

const canvasRef = ref<HTMLCanvasElement | null>(null)
const coords = ref('x: 0.000  y: 0.000')
const connected = ref(false)
const sessionId = ref('')

let ws: WebSocket | null = null
let pc: RTCPeerConnection | null = null
let dc: RTCDataChannel | null = null
let relayTimeout: ReturnType<typeof setTimeout> | null = null

/* ---- signalling + WebRTC ---- */
function buildEnv(type: string, payload: unknown) {
  return JSON.stringify({ type, payload })
}

function sendToSignal(msg: string) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(msg)
}

function connect() {
  const sid = roomCode || `viewer_${Math.random().toString(36).slice(2, 8)}`
  sessionId.value = sid

  const url = `${signalAddr}/connect/phone?sid=${encodeURIComponent(sid)}`
  ws = new WebSocket(url)
  ws.binaryType = 'blob'

  ws.onopen = () => {
    connected.value = true
    console.log('[Viewer] WebSocket 已连接')
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
      const msg = JSON.parse(evt.data)
      onSignalMessage(msg)
    } catch { /* ignore */ }
  }

  ws.onclose = () => {
    connected.value = false
    console.log('[Viewer] WebSocket 已断开')
  }
}

function onSignalMessage(msg: { type: string; payload?: any }) {
  switch (msg.type) {
    case 'session_assigned':
      sessionId.value = msg.payload
      return

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
  }
}

/* ---- WebRTC（黄金退化顺位 1-3） ---- */
function startWebRTC() {
  const cfg: RTCConfiguration = {
    iceServers: [{ urls: 'stun:stun.l.google.com:19302' }],
  }
  pc = new RTCPeerConnection(cfg)

  /* 状态感知：监听连接状态变化，失败时退化到中继模式 */
  pc.onconnectionstatechange = () => {
    const state = pc!.connectionState
    console.log('[Viewer] 连接状态:', state)
    if (state === 'connected') {
      connectionMode.value = 'webrtc'
      if (relayTimeout) { clearTimeout(relayTimeout); relayTimeout = null }
    } else if (state === 'failed' || state === 'disconnected') {
      console.warn('[Viewer] WebRTC 连接失败，退化到中继模式')
      connectionMode.value = 'relay'
    }
  }

  pc.onicecandidate = (evt) => {
    if (evt.candidate) {
      sendToSignal(buildEnv('ice_candidate', evt.candidate.toJSON()))
    }
  }

  /* 屏幕帧通过 DataChannel 二进制传输 */
  dc = pc.createDataChannel('control')
  dc.binaryType = 'blob'
  dc.onopen = () => {
    console.log('[Viewer] DataChannel 已打开')
    connectionMode.value = 'webrtc'
  }
  dc.onclose = () => {
    console.warn('[Viewer] DataChannel 关闭，退化到中继')
    connectionMode.value = 'relay'
  }
  dc.onmessage = (evt: MessageEvent) => {
    if (evt.data instanceof Blob) {
      onVideoFrame(evt.data)
    }
  }

  /* 5 秒超时：若 WebRTC 未在此时限内建立连接则转入中继 */
  relayTimeout = setTimeout(() => {
    if (connectionMode.value === 'connecting') {
      console.warn('[Viewer] WebRTC 握手超时，退化到中继模式')
      connectionMode.value = 'relay'
    }
  }, 5000)

  /* 作为 phone 发起 Offer */
  pc.createOffer()
    .then((offer) => pc!.setLocalDescription(offer))
    .then(() => {
      sendToSignal(buildEnv('offer', pc!.localDescription))
    })
    .catch(console.error)
}

function handleOffer(desc: any) {
  if (!pc) return
  pc.setRemoteDescription(new RTCSessionDescription(desc))
  pc.createAnswer()
    .then((answer) => pc!.setLocalDescription(answer))
    .then(() => {
      sendToSignal(buildEnv('answer', pc!.localDescription))
    })
    .catch(console.error)
}

function handleAnswer(desc: any) {
  if (!pc) return
  pc.setRemoteDescription(new RTCSessionDescription(desc)).catch(console.error)
}

/* ---- 核查点一：Canvas 渲染防爆仓锁 ---- */
/*
  isRendering 布尔锁：当上一帧正在解码/绘制时，新帧无条件丢弃，
  禁止内存中排队积压 → 防止浏览器爆仓。
  每帧绘制后立即 close() 释放 ImageBitmap，杜绝长周期内存泄漏。
*/
let isRendering = false

async function onVideoFrame(blob: Blob) {
  if (isRendering) return
  isRendering = true
  try {
    const bitmap = await createImageBitmap(blob, { colorSpaceConversion: 'none' })
    drawFrame(bitmap)
    bitmap.close() // 绘制完成后立即释放 GPU 内存
  } catch { /* 坏帧丢弃 */ }
  finally { isRendering = false } // 确保锁不会永久卡死
}

function drawFrame(bitmap: ImageBitmap) {
  const canvas = canvasRef.value
  if (!canvas) return
  const ctx = canvas.getContext('2d')
  if (!ctx) return

  if (canvas.width !== canvas.clientWidth || canvas.height !== canvas.clientHeight) {
    canvas.width = canvas.clientWidth
    canvas.height = canvas.clientHeight
  }

  const scale = Math.min(canvas.width / bitmap.width, canvas.height / bitmap.height)
  const dx = (canvas.width - bitmap.width * scale) / 2
  const dy = (canvas.height - bitmap.height * scale) / 2
  ctx.clearRect(0, 0, canvas.width, canvas.height)
  ctx.drawImage(bitmap, dx, dy, bitmap.width * scale, bitmap.height * scale)
}

/* ---- 优化三：高频事件节流 + 最小位移阈值 ---- */
/* 节流间隔 45ms，约 22 次/秒，平衡流畅度与带宽 */
const THROTTLE_MS = 45
const MIN_DELTA = 0.005

function throttle<T extends (...args: any[]) => void>(fn: T, ms: number): T {
  let last = 0
  let timer: ReturnType<typeof setTimeout> | null = null
  let lastArgs: any[] | null = null

  const invoke = () => {
    last = Date.now()
    timer = null
    if (lastArgs) fn(...lastArgs)
    lastArgs = null
  }

  return ((...args: any[]) => {
    lastArgs = args
    const now = Date.now()
    const elapsed = now - last
    if (elapsed >= ms) {
      if (timer) { clearTimeout(timer); timer = null }
      invoke()
    } else if (!timer) {
      timer = setTimeout(invoke, ms - elapsed)
    }
  }) as T
}

/* 上一次发送的坐标，用于位移阈值判断 */
let lastSentX = -1
let lastSentY = -1

function ratios(e: MouseEvent) {
  const el = canvasRef.value
  if (!el) return { xRatio: 0, yRatio: 0 }
  const rect = el.getBoundingClientRect()
  return {
    xRatio: (e.clientX - rect.left) / rect.width,
    yRatio: (e.clientY - rect.top) / rect.height,
  }
}

function sendCommand(type: string, data: unknown) {
  const payload = { type, payload: data }
  /* 退化顺位感知：中继模式或 DC 未就绪 -> WebSocket 转发 */
  if (connectionMode.value === 'relay' || dc?.readyState !== 'open') {
    sendToSignal(buildEnv('forward', { from: 'phone', payload }))
  } else {
    dc.send(JSON.stringify(payload))
  }
}

/* 经过节流 + 阈值过滤的鼠标移动处理 */
const onMouseMove = throttle((e: MouseEvent) => {
  const r = ratios(e)

  /* 最小位移阈值：变化小于 0.5% 时丢弃包 */
  if (lastSentX >= 0 && lastSentY >= 0) {
    const dx = Math.abs(r.xRatio - lastSentX)
    const dy = Math.abs(r.yRatio - lastSentY)
    if (dx < MIN_DELTA && dy < MIN_DELTA) return
  }

  lastSentX = r.xRatio
  lastSentY = r.yRatio
  coords.value = `x: ${r.xRatio.toFixed(3)}  y: ${r.yRatio.toFixed(3)}`
  sendCommand('MOUSE_MOVE', r)
}, THROTTLE_MS)

function onMouseDown(e: MouseEvent) {
  const btn = e.button === 0 ? 'left' : e.button === 2 ? 'right' : 'middle'
  sendCommand('MOUSE_CLICK', { button: btn, action: 'down' })
}

function onMouseUp(e: MouseEvent) {
  const btn = e.button === 0 ? 'left' : e.button === 2 ? 'right' : 'middle'
  sendCommand('MOUSE_CLICK', { button: btn, action: 'up' })
}

function onScroll(e: WheelEvent) {
  sendCommand('SCROLL', { deltaY: e.deltaY })
}

/* ---- 生命周期 ---- */
onMounted(() => {
  if (roomCode) connect()
})

onUnmounted(() => {
  if (relayTimeout) clearTimeout(relayTimeout)
  dc?.close()
  pc?.close()
  ws?.close()
})
</script>

<style scoped>
.viewer {
  position: relative;
  width: 100vw;
  height: 100vh;
  background: #000;
  overflow: hidden;
  display: flex;
  align-items: center;
  justify-content: center;
}
.remote-canvas {
  display: block;
  width: 100%;
  height: 100%;
  cursor: crosshair;
  object-fit: contain;
}
.toolbar {
  position: absolute;
  bottom: 0;
  left: 0;
  right: 0;
  display: flex;
  gap: 0.75rem;
  padding: 0.5rem 1rem;
  background: rgba(0, 0, 0, 0.6);
  color: #fff;
  font-size: 0.8rem;
  font-family: monospace;
}
.badge {
  padding: 0.15rem 0.5rem;
  border-radius: 3px;
}
.status-ok {
  background: #22c55e;
  color: #fff;
}
.status-err {
  background: #ef4444;
  color: #fff;
}
.badge-relay {
  background: #f59e0b;
  color: #000;
  animation: pulse 1.5s ease-in-out infinite;
}
.badge-wait {
  background: #3b82f6;
  color: #fff;
}
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.6; }
}
.coords {
  background: transparent;
  color: #ccc;
}
</style>
