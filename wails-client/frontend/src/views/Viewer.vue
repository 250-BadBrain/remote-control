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
      <span v-if="connectionMode === 'relay'" class="badge badge-relay">
        中继模式
      </span>
      <span v-else-if="connectionMode === 'connecting'" class="badge badge-wait">
        直连协商中...
      </span>
      <span class="badge coords">{{ coords }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { buildIceServers } from '../utils/ice'
import { handleIncomingFrame } from '../utils/frame'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

/* ---- 杩炴帴閫€鍖栨ā寮?---- */
/*
  榛勯噾閫€鍖栭『搴忥紙WebRTC 鏍囧噯淇濊瘉锛夛細
    绗?椤轰綅锛歀AN host candidate  鈥?ICE 绫诲瀷浼樺厛绾?126锛堟渶楂橈級
    绗?椤轰綅锛欼Pv6 host candidate  鈥?鍚屽睘 host 绫诲瀷锛岃嚜鍔ㄥ苟琛屾帰娴?    绗?椤轰綅锛歋TUN srflx candidate 鈥?绫诲瀷浼樺厛绾?100
    绗?椤轰綅锛歐ebSocket 鍥為€€涓户   鈥?connectionMode === 'relay'
  褰?WebRTC 杩炴帴澶辫触 / 瓒呮椂鏃讹紝鑷姩閫€鍖栧埌绗?椤轰綅銆?*/
type ConnMode = 'connecting' | 'webrtc' | 'relay'
const connectionMode = ref<ConnMode>('connecting')

const params = new URLSearchParams(window.location.hash.split('?')[1] || '')
const roomCode = params.get('code') || ''
const signalAddr = (params.get('signal') || getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER).replace(/\/+$/, '')

const canvasRef = ref<HTMLCanvasElement | null>(null)
const coords = ref('x: 0.000  y: 0.000')
const connected = ref(false)
const sessionId = ref('')
const disconnectReason = ref('')

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
  disconnectReason.value = ''

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
      handleIncomingFrame(evt.data, onVideoFrame)
      return
    }
    if (evt.data instanceof ArrayBuffer) {
      connectionMode.value = 'relay'
      handleIncomingFrame(evt.data, onVideoFrame)
      return
    }
    try {
      const msg = JSON.parse(evt.data)
      onSignalMessage(msg)
    } catch { /* ignore */ }
  }

  ws.onclose = () => {
    handleDisconnected(disconnectReason.value || '信令连接已断开')
    console.log('[Viewer] WebSocket 宸叉柇寮€')
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
    case 'peer_left':
      handleDisconnected('电脑受控端已断开')
      return
  }
}

function handleDisconnected(reason: string) {
  disconnectReason.value = reason
  connected.value = false
  connectionMode.value = 'connecting'
  if (relayTimeout) { clearTimeout(relayTimeout); relayTimeout = null }
  dc?.close()
  dc = null
  pc?.close()
  pc = null
}

/* ---- WebRTC锛堥粍閲戦€€鍖栭『浣?1-3锛?---- */
function startWebRTC() {
  const iceServers = buildIceServers()
  console.info('[Viewer] ICE servers:', iceServers.map((s) => s.urls))
  const cfg: RTCConfiguration = {
    iceServers,
    iceTransportPolicy: 'all',
  }
  pc = new RTCPeerConnection(cfg)

  /* 鐘舵€佹劅鐭ワ細鐩戝惉杩炴帴鐘舵€佸彉鍖栵紝澶辫触鏃堕€€鍖栧埌涓户妯″紡 */
  pc.onconnectionstatechange = () => {
    const state = pc!.connectionState
    console.log('[Viewer] 杩炴帴鐘舵€?', state)
    if (state === 'connected') {
      connectionMode.value = 'webrtc'
      if (relayTimeout) { clearTimeout(relayTimeout); relayTimeout = null }
    } else if (state === 'failed' || state === 'disconnected') {
      console.warn('[Viewer] WebRTC 杩炴帴澶辫触锛岄€€鍖栧埌涓户妯″紡')
      connectionMode.value = 'relay'
    }
  }

  pc.oniceconnectionstatechange = () => {
    console.info('[Viewer] ICE state:', pc?.iceConnectionState)
  }

  pc.onicecandidate = (evt) => {
    if (evt.candidate) {
      console.info('[Viewer] local ICE candidate:', evt.candidate.type, evt.candidate.protocol, evt.candidate.address, evt.candidate.port)
      sendToSignal(buildEnv('ice_candidate', evt.candidate.toJSON()))
    } else {
      console.info('[Viewer] ICE gathering completed')
    }
  }

  /* 灞忓箷甯ч€氳繃 DataChannel 浜岃繘鍒朵紶杈?*/
  dc = pc.createDataChannel('control')
  dc.binaryType = 'blob'
  dc.onopen = () => {
    console.log('[Viewer] DataChannel 宸叉墦寮€')
    connectionMode.value = 'webrtc'
  }
  dc.onclose = () => {
    console.warn('[Viewer] DataChannel 鍏抽棴锛岄€€鍖栧埌涓户')
    connectionMode.value = 'relay'
  }
  dc.onmessage = (evt: MessageEvent) => {
    handleIncomingFrame(evt.data, onVideoFrame)
  }

  /* 5 绉掕秴鏃讹細鑻?WebRTC 鏈湪姝ゆ椂闄愬唴寤虹珛杩炴帴鍒欒浆鍏ヤ腑缁?*/
  relayTimeout = setTimeout(() => {
    if (connectionMode.value === 'connecting') {
      console.warn('[Viewer] WebRTC 鎻℃墜瓒呮椂锛岄€€鍖栧埌涓户妯″紡')
      connectionMode.value = 'relay'
    }
  }, 12000)

  /* 浣滀负 phone 鍙戣捣 Offer */
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

/* ---- 鏍告煡鐐逛竴锛欳anvas 娓叉煋闃茬垎浠撻攣 ---- */
/*
  isRendering 甯冨皵閿侊細褰撲笂涓€甯ф鍦ㄨВ鐮?缁樺埗鏃讹紝鏂板抚鏃犳潯浠朵涪寮冿紝
  绂佹鍐呭瓨涓帓闃熺Н鍘?鈫?闃叉娴忚鍣ㄧ垎浠撱€?  姣忓抚缁樺埗鍚庣珛鍗?close() 閲婃斁 ImageBitmap锛屾潨缁濋暱鍛ㄦ湡鍐呭瓨娉勬紡銆?*/
let isRendering = false

async function onVideoFrame(blob: Blob) {
  if (isRendering) return
  isRendering = true
  try {
    const bitmap = await createImageBitmap(blob, { colorSpaceConversion: 'none' })
    drawFrame(bitmap)
    bitmap.close() // 缁樺埗瀹屾垚鍚庣珛鍗抽噴鏀?GPU 鍐呭瓨
  } catch { /* ignore bad frame */ }
  finally { isRendering = false }
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

  ctx.imageSmoothingEnabled = true
  ctx.imageSmoothingQuality = 'high'
  const scale = Math.min(canvas.width / bitmap.width, canvas.height / bitmap.height)
  const dx = (canvas.width - bitmap.width * scale) / 2
  const dy = (canvas.height - bitmap.height * scale) / 2
  ctx.clearRect(0, 0, canvas.width, canvas.height)
  ctx.drawImage(bitmap, dx, dy, bitmap.width * scale, bitmap.height * scale)
}

/* ---- 浼樺寲涓夛細楂橀浜嬩欢鑺傛祦 + 鏈€灏忎綅绉婚槇鍊?---- */
/* 鑺傛祦闂撮殧 45ms锛岀害 22 娆?绉掞紝骞宠　娴佺晠搴︿笌甯﹀ */
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

/* 涓婁竴娆″彂閫佺殑鍧愭爣锛岀敤浜庝綅绉婚槇鍊煎垽鏂?*/
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
  /* 閫€鍖栭『浣嶆劅鐭ワ細涓户妯″紡鎴?DC 鏈氨缁?-> WebSocket 杞彂 */
  if (connectionMode.value === 'relay' || dc?.readyState !== 'open') {
    console.debug('[Viewer] send relay command:', type)
    sendToSignal(buildEnv('forward', { from: 'phone', payload }))
  } else {
    console.debug('[Viewer] send datachannel command:', type)
    dc.send(JSON.stringify(payload))
  }
}

/* 缁忚繃鑺傛祦 + 闃堝€艰繃婊ょ殑榧犳爣绉诲姩澶勭悊 */
const onMouseMove = throttle((e: MouseEvent) => {
  const r = ratios(e)

  /* 鏈€灏忎綅绉婚槇鍊硷細鍙樺寲灏忎簬 0.5% 鏃朵涪寮冨寘 */
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

/* ---- 鐢熷懡鍛ㄦ湡 ---- */
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

