// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'

// Exercise the application response fences without creating editor workers.
vi.mock('./components/editor/MarkdownEditor', () => ({
  MarkdownEditor: ({ markdown, onChange }: { markdown: string; onChange: (value: string) => void }) =>
    <textarea aria-label="Test draft" value={markdown} onChange={(event) => onChange(event.target.value)} />,
}))

let emit: (locked: boolean) => void
let failChannel: () => void
let openChannel: () => void
let generation: number
let intercept: (url: string, init?: RequestInit) => Promise<Response> | undefined
beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  generation = 1
  intercept = () => undefined
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('EventSource', class {
    listeners = new Map<string, EventListener>()
    constructor() {
      emit = (locked) => {
        generation++
        this.listeners.get('encryption-lock')?.(new MessageEvent('encryption-lock', {
          data: JSON.stringify({ vaultId: 'personal', locked, generation }),
        }))
      }
      failChannel = () => this.listeners.get('error')?.(new Event('error'))
      openChannel = () => this.listeners.get('open')?.(new Event('open'))
    }
    addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener) }
    removeEventListener(type: string) { this.listeners.delete(type) }
    close() {}
  })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const response = intercept(url, init)
    if (response) return response
    if (url === '/api/health') return Response.json({})
    if (url === '/api/notebook') return Response.json({ name: 'Personal', configured: true })
    if (url === '/api/notebooks') return Response.json({ activeId: 'personal', notebooks: [{ id: 'personal', name: 'Personal' }] })
    if (url === '/api/encryption/status') return Response.json({ vaultId: 'personal', protected: true, locked: false, generation, idleRemainingMs: 900_000, activationEnabled: false })
    if (url === '/api/repository/tree') return Response.json({ entries: ['Secret', 'Second'].map((name) => ({ name: `${name}.md`, path: `${name}.md`, type: 'file', protected: true })) })
    if (url.startsWith('/api/repository/file?')) return Response.json({ path: new URL(url, 'https://test').searchParams.get('path'), content: 'original protected content', version: 'v1' })
    if (url.startsWith('/api/encryption/activity?')) return Response.json({ recorded: true, vaultId: 'personal', generation, idleRemainingMs: 900_000 })
    return Response.json({ error: 'unexpected request' }, { status: 500 })
  }))
})
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })

async function openDraft() {
  const view = render(<App />)
  await view.findByRole('button', { name: 'Lock vault' })
  fireEvent.click(await view.findByRole('button', { name: 'Secret' }))
  await view.findByRole('textbox', { name: 'Test draft' })
  return view
}

describe('protected content response fences', () => {
  it('does not extend the display deadline through successful status polling', async () => {
    vi.useFakeTimers()
    const view = render(<App />)
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    fireEvent.click(view.getByRole('button', { name: 'Secret' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(view.getByRole('textbox', { name: 'Test draft' })).toBeTruthy()
    await act(async () => { await vi.advanceTimersByTimeAsync(900_001) })
    expect(view.queryByRole('textbox', { name: 'Test draft' })).toBeNull()
    expect(vi.mocked(fetch).mock.calls.filter(([url]) => String(url) === '/api/encryption/status').length).toBeGreaterThan(2)
  })

  it('uses the remaining server idle time, including response latency', async () => {
    vi.useFakeTimers()
    let confirm!: (response: Response) => void
    intercept = (url) => url === '/api/encryption/status' ? new Promise((resolve) => { confirm = resolve }) : undefined
    const view = render(<App />)
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(confirm).toBeTypeOf('function')
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000) })
    await act(async () => confirm(Response.json({ vaultId: 'personal', protected: true, locked: false, generation, idleRemainingMs: 1_000, activationEnabled: false })))
    expect(view.getAllByRole('button', { name: 'Unlock vault' }).length).toBeGreaterThan(0)
    expect(view.queryByRole('button', { name: 'Lock vault' })).toBeNull()
  })

  it('does not reveal the retained draft through a late unlock after channel failure', async () => {
    const view = await openDraft()
    fireEvent.change(view.getByRole('textbox', { name: 'Test draft' }), { target: { value: 'hidden-late-unlock-draft' } })
    act(() => emit(true))
    let finish!: (response: Response) => void
    intercept = (url) => url === '/api/encryption/unlock' ? new Promise((resolve) => { finish = resolve }) : undefined
    fireEvent.click(view.getAllByRole('button', { name: 'Unlock vault' })[0])
    fireEvent.change(view.getByLabelText('Encryption password'), { target: { value: 'password' } })
    fireEvent.click(view.getByRole('button', { name: 'Unlock' }))
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    act(() => failChannel())
    await act(async () => finish(Response.json({ vaultId: 'personal', unlocked: true, generation: generation + 1, idleRemainingMs: 900_000 })))
    expect(view.queryByRole('textbox', { name: 'Test draft' })).toBeNull()
  })

  it.each(['editor', 'conflict', 'search'] as const)('hides %s at the idle deadline with a silent channel and stalled status', async (surface) => {
    vi.useFakeTimers()
    const view = render(<App />)
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    fireEvent.click(view.getByRole('button', { name: 'Secret' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    fireEvent.change(view.getByRole('textbox', { name: 'Test draft' }), { target: { value: 'idle-hidden-draft' } })
    intercept = (url, init) => {
      if (url === '/api/encryption/status') return new Promise(() => undefined)
      if (url === '/api/repository/search') return Promise.resolve(Response.json({ results: [{ path: 'Secret.md', type: 'content', line: 1, excerpt: 'idle-search-secret' }] }))
      if (url.startsWith('/api/repository/file?') && init?.method === 'PUT') return Promise.resolve(Response.json({ error: 'stale' }, { status: 409 }))
      if (url.startsWith('/api/repository/file?')) return Promise.resolve(Response.json({ path: 'Secret.md', content: 'idle-conflict-secret', version: 'v2' }))
    }
    if (surface === 'search') {
      fireEvent.change(view.getByRole('searchbox', { name: 'Search this notebook' }), { target: { value: 'idle-search-secret' } })
      await act(async () => { await vi.advanceTimersByTimeAsync(300) })
      expect(view.container.textContent).toContain('idle-search-secret')
    } else if (surface === 'conflict') {
      fireEvent.click(view.getByRole('button', { name: 'Save' }))
      await act(async () => { await vi.advanceTimersByTimeAsync(1) })
      expect(view.getByRole('dialog', { name: 'This note changed on the server' })).toBeTruthy()
    }
    await act(async () => { await vi.advanceTimersByTimeAsync(900_001) })
    expect(view.queryByRole('textbox', { name: 'Test draft' })).toBeNull()
    expect(view.queryByRole('dialog', { name: 'This note changed on the server' })).toBeNull()
    expect(view.container.textContent).not.toContain('idle-search-secret')
    expect(view.container.textContent).not.toContain('idle-conflict-secret')
    const stored = [localStorage, sessionStorage].flatMap((storage) => Array.from({ length: storage.length }, (_, index) => storage.getItem(storage.key(index)!))).join(' ')
    expect(stored).not.toContain('idle-hidden-draft')
  })

  it.each(['sse', 'offline'] as const)('hides the protected draft immediately on %s loss and rejects a late search', async (failure) => {
    const view = await openDraft()
    let finish!: (response: Response) => void
    intercept = (url) => url === '/api/repository/search' ? new Promise((resolve) => { finish = resolve }) : undefined
    fireEvent.change(view.getByRole('textbox', { name: 'Test draft' }), { target: { value: 'hidden-channel-draft' } })
    // Start a pending derived-content response while retaining the draft.
    fireEvent.change(view.getByRole('searchbox', { name: 'Search this notebook' }), { target: { value: 'late-channel-query' } })
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    act(() => { if (failure === 'sse') failChannel(); else window.dispatchEvent(new Event('offline')) })
    expect(view.queryByRole('textbox', { name: 'Test draft' })).toBeNull()
    await act(async () => finish(Response.json({ results: [{ path: 'Secret.md', type: 'content', excerpt: 'late-channel-secret' }] })))
    expect(view.container.textContent).not.toContain('late-channel-secret')
    // A channel reopening alone does not authorize plaintext; fresh status does.
    let confirm!: (response: Response) => void
    intercept = (url) => url === '/api/encryption/status' ? new Promise((resolve) => { confirm = resolve }) : undefined
    act(() => openChannel())
    await waitFor(() => expect(confirm).toBeTypeOf('function'))
    expect(view.queryByRole('textbox', { name: 'Test draft' })).toBeNull()
    await act(async () => confirm(Response.json({ vaultId: 'personal', protected: true, locked: false, generation, idleRemainingMs: 900_000, activationEnabled: false })))
    expect((await view.findByRole('textbox', { name: 'Test draft' }) as HTMLTextAreaElement).value).toBe('hidden-channel-draft')
    const stored = [localStorage, sessionStorage].flatMap((storage) => Array.from({ length: storage.length }, (_, index) => storage.getItem(storage.key(index)!))).join(' ')
    expect(stored).not.toContain('hidden-channel-draft')
  })
  it.each(['visible', 'late'] as const)('discards a %s conflict on lock and preserves the unsaved draft', async (mode) => {
    const view = await openDraft()
    let finish!: (response: Response) => void
    intercept = (url, init) => {
      if (url.startsWith('/api/repository/file?') && init?.method === 'PUT') return Promise.resolve(Response.json({ error: 'stale' }, { status: 409 }))
      if (url.startsWith('/api/repository/file?') && !init?.method) {
        if (mode === 'late') return new Promise((resolve) => { finish = resolve })
        return Promise.resolve(Response.json({ path: 'Secret.md', content: 'server-secret-sentinel', version: 'v2' }))
      }
    }
    fireEvent.change(view.getByRole('textbox', { name: 'Test draft' }), { target: { value: 'unsaved-secret-sentinel' } })
    fireEvent.click(view.getByRole('button', { name: 'Save' }))
    if (mode === 'visible') await view.findByRole('dialog', { name: 'This note changed on the server' })
    else await waitFor(() => expect(finish).toBeTypeOf('function'))
    act(() => emit(true))
    if (mode === 'late') await act(async () => finish(Response.json({ path: 'Secret.md', content: 'server-secret-sentinel', version: 'v2' })))
    expect(view.queryByRole('dialog', { name: 'This note changed on the server' })).toBeNull()
    expect(view.container.textContent).not.toContain('server-secret-sentinel')
    act(() => emit(false))
    expect((await view.findByRole('textbox', { name: 'Test draft' }) as HTMLTextAreaElement).value).toBe('unsaved-secret-sentinel')
  })

  it('does not install a delayed note response after lock and unlock', async () => {
    const view = await openDraft()
    let finish!: (response: Response) => void
    intercept = (url) => url === '/api/repository/file?path=Second.md' ? new Promise((resolve) => { finish = resolve }) : undefined
    fireEvent.click(view.getByRole('button', { name: 'Second' }))
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    act(() => emit(true))
    act(() => emit(false))
    await act(async () => finish(Response.json({ path: 'Second.md', content: 'late-note-sentinel', version: 'v1' })))
    expect((await view.findByRole('textbox', { name: 'Test draft' }) as HTMLTextAreaElement).value).toBe('original protected content')
  })

  it('does not acknowledge a late save over the preserved unsaved draft', async () => {
    const view = await openDraft()
    let finish!: (response: Response) => void
    intercept = (url, init) => url.startsWith('/api/repository/file?') && init?.method === 'PUT' ? new Promise((resolve) => { finish = resolve }) : undefined
    fireEvent.change(view.getByRole('textbox', { name: 'Test draft' }), { target: { value: 'unsaved-save-sentinel' } })
    fireEvent.click(view.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    act(() => emit(true))
    act(() => emit(false))
    await act(async () => finish(Response.json({ path: 'Secret.md', content: 'unsaved-save-sentinel', version: 'v2' })))
    expect((await view.findByRole('textbox', { name: 'Test draft' }) as HTMLTextAreaElement).value).toBe('unsaved-save-sentinel')
    expect((view.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('rejects an old note response after switching away and back to the same vault', async () => {
    let active = 'personal'
    let finish!: (response: Response) => void
    intercept = (url, init) => {
      if (url === '/api/notebooks') return Promise.resolve(Response.json({ activeId: active, notebooks: [{ id: 'personal', name: 'Personal' }, { id: 'work', name: 'Work' }] }))
      if (url.endsWith('/activate') && init?.method === 'POST') {
        active = url.includes('/work/') ? 'work' : 'personal'
        return Promise.resolve(Response.json({ id: active, name: active }))
      }
      if (url === '/api/encryption/status') return Promise.resolve(Response.json({ vaultId: active, protected: true, locked: false, generation: 1, idleRemainingMs: 900_000, activationEnabled: false }))
      if (url === '/api/repository/file?path=Second.md') return new Promise((resolve) => { finish = resolve })
    }
    const view = await openDraft()
    fireEvent.click(view.getByRole('button', { name: 'Second' }))
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    for (const name of ['Work', 'Personal']) {
      fireEvent.click(view.getByRole('button', { name: /Vaults/ }))
      fireEvent.click(view.getByRole('menuitemradio', { name }))
      await waitFor(() => expect(active).toBe(name === 'Work' ? 'work' : 'personal'))
      await waitFor(() => expect(view.queryByRole('textbox', { name: 'Test draft' })).toBeNull())
      await view.findByRole('button', { name: 'Lock vault' })
    }
    fireEvent.click(view.getByRole('button', { name: 'Secret' }))
    await view.findByRole('textbox', { name: 'Test draft' })
    await act(async () => finish(Response.json({ path: 'Second.md', content: 'foreign-view-sentinel', version: 'v2' })))
    expect((view.getByRole('textbox', { name: 'Test draft' }) as HTMLTextAreaElement).value).toBe('original protected content')
  })

  it.each(['visible', 'late'] as const)('discards a %s backlink preview on lock', async (mode) => {
    const view = await openDraft()
    let finish!: (response: Response) => void
    const preview = () => Response.json({ source: 'Secret.md', target: 'Renamed.md', token: 'token', rewrites: [{ notePath: 'Second.md', nextNotePath: 'Second.md', line: 1, before: 'backlink-secret-sentinel', after: 'Renamed.md' }] })
    intercept = (url) => {
      if (url !== '/api/repository/move/preview') return undefined
      return mode === 'late' ? new Promise((resolve) => { finish = resolve }) : Promise.resolve(preview())
    }
    fireEvent.click(view.getByRole('button', { name: 'Selected item actions' }))
    fireEvent.click(view.getByRole('menuitem', { name: 'Rename' }))
    fireEvent.change(view.getByLabelText('New name'), { target: { value: 'Renamed.md' } })
    fireEvent.submit(view.getByLabelText('New name').closest('form')!)
    if (mode === 'visible') await view.findByRole('dialog', { name: 'Update note links?' })
    else await waitFor(() => expect(finish).toBeTypeOf('function'))
    act(() => emit(true))
    if (mode === 'late') await act(async () => finish(preview()))
    expect(view.queryByRole('dialog', { name: 'Update note links?' })).toBeNull()
    expect(view.container.textContent).not.toContain('backlink-secret-sentinel')
  })

  it('does not replace the preserved draft with a late conflict resolution', async () => {
    const view = await openDraft()
    let finish!: (response: Response) => void
    intercept = (url, init) => {
      if (url.startsWith('/api/repository/file?') && init?.method === 'PUT') return Promise.resolve(Response.json({ error: 'stale' }, { status: 409 }))
      if (url === '/api/repository/file/resolve-conflict') return new Promise((resolve) => { finish = resolve })
    }
    fireEvent.change(view.getByRole('textbox', { name: 'Test draft' }), { target: { value: 'preserved-unsaved-draft' } })
    fireEvent.click(view.getByRole('button', { name: 'Save' }))
    const conflict = await view.findByRole('dialog', { name: 'This note changed on the server' })
    fireEvent.click(within(conflict).getByRole('button', { name: 'Save my version' }))
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    act(() => emit(true))
    act(() => emit(false))
    await act(async () => finish(Response.json({ path: 'Secret.md', content: 'late-resolution-sentinel', version: 'v3' })))
    expect((await view.findByRole('textbox', { name: 'Test draft' }) as HTMLTextAreaElement).value).toBe('preserved-unsaved-draft')
  })
})
