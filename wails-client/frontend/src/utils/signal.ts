export const DEFAULT_SIGNAL_SERVER = 'wss://signal.h2seo4.win:8443'

export function getDefaultSignalServer(): string {
  if (import.meta.env.DEV) {
    return DEFAULT_SIGNAL_SERVER
  }
  return (import.meta.env.VITE_SIGNAL_SERVER as string) || DEFAULT_SIGNAL_SERVER
}
