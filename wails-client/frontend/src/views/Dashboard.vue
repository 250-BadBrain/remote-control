<template>
  <div class="dashboard">
    <header class="header">
      <h1>远程控制</h1>
      <span class="status" :class="connected ? 'online' : 'offline'">
        {{ connected ? '已连接' : '未连接' }}
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
      <p class="hint">在手机浏览器中输入此 6 位码即可控制本机</p>
    </section>

    <section class="card">
      <h2>控制端设备</h2>
      <ul class="device-list" v-if="devices.length">
        <li v-for="d in devices" :key="d.id">{{ d.name }} — {{ d.status }}</li>
      </ul>
      <p v-else class="empty">等待设备接入中...</p>
    </section>

    <button class="btn-primary" @click="startSession" :disabled="connecting">
      {{ connecting ? '连接中...' : '启动会话' }}
    </button>

  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

const go = (window as any).go
const app = go?.main?.App

const sessionId = ref('')
const connected = ref(false)
const connecting = ref(false)
const devices = ref<{ id: string; name: string; status: string }[]>([])

/* 服务端地址默认为本机，用户可根据实际局域网 IP 修改 */
const serverAddr = ref(getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER)

function copyCode() {
  navigator.clipboard?.writeText(sessionId.value)
}

async function startSession() {
  if (connecting.value) return
  connecting.value = true

  if (!app?.Connect) {
    sessionId.value = Math.random().toString(36).substring(2, 8).toUpperCase()
    connected.value = true
    connecting.value = false
    return
  }

  try {
    await app.Connect('computer', serverAddr.value, '')
    sessionId.value = await app.GetSessionID()
    connected.value = true
  } catch (err: any) {
    console.error('[Dashboard] 连接失败:', err)
  }
  connecting.value = false
}

onMounted(async () => {
  if (app?.GetSessionID) {
    const id = await app.GetSessionID()
    if (id) {
      sessionId.value = id
      connected.value = true
    }
  }
})
</script>

<style scoped>
.dashboard {
  max-width: 640px;
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
  margin: 0;
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
  margin-top: 0.5rem;
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
.links {
  margin-top: 1rem;
  text-align: center;
}
.links a {
  color: #4f46e5;
  font-size: 0.9rem;
  text-decoration: none;
}
</style>
