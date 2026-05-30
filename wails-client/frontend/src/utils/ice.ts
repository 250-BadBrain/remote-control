const DEFAULT_STUN_URLS = [
  'stun:stun.l.google.com:19302',
  'stun:turn.h2seo4.win:3478',
]

const DEFAULT_TURN_URLS = [
  'turn:turn.h2seo4.win:3478?transport=udp',
  'turn:turn.h2seo4.win:3478?transport=tcp',
]

function splitUrls(value?: string): string[] {
  return (value || '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}

export function buildIceServers(): RTCIceServer[] {
  const iceServers: RTCIceServer[] = [
    { urls: DEFAULT_STUN_URLS },
  ]

  const turnUrls = splitUrls(import.meta.env.VITE_TURN_URLS as string)
  const username = ((import.meta.env.VITE_TURN_USERNAME as string) || 'remoteuser').trim()
  const credential = ((import.meta.env.VITE_TURN_PASSWORD as string) || '').trim()

  if (credential) {
    iceServers.push({
      urls: turnUrls.length ? turnUrls : DEFAULT_TURN_URLS,
      username,
      credential,
    })
  }

  return iceServers
}
