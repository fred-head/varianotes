export type AuthStatus = {
  mode: 'local' | 'disabled'
  setupRequired: boolean
  authenticated: boolean
  mfaRequired?: boolean
  csrfToken?: string
}

let csrfToken = ''
const authRequiredEvent = 'varianotes:auth-required'
const authChangedEvent = 'varianotes:auth-changed'
const channelName = 'varianotes-auth'

export function setCSRFToken(token?: string) {
  csrfToken = token ?? ''
}

export function currentCSRFToken() { return csrfToken }

export class AuthStatusError extends Error { constructor(message:string, readonly kind:'backend'|'update'){super(message)} }

export async function apiFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const method = (init.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase()
  const headers = new Headers(init.headers ?? (input instanceof Request ? input.headers : undefined))
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && csrfToken) headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(input, { ...init, cache: 'no-store', headers })
  const pathname = typeof input === 'string' ? input : input instanceof URL ? input.pathname : new URL(input.url, window.location.href).pathname
  const authenticationAttempt = pathname === '/api/auth/login' || pathname === '/api/auth/login/mfa'
  if (response.status === 401 && !authenticationAttempt) {
    setCSRFToken()
    window.dispatchEvent(new CustomEvent(authRequiredEvent))
  }
  return response
}

export async function authStatus(signal?: AbortSignal): Promise<AuthStatus> {
  const response = await fetch('/api/auth/status', { signal, cache: 'no-store' })
  if (!(response.headers.get('content-type') ?? '').includes('application/json')) throw new AuthStatusError('The frontend does not match the backend.', 'update')
  const body = await response.json() as AuthStatus & { error?: string }
  if (response.status === 404) throw new AuthStatusError('The backend does not support this frontend authentication contract.', 'update')
  if (!response.ok) throw new AuthStatusError(body.error ?? `Authentication status failed (${response.status})`, 'backend')
  setCSRFToken(body.csrfToken)
  return body
}

export function notifyAuthChanged() {
  const Channel = (window as unknown as { BroadcastChannel?: typeof BroadcastChannel }).BroadcastChannel
  if (typeof Channel === 'function') {
    const channel = new Channel(channelName)
    channel.postMessage('changed')
    channel.close()
  } else {
    window.dispatchEvent(new CustomEvent(authChangedEvent))
  }
}

export function listenForAuthEvents(onRequired: () => void, onChanged: () => void): () => void {
  const required = () => onRequired()
  const changed = () => onChanged()
  window.addEventListener(authRequiredEvent, required)
  window.addEventListener(authChangedEvent, changed)
  const Channel = (window as unknown as { BroadcastChannel?: typeof BroadcastChannel }).BroadcastChannel
  const channel = typeof Channel === 'function' ? new Channel(channelName) : undefined
  if (channel) channel.onmessage = changed
  return () => {
    window.removeEventListener(authRequiredEvent, required)
    window.removeEventListener(authChangedEvent, changed)
    channel?.close()
  }
}
