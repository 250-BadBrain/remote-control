<template>
  <div class="viewer" @contextmenu.prevent>
    <video
      ref="videoRef"
      class="remote-video"
      autoplay
      playsinline
      muted
      tabindex="0"
      @mousemove="onMouseMove"
      @mousedown="onMouseDown"
      @mouseup="onMouseUp"
      @wheel.prevent="onScroll"
    />
    <div class="toolbar">
      <button class="back-btn" @click="router.push('/')">返回</button>
      <span class="badge" :class="connected ? 'status-ok' : 'status-err'">
        {{ connected ? `已连接 ${sessionId}` : '未连接' }}
      </span>
      <span v-if="connectionMode === 'relay'" class="badge badge-relay">中继控制</span>
      <span v-else-if="connectionMode === 'connecting'" class="badge badge-wait">直连协商中...</span>
      <span v-else class="badge status-ok">视频直连</span>
      <span class="badge coords">{{ coords }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { buildIceServers } from '../utils/ice'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

type ConnMode = 'connecting' | 'webrtc' | 'relay'

const router = useRouter()
const route = useRoute()
const connectionMode = ref<ConnMode>('connecting')

const roomCode = String(route.query.code || '')
const signalAddr = String(route.query.signal || getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER).replace(/\/+$/, '')

const videoRef = ref<HTMLVideoElement | null>(null)
const coords = ref('x: 0.000  y: 0.000')
const connected = ref(false)
const sessionId = ref('')
const disconnectReason = ref('')

let ws: WebSocket | null = null
let pc: RTCPeerConnection | null = null
let dc: RTCDataChannel | null = null
let remoteStream: MediaStream | null = null

function buildEnv(type: string, payload: unknown) {
  return JSON.stringify({ type, payload })
}

function sendToSignal(msg: string) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(msg)
}

function connect() {
  if (!roomCode) {
    router.push('/')
    return
  }

  sessionId.value = roomCode
  disconnectReason.value = ''

  ws = new WebSocket(`${signalAddr}/connect/phone?sid=${encodeURIComponent(roomCode)}`)

  ws.onopen = () => {
    connected.value = true
  }

  ws.onmessage = (evt: MessageEvent) => {
    if (typeof evt.data !== 'string') return
    try {
      onSignalMessage(JSON.parse(evt.data))
    } catch {
      /* ignore */
    }
  }

  ws.onclose = () => {
    handleDisconnected(disconnectReason.value || '信令连接已断开')
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
    case 'answer':
      handleAnswer(msg.payload)
      return
    case 'ice_candidate':
      if (pc && msg.payload) {
        const candidate = typeof msg.payload === 'string' ? JSON.parse(msg.payload) : msg.payload
        pc.addIceCandidate(candidate).catch((err) => console.warn('[Viewer] add ICE failed:', err))
      }
      return
    case 'peer_left':
      handleDisconnected('受控端已断开')
      return
  }
}

function handleDisconnected(reason: string) {
  disconnectReason.value = reason
  connected.value = false
  connectionMode.value = 'connecting'
  cleanupPeer(false)
}

function startWebRTC() {
  cleanupPeer(false)
  pc = new RTCPeerConnection({
    iceServers: buildIceServers(),
    iceTransportPolicy: 'all',
  })
  remoteStream = new MediaStream()
  if (videoRef.value) videoRef.value.srcObject = remoteStream

  pc.addTransceiver('video', { direction: 'recvonly' })

  pc.ontrack = (evt) => {
    for (const track of evt.streams[0]?.getTracks() || [evt.track]) {
      if (!remoteStream!.getTracks().some((item) => item.id === track.id)) {
        remoteStream!.addTrack(track)
      }
    }
    if (videoRef.value) {
      videoRef.value.srcObject = remoteStream
      videoRef.value.play().catch(() => {})
    }
  }

  pc.onconnectionstatechange = () => {
    const state = pc?.connectionState
    if (state === 'connected') {
      connectionMode.value = 'webrtc'
    } else if (state === 'failed' || state === 'disconnected') {
      connectionMode.value = 'relay'
    }
  }

  pc.onicecandidate = (evt) => {
    if (evt.candidate) {
      sendToSignal(buildEnv('ice_candidate', evt.candidate.toJSON()))
    }
  }

  dc = pc.createDataChannel('control', { ordered: false, maxRetransmits: 0 })
  dc.onopen = () => {
    connectionMode.value = 'webrtc'
  }
  dc.onclose = () => {
    connectionMode.value = 'relay'
  }

  pc.createOffer()
    .then((offer) => pc!.setLocalDescription(offer))
    .then(() => sendToSignal(buildEnv('offer', {
      description: pc!.localDescription,
      profile: 'desktop',
    })))
    .catch((err) => console.warn('[Viewer] create offer failed:', err))
}

function handleAnswer(desc: any) {
  if (!pc) return
  pc.setRemoteDescription(new RTCSessionDescription(desc)).catch((err) => console.warn('[Viewer] set remote description failed:', err))
}

const THROTTLE_MS = 28
const MIN_DELTA = 0.002

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

let lastSentX = -1
let lastSentY = -1

function ratios(e: MouseEvent) {
  const el = videoRef.value
  if (!el) return { xRatio: 0, yRatio: 0 }
  const rect = el.getBoundingClientRect()

  const videoW = el.videoWidth || rect.width
  const videoH = el.videoHeight || rect.height
  const videoAspect = videoW / videoH
  const boxAspect = rect.width / rect.height

  let contentW = rect.width
  let contentH = rect.height
  let offsetX = 0
  let offsetY = 0

  if (boxAspect > videoAspect) {
    contentW = rect.height * videoAspect
    offsetX = (rect.width - contentW) / 2
  } else if (boxAspect < videoAspect) {
    contentH = rect.width / videoAspect
    offsetY = (rect.height - contentH) / 2
  }

  const x = (e.clientX - rect.left - offsetX) / contentW
  const y = (e.clientY - rect.top - offsetY) / contentH

  return {
    xRatio: Math.max(0, Math.min(1, x)),
    yRatio: Math.max(0, Math.min(1, y)),
  }
}

function sendCommand(type: string, data: unknown) {
  const payload = { type, payload: data }
  if (dc?.readyState === 'open') {
    dc.send(JSON.stringify(payload))
  } else {
    sendToSignal(buildEnv('forward', { from: 'phone', payload }))
  }
}

const onMouseMove = throttle((e: MouseEvent) => {
  const r = ratios(e)
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
  videoRef.value?.focus()
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

function cleanupPeer(closeWS = true) {
  dc?.close()
  dc = null
  pc?.close()
  pc = null
  remoteStream = null
  if (videoRef.value) videoRef.value.srcObject = null
  if (closeWS) {
    ws?.close()
    ws = null
  }
}

onMounted(() => {
  connect()
})

onUnmounted(() => {
  cleanupPeer()
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
.remote-video {
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
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 1rem;
  background: rgba(0, 0, 0, 0.6);
  color: #fff;
  font-size: 0.8rem;
  font-family: monospace;
}
.back-btn {
  border: 0;
  border-radius: 3px;
  padding: 0.15rem 0.5rem;
  background: #334155;
  color: #fff;
  cursor: pointer;
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
}
.badge-wait {
  background: #3b82f6;
  color: #fff;
}
.coords {
  background: transparent;
  color: #ccc;
}
</style>
