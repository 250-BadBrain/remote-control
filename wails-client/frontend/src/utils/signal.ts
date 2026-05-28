export const DEFAULT_SIGNAL_SERVER = 'wss://161.153.98.231:8443'

export function getDefaultSignalServer(): string {
  if (import.meta.env.DEV) {
    return DEFAULT_SIGNAL_SERVER
  }
  return (import.meta.env.VITE_SIGNAL_SERVER as string) || DEFAULT_SIGNAL_SERVER
}
