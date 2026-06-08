<template>
  <main class="home">
    <header class="header">
      <h1>远程控制</h1>
      <p>选择这台电脑的工作模式</p>
    </header>

    <section class="mode-grid">
      <article class="mode-card">
        <div>
          <h2>控制此电脑</h2>
          <p>共享本机屏幕，生成房间码，让手机或另一台电脑接入控制。</p>
        </div>
        <button class="btn-primary" @click="router.push('/host')">进入受控端</button>
      </article>

      <article class="mode-card">
        <div>
          <h2>控制远程电脑</h2>
          <p>输入对方房间码，在本窗口中直接用鼠标映射和滚轮控制远程电脑。</p>
        </div>
        <div class="control-form">
          <label class="label-sm" for="room-code">房间码</label>
          <input
            id="room-code"
            v-model="roomCode"
            maxlength="6"
            inputmode="numeric"
            class="input"
            placeholder="输入 6 位房间码"
            @keyup.enter="openViewer"
          />
          <label class="label-sm" for="signal-server">信令服务器</label>
          <input
            id="signal-server"
            v-model="serverAddr"
            class="input"
            placeholder="wss://signal.h2seo4.win:8443"
            @keyup.enter="openViewer"
          />
          <button class="btn-secondary" :disabled="roomCode.trim().length !== 6" @click="openViewer">
            进入控制端
          </button>
        </div>
      </article>
    </section>
  </main>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { DEFAULT_SIGNAL_SERVER, getDefaultSignalServer } from '../utils/signal'

const router = useRouter()
const roomCode = ref('')
const serverAddr = ref(getDefaultSignalServer() || DEFAULT_SIGNAL_SERVER)

function openViewer() {
  const code = roomCode.value.trim()
  if (code.length !== 6) return
  router.push({
    path: '/viewer',
    query: {
      code,
      signal: serverAddr.value.trim() || DEFAULT_SIGNAL_SERVER,
    },
  })
}
</script>

<style scoped>
.home {
  max-width: 920px;
  margin: 0 auto;
  padding: 2rem;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
}

.header {
  margin-bottom: 1.5rem;
}

.header h1 {
  margin: 0;
  font-size: 1.8rem;
}

.header p {
  margin: 0.4rem 0 0;
  color: #64748b;
}

.mode-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}

.mode-card {
  min-height: 280px;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  gap: 1.25rem;
  background: #f5f5f5;
  border-radius: 8px;
  padding: 1.25rem;
  border: 1px solid #e5e7eb;
}

.mode-card h2 {
  margin: 0 0 0.5rem;
  font-size: 1.15rem;
}

.mode-card p {
  margin: 0;
  color: #64748b;
  line-height: 1.55;
  font-size: 0.92rem;
}

.control-form {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.label-sm {
  font-size: 0.8rem;
  color: #666;
}

.input {
  width: 100%;
  box-sizing: border-box;
  padding: 0.58rem 0.75rem;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 0.92rem;
}

.btn-primary,
.btn-secondary {
  width: 100%;
  padding: 0.75rem;
  border: none;
  border-radius: 6px;
  font-size: 1rem;
  cursor: pointer;
}

.btn-primary {
  background: #4f46e5;
  color: #fff;
}

.btn-secondary {
  background: #0f172a;
  color: #fff;
}

.btn-secondary:disabled {
  background: #94a3b8;
  cursor: not-allowed;
}

@media (max-width: 720px) {
  .mode-grid {
    grid-template-columns: 1fr;
  }
}
</style>
