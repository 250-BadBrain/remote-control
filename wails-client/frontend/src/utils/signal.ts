export function getDefaultSignalServer(): string {
  if (import.meta.env.DEV) {
    return 'ws://localhost:8080'
  }
  return (import.meta.env.VITE_SIGNAL_SERVER as string) || ''
}
