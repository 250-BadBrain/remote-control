const CHUNK_MAGIC = 0x52434631 // "RCF1"
const CHUNK_HEADER_BYTES = 12

type PendingFrame = {
  chunks: ArrayBuffer[]
  received: number
  total: number
  size: number
}

const pendingFrames = new Map<number, PendingFrame>()

export async function handleIncomingFrame(data: unknown, render: (blob: Blob) => void) {
  if (data instanceof Blob) {
    const buffer = await data.arrayBuffer()
    if (handleFrameChunk(buffer, render)) return
    render(data)
    return
  }

  if (data instanceof ArrayBuffer) {
    if (handleFrameChunk(data, render)) return
    render(new Blob([data], { type: 'image/jpeg' }))
  }
}

function handleFrameChunk(buffer: ArrayBuffer, render: (blob: Blob) => void): boolean {
  if (buffer.byteLength < CHUNK_HEADER_BYTES) return false

  const view = new DataView(buffer)
  if (view.getUint32(0, false) !== CHUNK_MAGIC) return false

  const frameID = view.getUint32(4, false)
  const total = view.getUint16(8, false)
  const index = view.getUint16(10, false)
  if (total === 0 || index >= total) return true

  let pending = pendingFrames.get(frameID)
  if (!pending) {
    pending = {
      chunks: new Array(total),
      received: 0,
      total,
      size: 0,
    }
    pendingFrames.set(frameID, pending)
  }

  if (!pending.chunks[index]) {
    const chunk = buffer.slice(CHUNK_HEADER_BYTES)
    pending.chunks[index] = chunk
    pending.received++
    pending.size += chunk.byteLength
  }

  if (pending.received === pending.total) {
    pendingFrames.delete(frameID)
    render(new Blob(pending.chunks, { type: 'image/jpeg' }))
  }

  while (pendingFrames.size > 5) {
    const oldest = pendingFrames.keys().next().value
    pendingFrames.delete(oldest)
  }

  return true
}
