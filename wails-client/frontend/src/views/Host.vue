<template>
  <div class="host">
    <header class="header">
      <div>
        <button class="btn-link" @click="router.push('/')">返回</button>
        <h1>控制此电脑</h1>
      </div>
      <span class="status" :class="connected ? 'online' : 'offline'">
        {{ connected ? '已连接信令' : '未连接' }}
      </span>
    </header>

    <section class="card">
      <label class="label-sm">信令服务器</label>
      <input v-model="serverAddr" class="input-server" placeholder="wss://signal.h2seo4.win:8443" />
    </section>

    <section class="card">
      <h2>我的房间码</h2>
      <div class="code-box">
        <code>{{ sessionId || '---' }}</code>
        <button class="btn-copy" @click="copyCode" :disabled="!sessionId">复制</button>
      </div>
      <p class="hint">手机使用摇杆控制，另一台电脑使用鼠标直接映射控制。</p>
    </section>

    <section class="card">
      <h2>屏幕共享</h2>
      <video ref="previewRef" class="preview" autoplay muted playsinline />
      <p class="hint">{{ streamStatus }}</p>
    </section>

    <section class="card">
      <h2>控制端设备</h2>
      <ul class="device-list" v-if="devices.length">
        <li v-for="d in devices" :key="d.id">{{ d.name }} - {{ d.status }}</li>
      </ul>
      <p v-else class="empty">等待设备接入中...</p>
      <p v-if="disconnectNotice" class="notice">{{ disconnectNotice }}</p>
    </section>

    <button class="btn-primary" @click="startSession" :disabled="connecting">
      {{ connecting ? '连接中...' : connected ? '重新启动会话' : '启动会话' }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { buildIceServers } from '../utils/ice'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

type Envelope = {
  type: string
  payload?: any
}

type ControllerProfile = 'mobile' | 'desktop'

type VideoProfile = {
  label: string
  maxWidth: number
  maxHeight: number
  maxBitrate: number
  maxFramerate: number
}

type OfferPayload = RTCSessionDescriptionInit | {
  description: RTCSessionDescriptionInit
  profile?: ControllerProfile
}

const VIDEO_PROFILES: Record<ControllerProfile, VideoProfile> = {
  mobile: {
    label: 'mobile 720p30 2.5Mbps',
    maxWidth: 1280,
    maxHeight: 720,
    maxBitrate: 2_500_000,
    maxFramerate: 30,
  },
  desktop: {
    label: 'desktop 1080p30 6Mbps',
    maxWidth: 1920,
    maxHeight: 1080,
    maxBitrate: 6_000_000,
    maxFramerate: 30,
  },
}

const router = useRouter()
const go = (window as any).go
const app = go?.main?.App

const serverAddr = ref(getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER)
const sessionId = ref('')
const connected = ref(false)
const connecting = ref(false)
const peerReady = ref(false)
const disconnectNotice = ref('')
const streamStatus = ref('启动会话后会请求选择要共享的屏幕')
const previewRef = ref<HTMLVideoElement | null>(null)

let ws: WebSocket | null = null
let pc: RTCPeerConnection | null = null
let dc: RTCDataChannel | null = null
let screenStream: MediaStream | null = null

const devices = computed(() => peerReady.value
  ? [{ id: 'controller', name: '控制端', status: '已接入' }]
  : [])

function buildEnv(type: string, payload: unknown) {
  return JSON.stringify({ type, payload })
}

function sendToSignal(msg: string) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(msg)
}

function copyCode() {
  navigator.clipboard?.writeText(sessionId.value)
}

async function startSession() {
  if (connecting.value) return
  connecting.value = true
  disconnectNotice.value = ''

  await cleanup()

  try {
    await ensureScreenStream()
    const addr = (serverAddr.value.trim() || DEFAULT_SIGNAL_SERVER).replace(/\/+$/, '')
    ws = new WebSocket(`${addr}/connect/computer`)

    ws.onopen = () => {
      connected.value = true
      streamStatus.value = '信令已连接，等待控制端接入'
    }

    ws.onmessage = (evt) => {
      if (typeof evt.data !== 'string') return
      try {
        onSignalMessage(JSON.parse(evt.data))
      } catch {
        /* ignore */
      }
    }

    ws.onclose = () => {
      connected.value = false
      peerReady.value = false
      disconnectNotice.value = '信令连接已断开'
    }

    ws.onerror = () => {
      disconnectNotice.value = '无法连接信令服务器'
    }
  } catch (err) {
    console.warn('[Host] start failed:', err)
    disconnectNotice.value = err instanceof Error ? err.message : '启动失败'
    await cleanup()
  } finally {
    connecting.value = false
  }
}

async function ensureScreenStream() {
  if (screenStream) return
  if (!navigator.mediaDevices?.getDisplayMedia) {
    throw new Error('当前 WebView 不支持屏幕视频采集')
  }

  screenStream = await navigator.mediaDevices.getDisplayMedia({
    video: {
      frameRate: { ideal: 30, max: 30 },
      width: { ideal: 1920 },
      height: { ideal: 1080 },
    },
    audio: false,
  })

  const track = screenStream.getVideoTracks()[0]
  track.contentHint = 'detail'
  track.onended = () => {
    streamStatus.value = '屏幕共享已停止'
    cleanup()
  }

  if (previewRef.value) {
    previewRef.value.srcObject = screenStream
  }
  streamStatus.value = '屏幕视频流已准备好'
}

async function getLocalIceServers() {
  if (app?.GetFrontendICEServers) {
    const servers = await app.GetFrontendICEServers()
    return servers.map((server: { urls: string[]; username?: string; credential?: string }) => ({
      urls: server.urls,
      username: server.username,
      credential: server.credential,
    }))
  }
  return buildIceServers()
}

function unwrapOfferPayload(payload: OfferPayload): { description: RTCSessionDescriptionInit; profile: ControllerProfile } {
  if ('description' in payload) {
    return {
      description: payload.description,
      profile: payload.profile === 'desktop' ? 'desktop' : 'mobile',
    }
  }
  return { description: payload, profile: 'mobile' }
}

async function applyVideoProfile(sender: RTCRtpSender, profileName: ControllerProfile) {
  const profile = VIDEO_PROFILES[profileName]
  const track = sender.track
  const settings = track?.getSettings() || {}
  const sourceWidth = settings.width || profile.maxWidth
  const sourceHeight = settings.height || profile.maxHeight
  const scaleResolutionDownBy = Math.max(
    1,
    sourceWidth / profile.maxWidth,
    sourceHeight / profile.maxHeight,
  )

  const params = sender.getParameters()
  params.encodings = [{
    ...(params.encodings?.[0] || {}),
    maxBitrate: profile.maxBitrate,
    maxFramerate: profile.maxFramerate,
    scaleResolutionDownBy,
  }]
  ;(params as any).degradationPreference = 'balanced'

  try {
    await sender.setParameters(params)
  } catch (err) {
    console.warn('[Host] set video sender parameters failed:', err)
  }
}

async function createPeerConnection(profileName: ControllerProfile) {
  pc?.close()
  dc = null

  pc = new RTCPeerConnection({
    iceServers: await getLocalIceServers(),
    iceTransportPolicy: 'all',
  })

  pc.onicecandidate = (evt) => {
    if (evt.candidate) sendToSignal(buildEnv('ice_candidate', evt.candidate.toJSON()))
  }

  pc.onconnectionstatechange = () => {
    const state = pc?.connectionState
    if (state === 'connected') {
      peerReady.value = true
      disconnectNotice.value = ''
      streamStatus.value = '视频直连已建立'
    } else if (state === 'failed' || state === 'disconnected' || state === 'closed') {
      peerReady.value = false
      if (connected.value) disconnectNotice.value = '控制端连接已断开'
    }
  }

  pc.ondatachannel = (evt) => {
    dc = evt.channel
    dc.onopen = () => {
      peerReady.value = true
    }
    dc.onclose = () => {
      peerReady.value = false
      console.warn('[Host] control DataChannel closed')
    }
    dc.onmessage = (evt) => {
      if (typeof evt.data === 'string') {
        app?.ExecuteCommand?.(evt.data)
      }
    }
  }

  for (const track of screenStream?.getVideoTracks() || []) {
    const sender = pc.addTrack(track, screenStream!)
    await applyVideoProfile(sender, profileName)
  }
}

async function onSignalMessage(msg: Envelope) {
  switch (msg.type) {
    case 'session_assigned':
      sessionId.value = msg.payload
      return

    case 'peer_joined':
      streamStatus.value = '控制端已接入，等待 WebRTC offer'
      return

    case 'offer':
      await handleOffer(msg.payload)
      return

    case 'ice_candidate':
      if (pc && msg.payload) {
        const candidate = typeof msg.payload === 'string' ? JSON.parse(msg.payload) : msg.payload
        await pc.addIceCandidate(candidate).catch((err) => console.warn('[Host] add ICE failed:', err))
      }
      return

    case 'peer_left':
      peerReady.value = false
      disconnectNotice.value = '控制端已断开，等待新的设备接入'
      pc?.close()
      pc = null
      dc = null
      return

    case 'MOUSE_MOVE':
    case 'MOUSE_CLICK':
    case 'KEY_PRESS':
    case 'SCROLL':
      app?.ExecuteCommand?.(JSON.stringify({ type: msg.type, payload: msg.payload }))
      return
  }
}

async function handleOffer(payload: OfferPayload) {
  const { description, profile } = unwrapOfferPayload(payload)
  await ensureScreenStream()
  await createPeerConnection(profile)
  await pc!.setRemoteDescription(new RTCSessionDescription(description))
  const answer = await pc!.createAnswer()
  await pc!.setLocalDescription(answer)
  sendToSignal(buildEnv('answer', pc!.localDescription))
  streamStatus.value = `已应用 ${VIDEO_PROFILES[profile].label}，正在建立直连`
}

async function cleanup() {
  dc?.close()
  dc = null
  pc?.close()
  pc = null
  ws?.close()
  ws = null
  if (screenStream) {
    for (const track of screenStream.getTracks()) track.stop()
  }
  screenStream = null
  if (previewRef.value) previewRef.value.srcObject = null
  peerReady.value = false
}

onUnmounted(() => {
  cleanup()
})
</script>

<style scoped>
.host {
  max-width: 760px;
  margin: 0 auto;
  padding: 2rem;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
}
.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.5rem;
}
.header h1 {
  font-size: 1.5rem;
  margin: 0.25rem 0 0;
}
.btn-link {
  padding: 0;
  border: 0;
  background: transparent;
  color: #4f46e5;
  cursor: pointer;
}
.status {
  font-size: 0.8rem;
  padding: 0.2rem 0.6rem;
  border-radius: 4px;
}
.online { background: #22c55e; color: #fff; }
.offline { background: #64748b; color: #fff; }
.card {
  background: #f5f5f5;
  border-radius: 8px;
  padding: 1.25rem;
  margin-bottom: 1rem;
}
.card h2 {
  font-size: 1rem;
  margin: 0 0 0.75rem;
}
.code-box {
  display: flex;
  gap: 0.5rem;
  align-items: center;
}
.code-box code {
  font-size: 1.8rem;
  letter-spacing: 0.3em;
  background: #fff;
  padding: 0.25rem 0.75rem;
  border-radius: 4px;
  border: 1px solid #ddd;
}
.btn-copy {
  padding: 0.4rem 0.8rem;
  background: #4f46e5;
  color: #fff;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.85rem;
}
.btn-copy:disabled {
  background: #cbd5e1;
  cursor: not-allowed;
}
.hint {
  font-size: 0.8rem;
  color: #666;
  margin: 0.5rem 0 0;
}
.label-sm {
  display: block;
  font-size: 0.8rem;
  color: #666;
  margin-bottom: 0.3rem;
}
.input-server {
  width: 100%;
  padding: 0.5rem 0.75rem;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-size: 0.85rem;
  box-sizing: border-box;
}
.preview {
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  background: #000;
  border-radius: 6px;
  object-fit: contain;
}
.device-list {
  list-style: none;
  padding: 0;
  margin: 0;
}
.device-list li {
  padding: 0.4rem 0;
  border-bottom: 1px solid #e0e0e0;
}
.empty {
  color: #999;
  font-size: 0.9rem;
}
.notice {
  color: #b45309;
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
}
.btn-primary {
  width: 100%;
  padding: 0.75rem;
  background: #4f46e5;
  color: #fff;
  border: none;
  border-radius: 6px;
  font-size: 1rem;
  cursor: pointer;
}
.btn-primary:disabled {
  background: #a5b4fc;
  cursor: not-allowed;
}
.btn-primary:hover:not(:disabled) {
  background: #4338ca;
}
</style>
