import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { App } from './App'
import { apiFetch, authStatus, AuthStatusError, listenForAuthEvents, notifyAuthChanged, setCSRFToken, type AuthStatus } from './api'

type GateState = 'checking' | 'ready' | 'setup' | 'login' | 'mfa' | 'recover' | 'recover-confirm' | 'offline' | 'backend' | 'update'

async function responseJSON<T>(response: Response): Promise<T> {
  const body = await response.json() as T & { error?: string }
  if (!response.ok) throw new Error(body.error ?? `Request failed (${response.status})`)
  return body
}

export function AuthGate() {
  const [state, setState] = useState<GateState>('checking')
  const [status, setStatus] = useState<AuthStatus>()
  const [version, setVersion] = useState('dev')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [initialVaultPassword, setInitialVaultPassword] = useState<string>()
  const [recoveryChallenge, setRecoveryChallenge] = useState<string>()
  const [replacementRecoveryCode, setReplacementRecoveryCode] = useState('')
  const [recoveryNewPassword, setRecoveryNewPassword] = useState('')
  const pendingLoginPassword = useRef('')
  const checking = useRef(false)
  const stateRef = useRef(state)

  useEffect(() => {
    stateRef.current = state
  }, [state])

  const endBrowserSession = useCallback(() => {
    setCSRFToken()
    pendingLoginPassword.current = ''
    setInitialVaultPassword(undefined)
    setState('login')
  }, [])

  const check = useCallback(async () => {
    if (checking.current) return
    checking.current = true
    try {
      const next = await authStatus()
      setStatus(next)
      if (stateRef.current !== 'recover' && stateRef.current !== 'recover-confirm') setState(next.mode === 'disabled' || next.authenticated ? 'ready' : next.setupRequired ? 'setup' : next.mfaRequired ? 'mfa' : 'login')
      if (!next.authenticated && !next.mfaRequired && stateRef.current !== 'recover' && stateRef.current !== 'recover-confirm') {
        pendingLoginPassword.current = ''
        setInitialVaultPassword(undefined)
      }
      setError(undefined)
      const health = await fetch('/api/health', { cache: 'no-store' })
      const type = health.headers.get('content-type') ?? ''
      if (!type.includes('application/json')) { setState('update'); return }
      if (health.ok) setVersion(((await health.json()) as { version?:string }).version || 'dev')
    } catch (caught) {
      setState(caught instanceof AuthStatusError && caught.kind==='update' ? 'update' : navigator.onLine ? 'backend' : 'offline')
      setError(caught instanceof Error ? caught.message : 'Authentication status is unavailable.')
    } finally { checking.current = false }
  }, [])

  useEffect(() => {
    queueMicrotask(() => void check())
    let lastCheck = 0
    const retry = () => { const now = Date.now(); if (now-lastCheck < 1000 || document.visibilityState === 'hidden') return; lastCheck=now; void check() }
    window.addEventListener('online', retry)
    window.addEventListener('focus', retry)
    document.addEventListener('visibilitychange', retry)
    const stop = listenForAuthEvents(endBrowserSession, retry)
    return () => { window.removeEventListener('online', retry); window.removeEventListener('focus', retry); document.removeEventListener('visibilitychange', retry); stop() }
  }, [check, endBrowserSession])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const password = String(form.get('password') ?? '')
    const confirm = String(form.get('confirmPassword') ?? '')
    if ((state === 'setup' || state === 'recover') && password !== confirm) { setError('Passwords do not match.'); return }
    setBusy(true); setError(undefined)
    try {
      if (state === 'recover') {
        const result = await responseJSON<{ challengeId: string; recoveryCode: string }>(await apiFetch('/api/auth/recovery/prepare', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ recoveryCode: String(form.get('vaultRecoveryCode') ?? ''), newPassword: password }),
        }))
        setRecoveryChallenge(result.challengeId)
        setReplacementRecoveryCode(result.recoveryCode)
        setRecoveryNewPassword(password)
        setState('recover-confirm')
        return
      }
      const endpoint = state === 'setup' ? '/api/auth/setup' : state === 'mfa' ? '/api/auth/login/mfa' : '/api/auth/login'
      const body = state === 'setup'
        ? { bootstrapToken:String(form.get('bootstrapToken') ?? ''), password }
        : state === 'mfa' ? { code:String(form.get('code') ?? '') }
        : { password, rememberDevice:form.get('rememberDevice') === 'on' }
      if (state === 'login' || state === 'setup') pendingLoginPassword.current = password
      const response = await apiFetch(endpoint, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body) })
      const result = await response.json() as { error?:string; code?:string; csrfToken?:string; mfaRequired?:boolean }
      if (!response.ok) {
        if (state === 'mfa' && result.code === 'mfa_challenge_expired') setState('login')
        throw new Error(result.error ?? `Authentication failed (${response.status})`)
      }
      if (result.mfaRequired) { setState('mfa'); return }
      if (state === 'login' || state === 'setup') {
        setInitialVaultPassword(password)
        pendingLoginPassword.current = ''
      } else if (state === 'mfa') {
        setInitialVaultPassword(pendingLoginPassword.current || undefined)
        pendingLoginPassword.current = ''
      }
      setCSRFToken(result.csrfToken)
      notifyAuthChanged()
      await check()
    } catch (caught) { setError(caught instanceof Error ? caught.message : 'Authentication failed.') }
    finally { setBusy(false) }
  }

  async function confirmRecovery(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!recoveryChallenge) return
    const form = new FormData(event.currentTarget)
    setBusy(true)
    setError(undefined)
    try {
      await responseJSON(await apiFetch('/api/auth/recovery/confirm', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ challengeId: recoveryChallenge, newPassword: recoveryNewPassword, recoveryCode: String(form.get('replacementRecoveryCode') ?? ''), confirmed: form.get('recoveryCodeStored') === 'on' }),
      }))
      setRecoveryChallenge(undefined)
      setReplacementRecoveryCode('')
      setRecoveryNewPassword('')
      setError(undefined)
      setState('login')
      setCSRFToken()
      await check()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Password recovery failed.')
    } finally {
      setBusy(false)
    }
  }

  async function copyReplacementCode() {
    if (!navigator.clipboard) {
      setError('Clipboard is unavailable. Select and copy the code manually.')
      return
    }
    try {
      await navigator.clipboard.writeText(replacementRecoveryCode)
      setError(undefined)
    } catch {
      setError('Clipboard is unavailable. Select and copy the code manually.')
    }
  }

  if (state === 'ready' && status) return <App authMode={status.mode} runningVersion={version} initialVaultPassword={initialVaultPassword} onInitialVaultPasswordConsumed={() => setInitialVaultPassword(undefined)} onLoggedOut={() => { endBrowserSession(); notifyAuthChanged() }} />
  if (state === 'checking') return <GateMessage title="Opening VariaNotes…">Checking the secure session.</GateMessage>
  if (state === 'offline') return <GateMessage title="You are offline" action={() => void check()}>VariaNotes is online-first. Reconnect to the server before opening or editing notes.</GateMessage>
  if (state === 'backend') return <GateMessage title="Backend unavailable" action={() => void check()}>{error ?? 'The VariaNotes server could not be reached.'}</GateMessage>
  if (state === 'update') return <GateMessage title="Frontend update required" action={() => window.location.reload()}>The cached app shell does not match the running backend. Reload VariaNotes to update it.</GateMessage>
  if (state === 'recover-confirm') return <main className="flex min-h-screen items-center justify-center bg-zinc-950 p-4 text-zinc-100"><section className="w-full max-w-md rounded-xl border border-zinc-800 bg-zinc-900 p-6 shadow-2xl sm:p-8"><p className="text-xs font-semibold uppercase tracking-[0.22em] text-amber-400">VariaNotes</p><h1 className="mt-2 text-2xl font-semibold">Store your replacement recovery code</h1><p className="mt-2 text-sm leading-6 text-zinc-400">Your password and vault key envelopes change only after you confirm this new code. Store it somewhere separate from VariaNotes. Earlier backups keep their old password and recovery envelopes.</p><div className="mt-4 rounded border border-zinc-700 bg-zinc-950 p-3"><code className="select-all break-all font-mono text-sm text-zinc-100">{replacementRecoveryCode}</code><button type="button" onClick={() => void copyReplacementCode()} className="mt-3 min-h-9 rounded border border-zinc-700 px-3 text-xs text-zinc-300">Copy recovery code</button></div><form className="mt-4 space-y-4" onSubmit={(event) => void confirmRecovery(event)}><label className="block text-sm text-zinc-300">Re-enter the replacement code<input name="replacementRecoveryCode" required autoComplete="off" spellCheck={false} className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 font-mono text-sm" /></label><label className="flex items-start gap-2 text-xs leading-5 text-zinc-300"><input name="recoveryCodeStored" type="checkbox" required className="mt-0.5 h-4 w-4" />I stored this code separately and understand that confirming recovery signs out all sessions.</label>{error && <p role="alert" className="rounded-md border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{error}</p>}<div className="flex justify-end gap-2"><button type="button" disabled={busy} onClick={() => { setRecoveryChallenge(undefined); setReplacementRecoveryCode(''); setRecoveryNewPassword(''); setError(undefined); setState('login') }} className="min-h-10 rounded-md border border-zinc-700 px-3 text-sm text-zinc-300">Cancel</button><button disabled={busy} className="min-h-10 rounded-md bg-amber-500 px-4 text-sm font-semibold text-zinc-950 disabled:opacity-50">{busy ? 'Recovering…' : 'Confirm recovery'}</button></div></form></section></main>

  const title = state === 'setup' ? 'Set up your owner password' : state === 'mfa' ? 'Two-factor authentication' : state === 'recover' ? 'Recover password and vault access' : 'Welcome back'
  const description = state === 'setup' ? 'Enter the one-time token generated by the VariaNotes operator and choose a password.' : state === 'mfa' ? 'Enter the current six-digit authenticator code or one unused recovery code.' : state === 'recover' ? 'Use your one-time vault recovery code to choose a new sign-in password. Recovery preserves encrypted vault contents.' : 'Sign in to VariaNotes.'
  return <main className="flex min-h-screen items-center justify-center bg-zinc-950 p-4 text-zinc-100"><section className="w-full max-w-md rounded-xl border border-zinc-800 bg-zinc-900 p-6 shadow-2xl sm:p-8"><p className="text-xs font-semibold uppercase tracking-[0.22em] text-amber-400">VariaNotes</p><h1 className="mt-2 text-2xl font-semibold">{title}</h1><p className="mt-2 text-sm leading-6 text-zinc-400">{description}</p><form className="mt-6 space-y-4" onSubmit={(event) => void submit(event)}>{state === 'setup' && <label className="block text-sm text-zinc-300">One-time setup token<input name="bootstrapToken" required autoComplete="off" spellCheck={false} className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 font-mono text-sm outline-none focus:border-amber-500" /></label>}{state === 'recover' && <label className="block text-sm text-zinc-300">Vault recovery code<input name="vaultRecoveryCode" required autoComplete="off" spellCheck={false} className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 font-mono text-sm outline-none focus:border-amber-500" /></label>}{state !== 'mfa' && state !== 'recover' && <label className="block text-sm text-zinc-300">Password<input name="password" type="password" required minLength={15} maxLength={1024} autoComplete={state === 'setup' ? 'new-password' : 'current-password'} className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500" /></label>}{state === 'recover' && <><label className="block text-sm text-zinc-300">New password<input name="password" type="password" required minLength={15} maxLength={1024} autoComplete="new-password" className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500" /></label><label className="block text-sm text-zinc-300">Confirm new password<input name="confirmPassword" type="password" required minLength={15} maxLength={1024} autoComplete="new-password" className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500" /></label></>}{state === 'mfa' && <label className="block text-sm text-zinc-300">Authentication code<input name="code" required autoFocus autoComplete="one-time-code" inputMode="numeric" spellCheck={false} className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 font-mono text-sm outline-none focus:border-amber-500" /></label>}{state === 'setup' && <label className="block text-sm text-zinc-300">Confirm password<input name="confirmPassword" type="password" required minLength={15} maxLength={1024} autoComplete="new-password" className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500" /></label>}{state === 'login' && <label className="flex min-h-11 items-center gap-3 text-sm text-zinc-300"><input name="rememberDevice" type="checkbox" className="h-5 w-5 accent-amber-500" />Remember this device</label>}{error && <p role="alert" className="rounded-md border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{error}</p>}<button disabled={busy} className="min-h-11 w-full rounded-md bg-amber-500 px-4 font-semibold text-zinc-950 hover:bg-amber-400 disabled:opacity-50">{busy ? 'Please wait…' : state === 'setup' ? 'Complete setup' : state === 'mfa' ? 'Verify' : state === 'recover' ? 'Prepare recovery' : 'Sign in'}</button></form>{(state === 'login' || state === 'recover') && <button type="button" onClick={() => { setError(undefined); setState(state === 'login' ? 'recover' : 'login') }} className="mt-4 min-h-10 text-xs text-amber-300 hover:text-amber-200">{state === 'login' ? 'Forgot password? Use a vault recovery code' : 'Back to sign in'}</button>}</section></main>
}

function GateMessage({ title, children, action }: { title:string; children:string; action?:()=>void }) {
  return <main className="flex min-h-screen items-center justify-center bg-zinc-950 p-4 text-zinc-100"><section role="status" className="w-full max-w-md rounded-xl border border-zinc-800 bg-zinc-900 p-7"><p className="text-xs font-semibold uppercase tracking-[0.22em] text-amber-400">VariaNotes</p><h1 className="mt-2 text-xl font-semibold">{title}</h1><p className="mt-3 text-sm leading-6 text-zinc-400">{children}</p>{action && <button type="button" onClick={action} className="mt-5 min-h-11 rounded-md border border-zinc-700 px-4 text-sm hover:bg-zinc-800">Try again</button>}</section></main>
}
