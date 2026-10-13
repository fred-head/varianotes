// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App, TrashDialog } from './App'

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  Range.prototype.getClientRects = () => [] as unknown as DOMRectList
  Range.prototype.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0)
  vi.stubGlobal('ResizeObserver', ResizeObserverStub)
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
  localStorage.clear()
  sessionStorage.clear()
  localStorage.setItem('varianotes.auto-lock-minutes', '1')
  let active = 'personal'
  let notebooks = [{ id: 'local', name: 'repos' }, { id: 'personal', name: 'Personal Notes', branch: 'main' }, { id: 'work', name: 'Work', branch: 'main' }]
  const noteContent = '# Auto-lock note'
  const noteVersion = 'v1'
  let notePath = 'Note.md'
  let noteTrashed = false
  let trashItems: Array<{id:string;originalPath:string;type:'file';deletedAt:string;size:number}> = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url === '/api/health') return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (url === '/api/auth/logout' && init?.method === 'POST') return Response.json({ authenticated: false })
    if (url === '/api/notebook') return Response.json({ name: active === 'personal' ? 'Personal Notes' : 'Work', configured: true })
    if (url === '/api/notebooks') return Response.json({ activeId: active, notebooks })
    if (url === '/api/notebooks/work/activate' && init?.method === 'POST') { active = 'work'; return Response.json({ id: 'work', name: 'Work' }) }
    if (url === '/api/notebooks/local' && init?.method === 'DELETE') { notebooks = notebooks.filter((notebook) => notebook.id !== 'local'); return new Response(null, { status: 204 }) }
    if (url === '/api/repository/tree') return Response.json({ entries: active === 'personal' ? [...(noteTrashed ? [] : [{ name: notePath, path: notePath, type: 'file' }]), { name: 'Second.md', path: 'Second.md', type: 'file' }] : [{ name: 'Work.md', path: 'Work.md', type: 'file' }] })
    if (url === '/api/repository/search' && init?.method === 'POST') {
      const body = JSON.parse(String(init.body)) as { query: string }
      if (body.query === 'auto-lock') return Response.json({ results: [{ path: 'Note.md', type: 'content', line: 1, excerpt: '# Auto-lock note' }] })
      if (body.query === 'search-sentinel-7e3f1') return Response.json({ results: [{ path: 'Private.md', type: 'content', line: 1, excerpt: 'search-sentinel-7e3f1 protected excerpt' }] })
      return Response.json({ results: [] })
    }
    if (url === '/api/repository/move/preview' && init?.method === 'POST') { const body = JSON.parse(String(init.body)); return Response.json({ source:body.source,target:body.target,token:'rewrite-token',rewrites:[{notePath:'Second.md',nextNotePath:'Second.md',line:1,before:'Note.md',after:body.target}] }) }
    if (url === '/api/repository/move' && init?.method === 'POST') { const body = JSON.parse(String(init.body)); notePath = body.target; return Response.json({ path:body.target,rewrites:[] }) }
    if (url === '/api/repository/entry?path=Note.md' && init?.method === 'DELETE') { noteTrashed = true; const item = { id: 'b'.repeat(32), originalPath: 'Note.md', type: 'file' as const, deletedAt: '2026-08-28T10:00:00Z', size: 42 }; trashItems = [item]; return Response.json(item) }
    if (url === '/api/repository/trash') return Response.json({ items: trashItems })
    if (url === `/api/repository/trash/${'b'.repeat(32)}/restore` && init?.method === 'POST') { noteTrashed = false; const item = trashItems[0]; trashItems = []; return Response.json(item) }
    if (url === `/api/repository/trash/${'b'.repeat(32)}` && init?.method === 'DELETE') { const item = trashItems[0]; trashItems = []; return Response.json(item) }
    if (url.includes('Second.md')) return Response.json({ path: 'Second.md', content: '# Second note', version: 'v2' })
    if (url.startsWith('/api/repository/file?')) return Response.json({ path: notePath, content: noteContent, version: noteVersion })
    return Response.json({ error: 'unexpected request' }, { status: 500 })
  }))
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('Application version in Settings', () => {
  it('shows the exact running version beside the Settings heading without a copy button', async () => {
    const version = '0.1.0-alpha.2.security.5'
    const view = render(<App runningVersion={version} />)
    fireEvent.click(await view.findByRole('button', { name: 'Settings' }))
    const heading = await view.findByRole('heading', { name: 'Settings' })
    const header = heading.parentElement!

    expect(header.className).toContain('justify-between')
    expect(within(header).getByText(version)).toBeTruthy()
    expect(view.queryByRole('button', { name: 'Copy version' })).toBeNull()
    expect(view.queryByRole('region', { name: 'Application version' })).toBeNull()
  })

  it('shows dev beside the Settings heading for an unversioned development build', async () => {
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name: 'Settings' }))
    const heading = await view.findByRole('heading', { name: 'Settings' })
    const header = heading.parentElement!

    expect(within(header).getByText('dev')).toBeTruthy()
  })
})

// Milkdown removes document listeners 3 seconds after editor setup.
afterAll(async () => new Promise((resolve) => setTimeout(resolve, 3_100)))

describe('App auto-lock integration', () => {
  it('keeps the note visible when inactivity changes it to Read only', async () => {
    const view = render(<App />)
    expect(await view.findByText('VariaNotes')).toBeTruthy()
    await waitFor(() => expect((view.container.querySelector('[aria-haspopup="menu"]') as HTMLElement).textContent).toContain('Vaults'))
    await waitFor(() => expect(view.getAllByRole('button', { name: 'Personal Notes' }).length).toBeGreaterThan(0))
    expect(view.getByText('Create in:').parentElement?.textContent).toContain('Personal Notes')
    expect(view.container.textContent).not.toContain('Local repository')
    expect(view.container.textContent).not.toContain('Repository Root')
    const noteButton = await view.findByRole('button', { name: 'Note' })
    fireEvent.click(noteButton)
	await waitFor(() => expect(view.container.textContent).toContain('Auto-lock note'), { timeout: 5000 })

    vi.useFakeTimers()
    // Changing the setting restarts the same active timer under fake timers.
    fireEvent.click(view.getByRole('button', { name: 'Settings' }))
    await act(async () => {
      fireEvent.change(view.getByLabelText('Make editor read-only after'), { target: { value: '5' } })
      await Promise.resolve()
    })
    fireEvent.click(view.getByRole('button', { name: 'Done' }))
    await act(async () => {
      vi.advanceTimersByTime(300_000)
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(view.getByRole('button', { name: '🔒 Read only' })).toBeTruthy()
    expect(view.container.textContent).toContain('Auto-lock note')
  })

  it('uses one bounded PWA scroll area and measures the sticky toolbar offset from the visible header stack', async () => {
    const view = render(<App />)
    const headerStack = await waitFor(() => {
      const element = view.container.querySelector<HTMLElement>('.varianotes-note-header-stack')
      expect(element).toBeTruthy()
      return element!
    })
    vi.spyOn(headerStack, 'getBoundingClientRect').mockReturnValue({
      x: 0, y: 0, top: 0, left: 0, right: 320, bottom: 123, width: 320, height: 123, toJSON: () => ({}),
    })
    fireEvent(window, new Event('resize'))

    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    const formatting = await view.findByRole('toolbar', { name: 'Editor formatting' })
    await waitFor(() => expect(view.container.querySelector('.ProseMirror')).toBeTruthy())
    const toolbars = formatting.closest<HTMLElement>('[aria-label="Editor toolbars"]')
    const noteScroller = formatting.closest<HTMLElement>('main')
    await waitFor(() => expect(noteScroller?.style.getPropertyValue('--editor-toolbar-top')).toBe('123px'))
    expect(view.container.firstElementChild?.classList.contains('varianotes-app-shell')).toBe(true)
    expect(noteScroller?.classList.contains('varianotes-note-scroll')).toBe(true)
    expect(toolbars?.dataset.sticky).toBe('true')
  })

  it('collapses and restores the desktop sidebar without replacing an active editor and remembers the preference', async () => {
    const user = userEvent.setup()
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    fireEvent.click(await view.findByRole('button', { name: 'Second' }), { ctrlKey: true })
    await waitFor(() => expect(view.getAllByRole('tab')).toHaveLength(2))
    fireEvent.click(view.getByRole('tab', { name: 'Note' }))
    await waitFor(() => expect(view.container.querySelector('.ProseMirror')?.textContent).toContain('Auto-lock note'))
    const editor = await waitFor(() => {
      const element = view.container.querySelector<HTMLElement>('.ProseMirror')
      expect(element).toBeTruthy()
      return element!
    })
    editor.focus()
    await user.type(editor, ' sidebar draft', { skipClick: true })
    await waitFor(() => expect(editor.textContent).toContain('sidebar draft'))
    const draftHTML = editor.innerHTML

    const sidebar = view.getByRole('complementary', { name: 'Notebook navigation' })
    const toggle = within(sidebar).getByRole('button', { name: 'Collapse notebook sidebar' })
    toggle.focus()
    await user.keyboard('{Enter}')
    const restore = within(sidebar).getByRole('button', { name: 'Expand notebook sidebar' })
    expect(restore.getAttribute('aria-expanded')).toBe('false')
    expect(sidebar.classList.contains('lg:w-12')).toBe(true)
    expect(sidebar.classList.contains('lg:invisible')).toBe(false)
    expect(view.container.querySelector('article')?.classList.contains('max-w-none')).toBe(true)
    expect(localStorage.getItem('varianotes.notebook-sidebar-collapsed')).toBe('true')
    expect(view.getAllByRole('tab')).toHaveLength(2)
    expect(view.getByRole('tab', { name: 'Second' })).toBeTruthy()
    expect(view.getByRole('tab', { name: 'Note' }).getAttribute('aria-selected')).toBe('true')
    expect(view.container.querySelector('.ProseMirror')).toBe(editor)
    expect(editor.innerHTML).toBe(draftHTML)

    restore.focus()
    await user.keyboard('{Enter}')
    expect(within(sidebar).getByRole('button', { name: 'Collapse notebook sidebar' }).getAttribute('aria-expanded')).toBe('true')
    expect(sidebar.classList.contains('lg:w-80')).toBe(true)
    expect(view.container.querySelector('article')?.classList.contains('max-w-4xl')).toBe(true)
    expect(localStorage.getItem('varianotes.notebook-sidebar-collapsed')).toBeNull()
    expect(view.container.querySelector('.ProseMirror')).toBe(editor)
    expect(editor.innerHTML).toBe(draftHTML)

    fireEvent.click(within(sidebar).getByRole('button', { name: 'Collapse notebook sidebar' }))
    view.unmount()
    const reloaded = render(<App />)
    expect(await reloaded.findByRole('button', { name: 'Expand notebook sidebar' })).toBeTruthy()
  })

  it('keeps the mobile navigation drawer available when the desktop sidebar preference is collapsed', async () => {
    localStorage.setItem('varianotes.notebook-sidebar-collapsed', 'true')
    const view = render(<App />)
    const openNavigation = await view.findByRole('button', { name: 'Open notebook navigation' })
    expect(openNavigation.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(openNavigation)
    expect(openNavigation.getAttribute('aria-expanded')).toBe('true')
    const sidebar = view.getByRole('complementary', { name: 'Notebook navigation' })
    expect(sidebar.classList.contains('translate-x-0')).toBe(true)
    expect(sidebar.classList.contains('lg:w-12')).toBe(true)
    expect(within(sidebar).getByRole('button', { name: 'Expand notebook sidebar' })).toBeTruthy()
    expect(sidebar.querySelector('#notebook-navigation-content')).toBeTruthy()
  })

  it('opens vault navigation, onboarding, and switches without stale tree state', async () => {
    const view = render(<App />)
    await waitFor(() => expect((view.container.querySelector('[aria-haspopup="menu"]') as HTMLElement).textContent).toContain('Vaults'))
    await waitFor(() => expect(view.getAllByRole('button', { name: 'Personal Notes' }).length).toBeGreaterThan(0))
    const switcher = view.container.querySelector('[aria-haspopup="menu"]') as HTMLButtonElement
    fireEvent.click(switcher)
    expect(view.getByRole('menuitemradio', { name: 'Personal Notes' }).getAttribute('aria-checked')).toBe('true')
    fireEvent.click(view.getByRole('menuitem', { name: '+ Add Vault' }))
    expect(view.getByRole('heading', { name: 'Add Vault' })).toBeTruthy()
    fireEvent.click(view.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(switcher)
    fireEvent.click(view.getByRole('menuitemradio', { name: 'Work' }))
    await waitFor(() => expect(switcher.textContent).toContain('Vaults'))
    expect(view.queryByRole('button', { name: 'Note' })).toBeNull()
    await waitFor(() => expect(view.getAllByRole('button', { name: 'Work' }).length).toBeGreaterThan(0))
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([url]) => String(url).includes('/api/repository/git/'))).toBe(false)
  })

  it('offers vault onboarding on a fresh installation', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/health') return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (url === '/api/notebook') return Response.json({ name: 'Notebook', configured: false })
      if (url === '/api/notebooks') return Response.json({ activeId: '', notebooks: [] })
      if (url === '/api/repository/tree') return Response.json({ error: 'repository is not configured' }, { status: 503 })
      return Response.json({ error: 'unexpected request' }, { status: 500 })
    }))

    const view = render(<App />)
    expect(await view.findByRole('heading', { name: 'Create your first vault' })).toBeTruthy()
    expect(view.getByText('No vault yet')).toBeTruthy()
    expect(view.queryByText(/Set VARIANOTES_DEV_VAULT_PATH/)).toBeNull()
    fireEvent.click(view.getAllByRole('button', { name: 'Add Vault' })[0])
    expect(view.getByRole('heading', { name: 'Add Vault' })).toBeTruthy()
    expect(view.getByRole('button', { name: 'Create an empty vault' })).toBeTruthy()
  })

  it('creates a filesystem vault and makes it available to the active app', async () => {
    let created = false
    const notebook = { id: 'vault-id', name: 'My notes' }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/health') return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (url === '/api/notebook') return Response.json({ name: created ? notebook.name : 'Notebook', configured: created })
      if (url === '/api/notebooks') return Response.json({ activeId: created ? notebook.id : '', notebooks: created ? [notebook] : [] })
      if (url === '/api/notebooks/local' && init?.method === 'POST') { created = true; return Response.json(notebook, { status: 201 }) }
      if (url === `/api/notebooks/${notebook.id}/health`) return Response.json({ localFiles: { state: 'ok' }, writeAccess: { state: 'ok' } })
      if (url === '/api/repository/tree') return Response.json({ entries: [] })
      return Response.json({ error: 'unexpected request' }, { status: 500 })
    }))

    const view = render(<App />)
    fireEvent.click((await view.findAllByRole('button', { name: 'Add Vault' }))[0])
    fireEvent.change(view.getByLabelText('Vault name'), { target: { value: notebook.name } })
    fireEvent.click(view.getByRole('button', { name: 'Create vault' }))

    await waitFor(() => expect(created).toBe(true))
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([url, init]) => String(url) === '/api/notebooks/local' && init?.method === 'POST' && JSON.parse(String(init.body)).name === notebook.name)).toBe(true)
    await view.findByRole('heading', { name: 'Manage Vaults' })
    fireEvent.click(view.getByRole('button', { name: 'Done' }))
    expect(await view.findByRole('button', { name: notebook.name })).toBeTruthy()
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([url]) => String(url).includes('/api/repository/git/'))).toBe(false)
  })

  it('unregisters an inactive legacy notebook without presenting file deletion', async () => {
    const view = render(<App />)
    const switcher = view.container.querySelector('[aria-haspopup="menu"]') as HTMLButtonElement
    await waitFor(() => expect(switcher.textContent).toContain('Vaults'))
    await waitFor(() => expect(view.getAllByRole('button', { name: 'Personal Notes' }).length).toBeGreaterThan(0))
    fireEvent.click(switcher)
    fireEvent.click(view.getByRole('menuitem', { name: 'Manage Vaults' }))
    const legacyCard = (await view.findByRole('heading', { name: 'repos' })).closest('section') as HTMLElement
    const remove = Array.from(legacyCard.querySelectorAll('button')).find(button => button.textContent === 'Remove…')!
    fireEvent.click(remove)
    fireEvent.click(view.getByRole('button', { name: 'Remove registration' }))
    await waitFor(() => expect(view.queryByRole('heading', { name: 'repos' })).toBeNull())
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([url, init]) => String(url) === '/api/notebooks/local' && init?.method === 'DELETE')).toBe(true)
  })

  it('warns that deleting a vault folder also removes legacy Git history', async () => {
    const view = render(<App />)
    const switcher = view.container.querySelector('[aria-haspopup="menu"]') as HTMLButtonElement
    await waitFor(() => expect(switcher.textContent).toContain('Vaults'))
    fireEvent.click(switcher)
    fireEvent.click(view.getByRole('menuitem', { name: 'Manage Vaults' }))
    const workCard = (await view.findByRole('heading', { name: 'Work' })).closest('section') as HTMLElement
    fireEvent.click(within(workCard).getByRole('button', { name: 'Remove…' }))
    const confirmation = await view.findByRole('alertdialog', { name: 'Remove Work?' })
    fireEvent.click(within(confirmation).getByRole('checkbox'))
    expect(confirmation.textContent).toContain('legacy .git history')
    expect(confirmation.textContent).toContain('Operator backups may retain copies')
  })

  it('opens, switches, and closes notes in session tabs without duplicate editors', async () => {
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    await waitFor(() => expect(view.container.textContent).toContain('Auto-lock note'))
    expect(view.getAllByRole('tab')).toHaveLength(1)

    fireEvent.click(view.getByRole('button', { name: 'Second' }), { ctrlKey: true })
    await waitFor(() => expect(view.container.textContent).toContain('Second note'))
    expect(view.getAllByRole('tab')).toHaveLength(2)
    expect(view.getByRole('tab', { name: 'Second' }).getAttribute('aria-selected')).toBe('true')

    fireEvent.keyDown(window, { key: 'Tab', ctrlKey: true })
    await waitFor(() => expect(view.container.textContent).toContain('Auto-lock note'))
    expect(view.getByRole('tab', { name: 'Note' }).getAttribute('aria-selected')).toBe('true')

    fireEvent.keyDown(window, { key: 'w', ctrlKey: true })
    await waitFor(() => expect(view.queryByRole('tab', { name: 'Note' })).toBeNull())
    expect(view.getAllByRole('tab')).toHaveLength(1)
    expect(view.container.textContent).toContain('Second note')
  })

  it('previews and confirms portable link rewrites before renaming a note', async () => {
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name:'Note' }))
    await waitFor(() => expect(view.container.textContent).toContain('Auto-lock note'))
    fireEvent.click(view.getByRole('button', { name:'Selected item actions' }))
    fireEvent.click(view.getByRole('menuitem', { name:'Rename' }))
    fireEvent.change(view.getByLabelText('New name'), { target:{ value:'Renamed.md' } })
    fireEvent.submit(view.getByLabelText('New name').closest('form')!)
    const dialog = await view.findByRole('dialog', { name:'Update note links?' })
    expect(dialog.textContent).toContain('Second.md')
    expect(dialog.textContent).toContain('Note.md')
    expect(dialog.textContent).toContain('Renamed.md')
    fireEvent.click(view.getByRole('button', { name:'Move and update links' }))
    await waitFor(() => expect(view.getByRole('button', { name:'Renamed' })).toBeTruthy())
    const moveCall = vi.mocked(globalThis.fetch).mock.calls.find(([url,init]) => String(url)==='/api/repository/move' && init?.method==='POST')
    expect(JSON.parse(String(moveCall?.[1]?.body))).toEqual({ source:'Note.md',target:'Renamed.md',rewriteToken:'rewrite-token' })
  })

  it('preserves both versions when a stale browser draft meets a changed server file', async () => {
    const fallback = vi.mocked(globalThis.fetch)
    let serverContent = '# Auto-lock note'
    let serverVersion = 'v1'
    let conflicted = false
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.startsWith('/api/repository/file?') && init?.method === 'PUT' && !conflicted) {
        conflicted = true
        serverContent = '# Server version'
        serverVersion = 'v2'
        return Response.json({ error: 'file changed since it was opened' }, { status: 409 })
      }
      if (url === '/api/repository/file/resolve-conflict' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as { content: string }
        serverContent = body.content
        serverVersion = 'v3'
        return Response.json({ path: 'Note.md', content: serverContent, version: serverVersion })
      }
      if (url.startsWith('/api/repository/file?')) return Response.json({ path: 'Note.md', content: serverContent, version: serverVersion })
      return fallback(input, init)
    }))

    const user = userEvent.setup()
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    const editor = await view.findByText('Auto-lock note').then((element) => element.closest('.ProseMirror') as HTMLElement)
    editor.focus()
    await user.type(editor, ' from browser', { skipClick: true })
    await waitFor(() => expect(editor.textContent).toContain('from browser'))
    fireEvent.click(view.getByRole('button', { name: 'Save' }))

    const dialog = await view.findByRole('dialog', { name: 'This note changed on the server' })
    expect(dialog.textContent).toContain('from browser')
    expect(dialog.textContent).toContain('Server version')
    fireEvent.click(view.getByRole('button', { name: 'Save my version' }))
    await waitFor(() => expect(serverContent).toContain('from browser'))
    expect(view.queryByRole('dialog', { name: 'This note changed on the server' })).toBeNull()
  })

  it('moves a note to notebook Trash and restores it from the touch-friendly Trash view', async () => {
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    await waitFor(() => expect(view.container.textContent).toContain('Auto-lock note'))
    fireEvent.click(view.getByRole('button', { name: 'Selected item actions' }))
    fireEvent.click(view.getByRole('menuitem', { name: 'Move to Trash' }))
    fireEvent.click(view.getByRole('button', { name: 'Move to Trash' }))
    await waitFor(() => expect(view.queryByRole('button', { name: 'Note' })).toBeNull())

    fireEvent.click(view.getByRole('button', { name: 'Open Trash' }))
    expect(await view.findByRole('dialog', { name: 'Trash' })).toBeTruthy()
    expect(view.getByText('Note.md')).toBeTruthy()
    fireEvent.click(view.getByRole('button', { name: 'Restore' }))
    await waitFor(() => expect(view.getByText('Restored “Note.md”.')).toBeTruthy())
    await waitFor(() => expect(view.getByRole('button', { name: 'Note' })).toBeTruthy())
    expect(view.getByText('Trash is empty.')).toBeTruthy()
  })

  it('requires explicit confirmation before permanently deleting a trashed note', async () => {
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    fireEvent.click(view.getByRole('button', { name: 'Selected item actions' }))
    fireEvent.click(view.getByRole('menuitem', { name: 'Move to Trash' }))
    fireEvent.click(view.getByRole('button', { name: 'Move to Trash' }))
    await waitFor(() => expect(view.queryByRole('button', { name: 'Note' })).toBeNull())
    fireEvent.click(view.getByRole('button', { name: 'Open Trash' }))
    fireEvent.click(await view.findByRole('button', { name: 'Permanently delete' }))
    fireEvent.click(view.getAllByRole('button', { name: 'Permanently delete' }).at(-1)!)
    await waitFor(() => expect(view.getByText('Trash is empty.')).toBeTruthy())
    expect(view.queryByRole('alertdialog', { name: 'Permanently delete?' })).toBeNull()
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([url, init]) => String(url) === `/api/repository/trash/${'b'.repeat(32)}` && init?.method === 'DELETE')).toBe(true)
  })

  it('offers creation from note and notebook-root context menus', async () => {
    const view = render(<App />)
    const note = await view.findByRole('button', { name: 'Note' })
    fireEvent.contextMenu(note.closest('div')!)
    expect(view.getByRole('menuitem', { name: 'New Note' })).toBeTruthy()
    expect(view.getByRole('menuitem', { name: 'New Folder' })).toBeTruthy()
    fireEvent.click(view.getByRole('menuitem', { name: 'New Folder' }))
    expect(view.getByRole('dialog', { name: 'New Folder' })).toBeTruthy()
    fireEvent.click(view.getByRole('button', { name: 'Cancel' }))
    fireEvent.contextMenu(view.getByRole('button', { name: 'Personal Notes' }))
    expect(view.getByRole('menuitem', { name: 'New Note' })).toBeTruthy()
  })

  it('shows a direct sign-out action when local authentication is enabled', async () => {
    const onLoggedOut = vi.fn()
    const view = render(<App authMode="local" onLoggedOut={onLoggedOut} />)
    fireEvent.click(await view.findByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(onLoggedOut).toHaveBeenCalledOnce())
  })

  it('does not start synchronization when the browser tab closes', async () => {
    render(<App />)
    await waitFor(() => expect(vi.mocked(globalThis.fetch).mock.calls.some(([url]) => String(url) === '/api/notebooks')).toBe(true))
    window.dispatchEvent(new Event('pagehide'))
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([url]) => String(url).includes('/api/repository/git/'))).toBe(false)
  })

  it('searches the active notebook and opens a content result', async () => {
    const view = render(<App />)
    const search = await view.findByRole('searchbox', { name: 'Search this notebook' })
    fireEvent.change(search, { target: { value: 'auto-lock' } })
    const result = await view.findByRole('button', { name: /Note.*L1/ })
    expect(result.textContent).toContain('Auto-lock note')
    fireEvent.click(result)
    await waitFor(() => expect(view.container.textContent).toContain('Auto-lock note'))
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([url, init]) => String(url) === '/api/repository/search' && init?.method === 'POST' && JSON.parse(String(init.body)).query === 'auto-lock')).toBe(true)
  })

  it('keeps search data out of browser storage and discards it on server lock, including late responses', async () => {
    const eventSources: Array<{ url: string; listeners: Map<string, EventListener>; closed: boolean; close: () => void; addEventListener: (type: string, listener: EventListener) => void; removeEventListener: (type: string, listener: EventListener) => void; emit: (locked: boolean) => void }> = []
    class EventSourceStub {
      url: string
      listeners = new Map<string, EventListener>()
      closed = false
      constructor(url: string) { this.url = url; eventSources.push(this) }
      addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener) }
      removeEventListener(type: string) { this.listeners.delete(type) }
      close() { this.closed = true }
      emit(locked: boolean) {
        const listener = this.listeners.get('encryption-lock')
        listener?.(new MessageEvent('encryption-lock', { data: JSON.stringify({ vaultId: 'personal', locked }) }))
      }
    }
    vi.stubGlobal('EventSource', EventSourceStub)
    const searchSentinel = 'search-sentinel-7e3f1'
    let finishLateResponse: ((response: Response) => void) | undefined
    const fetchMock = vi.mocked(globalThis.fetch)
    const originalFetch = fetchMock.getMockImplementation()!
    fetchMock.mockImplementation((input, init) => {
      if (String(input) === '/api/repository/search' && JSON.parse(String(init?.body)).query === 'late-sentinel') {
        return new Promise<Response>((resolve) => { finishLateResponse = resolve })
      }
      return originalFetch(input, init)
    })

    const view = render(<App />)
    const search = await view.findByRole('searchbox', { name: 'Search this notebook' })
    await waitFor(() => expect(eventSources.some((source) => source.url.includes('vaultId=personal'))).toBe(true))
    const source = eventSources.find((item) => item.url.includes('vaultId=personal'))!
    source.emit(false)

    fireEvent.change(search, { target: { value: searchSentinel } })
    const result = await view.findByRole('button', { name: /Private.*L1/ })
    expect(result.textContent).toContain(searchSentinel)
    const storageSnapshot = [localStorage, sessionStorage].flatMap((storage) => Array.from({ length: storage.length }, (_, index) => `${storage.key(index)}=${storage.getItem(storage.key(index)!)}`)).join(' ')
    expect(storageSnapshot).not.toContain(searchSentinel)
    source.emit(true)
    await waitFor(() => expect((search as HTMLInputElement).value).toBe(''))
    expect(view.queryByRole('button', { name: /Private.*L1/ })).toBeNull()

    source.emit(false)
    fireEvent.change(search, { target: { value: 'late-sentinel' } })
    await waitFor(() => expect(finishLateResponse).toBeDefined())
    source.emit(true)
    await act(async () => {
      finishLateResponse?.(Response.json({ results: [{ path: 'Private.md', type: 'content', line: 1, excerpt: 'late-sentinel plaintext' }] }))
      await Promise.resolve()
    })
    expect((search as HTMLInputElement).value).toBe('')
    expect(view.container.textContent).not.toContain('late-sentinel')
    const updatedStorageSnapshot = [localStorage, sessionStorage].flatMap((storage) => Array.from({ length: storage.length }, (_, index) => `${storage.key(index)}=${storage.getItem(storage.key(index)!)}`)).join(' ')
    expect(updatedStorageSnapshot).not.toContain('late-sentinel')
  })

  it('opens mobile notebook navigation and closes it after choosing a note', async () => {
    const view = render(<App />)
    const openNavigation = view.getByRole('button', { name: 'Open notebook navigation' })
    const navigation = view.getByRole('complementary', { name: 'Notebook navigation' })
    expect(navigation.className).toContain('-translate-x-full')
    fireEvent.click(openNavigation)
    expect(openNavigation.getAttribute('aria-expanded')).toBe('true')
    expect(navigation.className).toContain('translate-x-0')
    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    await waitFor(() => expect(openNavigation.getAttribute('aria-expanded')).toBe('false'))
  })

  it('reports server unavailability and offers browser-provided PWA installation', async () => {
    const view = render(<App />)
    window.dispatchEvent(new Event('offline'))
    expect(await view.findByText(/Vault files are online-only/)).toBeTruthy()

    const prompt = vi.fn(async () => undefined)
    const event = new Event('beforeinstallprompt') as Event & { prompt: typeof prompt; userChoice: Promise<{ outcome: 'accepted' }> }
    event.prompt = prompt
    event.userChoice = Promise.resolve({ outcome: 'accepted' })
    window.dispatchEvent(event)
    const install = await view.findByRole('button', { name: 'Install app' })
    fireEvent.click(install)
    await waitFor(() => expect(prompt).toHaveBeenCalledOnce())
  })

  it('keeps the PWA installation suggestion dismissed', async () => {
    const view = render(<App />)
    const event = new Event('beforeinstallprompt') as InstallPromptEventForTest
    event.prompt = vi.fn(async () => undefined)
    event.userChoice = Promise.resolve({ outcome: 'dismissed' })
    window.dispatchEvent(event)
    fireEvent.click(await view.findByRole('button', { name: 'Dismiss install suggestion' }))
    expect(localStorage.getItem('varianotes.install-prompt-dismissed')).toBe('true')
    expect(view.queryByText('Install VariaNotes for a standalone app experience.')).toBeNull()

    cleanup()
    const nextView = render(<App />)
    window.dispatchEvent(event)
    expect(nextView.queryByText('Install VariaNotes for a standalone app experience.')).toBeNull()
  })

  it('does not persist unsaved note content into browser storage', async () => {
    const view = render(<App />)
    fireEvent.click(await view.findByRole('button', { name: 'Note' }))
    const editor = await view.findByText('Auto-lock note').then((element) => element.closest('.ProseMirror') as HTMLElement)
    editor.focus()
    await userEvent.type(editor, ' private draft', { skipClick: true })
    await waitFor(() => expect(editor.textContent).toContain('private draft'))
  })

  it('requires an explicit keep or decrypt choice when moving protected content out', async () => {
    const fetchMock = vi.mocked(globalThis.fetch)
    const originalFetch = fetchMock.getMockImplementation()!
    const moveBodies: Array<Record<string, unknown>> = []
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input)
      if (url === '/api/repository/tree') return Response.json({ entries: [
        { name: 'Secret.md', path: 'Secret.md', type: 'file', protected: true },
        { name: 'archive', path: 'archive', type: 'directory', children: [] },
      ] })
      if (url === '/api/repository/move/preview' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as { source: string; target: string }
        return Response.json({ ...body, token: 'preview-token', rewrites: [] })
      }
      if (url === '/api/repository/move' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>
        moveBodies.push(body)
        if (moveBodies.length === 1) return Response.json({ error: 'protection choice required', code: 'move_protection_choice_required' }, { status: 409 })
        return Response.json({ path: body.target, rewrites: [] })
      }
      return originalFetch(input, init)
    })

    const view = render(<App />)
    const secret = await view.findByRole('button', { name: 'Secret' })
    fireEvent.contextMenu(secret)
    fireEvent.click(view.getByRole('menuitem', { name: 'Move…' }))
    const picker = await view.findByRole('dialog', { name: 'Move “Secret.md”' })
    fireEvent.click(within(picker).getByRole('button', { name: 'archive' }))
    fireEvent.click(within(picker).getByRole('button', { name: 'Move here' }))

    const choice = await view.findByRole('dialog', { name: 'Choose protection after moving' })
    expect(choice.textContent).toContain('A shared attachment remains protected while any protected note still references it.')
    expect(moveBodies[0]).not.toHaveProperty('protectionChoice')
    fireEvent.click(within(choice).getByRole('button', { name: 'Decrypt and move' }))
    await waitFor(() => expect(moveBodies).toHaveLength(2))
    expect(moveBodies[1]).toMatchObject({ source: 'Secret.md', target: 'archive/Secret.md', protectionChoice: 'decrypt' })
  })

  it('hides protected drafts on lock, keeps them in memory, and follows lock events', async () => {
    const eventSources: Array<{ listeners: Map<string, EventListener>; emit: (locked: boolean, generation: number) => void }> = []
    class EventSourceStub {
      listeners = new Map<string, EventListener>()
      constructor() { eventSources.push(this) }
      addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener) }
      removeEventListener(type: string) { this.listeners.delete(type) }
      close() {}
      emit(locked: boolean, generation: number) {
        this.listeners.get('encryption-lock')?.(new MessageEvent('encryption-lock', {
          data: JSON.stringify({ vaultId: 'personal', locked, generation }),
        }))
      }
    }
    vi.stubGlobal('EventSource', EventSourceStub)
    const fetchMock = vi.mocked(globalThis.fetch)
    const originalFetch = fetchMock.getMockImplementation()!
    let locked = false
    let generation = 1
    let statusUnavailable = false
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input)
      if (url === '/api/encryption/status') {
        if (statusUnavailable) return Response.json({ error: 'protection status unavailable' }, { status: 503 })
        return Response.json({
          vaultId: 'personal', protected: true, locked, generation, recoveryCodeConfigured: true,
          unlockPasswordMode: 'login', activationEnabled: false, autoLockMinutes: 15, mfaEnabled: false,
        })
      }
      if (url === '/api/repository/tree') return Response.json({ entries: [{ name: 'Secret.md', path: 'Secret.md', type: 'file', protected: true }] })
      if (url === '/api/repository/file?path=Secret.md' && !init?.method) return Response.json({ path: 'Secret.md', content: '# Protected note', version: 'v1' })
      if (url.startsWith('/api/encryption/activity?')) return Response.json({ recorded: true })
      if (url === '/api/encryption/lock' && init?.method === 'POST') {
        locked = true
        generation++
        eventSources[0]?.emit(true, generation)
        return Response.json({ vaultId: 'personal', locked: true, generation })
      }
      if (url === '/api/encryption/unlock' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as { password: string }
        if (body.password !== 'correct vault password') return Response.json({ error: 'vault password is incorrect', code: 'invalid_credentials' }, { status: 401 })
        locked = false
        generation++
        eventSources[0]?.emit(false, generation)
        return Response.json({ vaultId: 'personal', unlocked: true, generation })
      }
      return originalFetch(input, init)
    })

    const authRequired = vi.fn()
    window.addEventListener('varianotes:auth-required', authRequired)
    const view = render(<App />)
    await waitFor(() => expect(eventSources).toHaveLength(1))
    fireEvent.click(await view.findByRole('button', { name: 'Secret' }))
    const editor = await view.findByText('Protected note').then((element) => element.closest('.ProseMirror') as HTMLElement)
    await view.findByRole('toolbar', { name: 'Editor formatting' })
    expect(editor.getAttribute('contenteditable')).toBe('true')
    editor.focus()
    const user = userEvent.setup()
    await user.type(editor, ' private draft', { skipClick: true })
    await waitFor(() => expect(editor.textContent).toContain('private draft'))
    await view.findByText('Changes not saved yet')

    fireEvent.click(view.getByRole('button', { name: 'Lock vault' }))
    await view.findByRole('heading', { name: 'Protected vault is locked' })
    expect(view.container.textContent).not.toContain('private draft')
    expect(view.container.querySelector('.ProseMirror')).toBeNull()
    const storageSnapshot = [localStorage, sessionStorage].flatMap((storage) => Array.from({ length: storage.length }, (_, index) => `${storage.key(index)}=${storage.getItem(storage.key(index)!)}`)).join(' ')
    expect(storageSnapshot).not.toContain('private draft')

    fireEvent.click(view.getAllByRole('button', { name: 'Unlock vault' }).at(-1)!)
    const unlockDialog = await view.findByRole('dialog', { name: 'Unlock protected vault' })
    const password = within(unlockDialog).getByLabelText('Sign-in password')
    fireEvent.change(password, { target: { value: 'wrong vault password' } })
    fireEvent.click(within(unlockDialog).getByRole('button', { name: 'Unlock' }))
    expect(await view.findByRole('alert')).toBeTruthy()
    expect(authRequired).not.toHaveBeenCalled()
    fireEvent.change(password, { target: { value: 'correct vault password' } })
    fireEvent.click(within(unlockDialog).getByRole('button', { name: 'Unlock' }))
    await waitFor(() => expect(view.container.querySelector('.ProseMirror')?.textContent).toContain('private draft'))
    const restoredEditor = view.container.querySelector('.ProseMirror') as HTMLElement
    expect(restoredEditor.textContent).toContain('Protected note')

    statusUnavailable = true
    fireEvent(window, new Event('focus'))
    await view.findByRole('heading', { name: 'Protected vault is locked' })
    expect(view.container.textContent).not.toContain('private draft')
    statusUnavailable = false
    fireEvent.click(view.getAllByRole('button', { name: 'Unlock vault' }).at(-1)!)
    const retryDialog = await view.findByRole('dialog', { name: 'Unlock protected vault' })
    fireEvent.change(within(retryDialog).getByLabelText('Sign-in password'), { target: { value: 'correct vault password' } })
    fireEvent.click(within(retryDialog).getByRole('button', { name: 'Unlock' }))
    await waitFor(() => expect(view.container.querySelector('.ProseMirror')?.textContent).toContain('private draft'))

    generation++
    eventSources[0]?.emit(true, generation)
    await view.findByRole('heading', { name: 'Protected vault is locked' })
    expect(view.container.textContent).not.toContain('private draft')
    window.removeEventListener('varianotes:auth-required', authRequired)
  })
})

describe('Trash bulk actions', () => {
  it('asks how to restore protected content and sends the selected protection choice', async () => {
    const item = { id: '3'.repeat(32), originalPath: 'private/Secret.md', type: 'file' as const, deletedAt: '2026-08-28T12:00:00Z', size: 32, protected: true, protectionChoiceRequired: true }
    let remaining = [item]
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input)
      if (url === '/api/repository/trash') return Response.json({ items: remaining })
      if (url.endsWith('/restore') && init?.method === 'POST') { remaining = []; return Response.json(item) }
      return Response.json({ error: 'unexpected request' }, { status: 500 })
    })
    const view = render(<TrashDialog onChanged={vi.fn(async () => undefined)} onClose={vi.fn()} />)
    fireEvent.click(await view.findByRole('button', { name: 'Restore' }))
    expect(view.getByRole('dialog', { name: 'Restore protected content?' })).toBeTruthy()
    fireEvent.click(view.getByRole('button', { name: 'Decrypt and restore' }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'POST' && JSON.parse(String(init.body)).protectionChoice === 'decrypt')).toBe(true))
  })

  it('reauthenticates before downloading a protected plaintext ZIP and shows the export warning', async () => {
    const fetchMock = vi.mocked(globalThis.fetch)
    const fallback = fetchMock.getMockImplementation()!
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input)
      if (url === '/api/notebooks/personal/health') return Response.json({ localFiles: { state: 'ok' }, writeAccess: { state: 'ok' } })
      if (url === '/api/notebooks/personal/export' && !init?.method) return Response.json({ code: 'export_reauthentication_required' }, { status: 428 })
      if (url === '/api/notebooks/personal/export/reauth' && init?.method === 'POST') return Response.json({ token: 'one-use-export-token' })
      if (url === '/api/notebooks/personal/export' && init?.method === 'POST') return new Response('zip bytes', { status: 200, headers: { 'Content-Type': 'application/zip' } })
      return fallback(input, init)
    })
    const createDescriptor = Object.getOwnPropertyDescriptor(URL, 'createObjectURL')
    const revokeDescriptor = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL')
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: vi.fn(() => 'blob:protected-export') })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() })

    const view = render(<App />)
    const switcher = view.container.querySelector('[aria-haspopup="menu"]') as HTMLButtonElement
    await waitFor(() => expect(switcher).toBeTruthy())
    fireEvent.click(switcher)
    fireEvent.click(view.getByRole('menuitem', { name: 'Manage Vaults' }))
    const card = (await view.findByRole('heading', { name: 'Personal Notes' })).closest('section') as HTMLElement
    fireEvent.click(within(card).getByRole('button', { name: 'Export ZIP' }))

    const dialog = await view.findByRole('dialog', { name: 'Confirm protected export' })
    expect(dialog.textContent).toContain('This ZIP is unencrypted and contains plaintext.')
    expect(dialog.textContent).toContain('Earlier backups may also contain plaintext copies.')
    const password = within(dialog).getByLabelText('Vault password') as HTMLInputElement
    fireEvent.change(password, { target: { value: 'fresh vault password sentinel' } })
    const confirm = within(dialog).getByRole('checkbox')
    const download = within(dialog).getByRole('button', { name: 'Export unencrypted ZIP' }) as HTMLButtonElement
    expect(download.disabled).toBe(true)
    fireEvent.click(confirm)
    expect(download.disabled).toBe(false)
    fireEvent.click(download)

    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url) === '/api/notebooks/personal/export' && init?.method === 'POST')).toBe(true))
    const reauthCall = fetchMock.mock.calls.find(([url]) => String(url) === '/api/notebooks/personal/export/reauth')
    const exportCall = fetchMock.mock.calls.find(([url, init]) => String(url) === '/api/notebooks/personal/export' && init?.method === 'POST')
    expect(reauthCall && JSON.parse(String(reauthCall[1]?.body))).toEqual({ password: 'fresh vault password sentinel' })
    expect(exportCall && JSON.parse(String(exportCall[1]?.body))).toEqual({ token: 'one-use-export-token' })
    expect(String(reauthCall?.[0])).not.toContain('fresh vault password sentinel')
    expect(localStorage.getItem('fresh vault password sentinel')).toBeNull()
    expect(sessionStorage.getItem('fresh vault password sentinel')).toBeNull()
    expect(await view.queryByRole('dialog', { name: 'Confirm protected export' })).toBeNull()

    if (createDescriptor) Object.defineProperty(URL, 'createObjectURL', createDescriptor)
    else Reflect.deleteProperty(URL, 'createObjectURL')
    if (revokeDescriptor) Object.defineProperty(URL, 'revokeObjectURL', revokeDescriptor)
    else Reflect.deleteProperty(URL, 'revokeObjectURL')
  })

  it('selects all items and permanently deletes the complete selection', async () => {
    const items = [
      { id: '1'.repeat(32), originalPath: 'One.md', type: 'file' as const, deletedAt: '2026-08-28T10:00:00Z', size: 10 },
      { id: '2'.repeat(32), originalPath: 'Folder', type: 'directory' as const, deletedAt: '2026-08-28T11:00:00Z', size: 20 },
    ]
    let remaining = [...items]
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input)
      if (url === '/api/repository/trash') return Response.json({ items: remaining })
      if (init?.method === 'DELETE') {
        const item = remaining.find((candidate) => url.endsWith(candidate.id))!
        remaining = remaining.filter((candidate) => candidate.id !== item.id)
        return Response.json(item)
      }
      return Response.json({ error: 'unexpected request' }, { status: 500 })
    })
    const view = render(<TrashDialog onChanged={vi.fn(async () => undefined)} onClose={vi.fn()} />)
    fireEvent.click(await view.findByLabelText('Select all Trash items'))
    fireEvent.click(view.getByRole('button', { name: 'Delete selected' }))
    fireEvent.click(view.getAllByRole('button', { name: 'Permanently delete' }).at(-1)!)
    await waitFor(() => expect(view.getByText('Trash is empty.')).toBeTruthy())
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'DELETE')).toHaveLength(2)
  })
})

type InstallPromptEventForTest = Event & {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}
