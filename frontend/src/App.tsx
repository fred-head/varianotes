import { lazy, Suspense, useCallback, useEffect, useRef, useState, type CSSProperties, type FormEvent, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react'
import { AutoLockController, autoLockOptions, loadAutoLockPreference, parseAutoLockMinutes, saveAutoLockPreference, type AutoLockMinutes } from './app/autoLock'
import { emptyDocumentStats, type DocumentStats } from './app/documentStats'
import { apiFetch, listenForAuthEvents, notifyAuthChanged, setCSRFToken } from './api'

const MarkdownEditor = lazy(() => import('./components/editor/MarkdownEditor').then((module) => ({ default: module.MarkdownEditor })))

type Health = 'checking' | 'online' | 'offline'
type SaveStatus = 'saved' | 'unsaved' | 'saving' | 'error' | 'conflict'
type Theme = 'dark' | 'light'
type TreeNode = { name: string; path: string; type: 'directory' | 'file'; protected?: boolean; protectionInherited?: boolean; children?: TreeNode[] }
type FileResponse = { path: string; content: string; version: string }
type Draft = FileResponse & { savedContent: string }
type DocumentStatsSnapshot = { documentKey: string; stats: DocumentStats }
type MenuState = { entry?: TreeNode; x: number; y: number }
type CleanupAsset = { path: string; size: number }
type CleanupFailure = { path: string; error: string }
type TrashItem = { id:string; originalPath:string; type:'file'|'directory'; deletedAt:string; size:number; protected?:boolean; protectionChoiceRequired?:boolean }
type LinkRewrite = { notePath:string; nextNotePath:string; line:number; before:string; after:string }
type MoveLinkPreview = { source:string; target:string; token:string; rewrites:LinkRewrite[] }
type PendingProtectionMove = { entry: TreeNode; target: string; rewriteToken?: string }
type NotebookInfo = { id: string; name: string }
type EncryptionStatus = { vaultId: string; protected: boolean; locked: boolean; generation: number; recoveryCodeConfigured: boolean; unlockPasswordMode: 'login' | 'separate'; activationEnabled: false; autoLockMinutes: number; mfaEnabled: boolean }
type NotebookCheck = { state:string; message:string }
type NotebookHealth = { localFiles:NotebookCheck; writeAccess:NotebookCheck }
type SearchResult = { path: string; type: 'directory' | 'file' | 'content'; line?: number; excerpt?: string }
type ServerSessionSettings = { lifetimeHours: number; idleHours: number; rememberDays: number }
type SecuritySession = { id: string; clientDescription?: string; current: boolean; lastActivityAt: string; revokedAt?: string }
type NoteTab = { path: string; readOnly: boolean }
type CreateEntryRequest = { type:'file'|'directory'; parent:string; suggested:string }
type InstallPromptEvent = Event & { prompt: () => Promise<void>; userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }> }
const expandedFoldersStorageKey = 'varianotes.expanded-folders'
const themeStorageKey = 'varianotes.theme'
const installPromptDismissedStorageKey = 'varianotes.install-prompt-dismissed'
const notebookSidebarCollapsedStorageKey = 'varianotes.notebook-sidebar-collapsed'

function loadTheme(): Theme {
  const stored = localStorage.getItem(themeStorageKey)
  if (stored === 'dark' || stored === 'light') return stored
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

function loadExpandedFolders(): Set<string> {
  try {
    const value = JSON.parse(localStorage.getItem(expandedFoldersStorageKey) ?? '[]')
    return new Set(Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [])
  } catch {
    return new Set()
  }
}

function parentPath(path: string): string {
  const separator = path.lastIndexOf('/')
  return separator < 0 ? '' : path.slice(0, separator)
}

function baseName(path: string): string {
  return path.split('/').pop() ?? path
}

function findTreeNode(entries: TreeNode[], path: string): TreeNode | undefined {
  for (const entry of entries) {
    if (entry.path === path) return entry
    const child = entry.children && findTreeNode(entry.children, path)
    if (child) return child
  }
}

function markdownPaths(entries: TreeNode[]): string[] {
  const result:string[] = []
  for (const entry of entries) {
    if (entry.type === 'file') result.push(entry.path)
    else if (entry.children) result.push(...markdownPaths(entry.children))
  }
  return result
}

class APIError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) {
    super(message)
  }
}

function messageFrom(error: unknown): string {
  return error instanceof Error ? error.message : 'An unexpected error occurred.'
}

async function responseJSON<T>(response: Response): Promise<T> {
  const body = (await response.json()) as T & { error?: string; code?: string }
  if (!response.ok) throw new APIError(body.error ?? `Request failed (${response.status})`, response.status, body.code)
  return body
}

export function App({ authMode = 'disabled', runningVersion = 'dev', onLoggedOut = () => undefined, initialVaultPassword, onInitialVaultPasswordConsumed }: { authMode?: 'local'|'disabled'; runningVersion?:string; onLoggedOut?:()=>void; initialVaultPassword?:string; onInitialVaultPasswordConsumed?:()=>void } = {}) {
  const [theme, setTheme] = useState<Theme>(loadTheme)
  const [autoLockMinutes, setAutoLockMinutes] = useState<AutoLockMinutes>(() => loadAutoLockPreference(localStorage))
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [notebookSwitcherOpen, setNotebookSwitcherOpen] = useState(false)
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false)
  const [notebookSidebarCollapsed, setNotebookSidebarCollapsed] = useState(() => localStorage.getItem(notebookSidebarCollapsedStorageKey) === 'true')
  const [browserOnline, setBrowserOnline] = useState(() => navigator.onLine)
  const [installPrompt, setInstallPrompt] = useState<InstallPromptEvent>()
  const [installPromptDismissed, setInstallPromptDismissed] = useState(() => localStorage.getItem(installPromptDismissedStorageKey) === 'true')
  const [addNotebookOpen, setAddNotebookOpen] = useState(false)
  const [manageNotebooksOpen, setManageNotebooksOpen] = useState(false)
  const [notebooks, setNotebooks] = useState<NotebookInfo[]>([])
  const [activeNotebookID, setActiveNotebookID] = useState('')
  const [readOnly, setReadOnly] = useState(false)
  const [health, setHealth] = useState<Health>('checking')
  const [notebookName, setNotebookName] = useState('Notebook')
  const [notebookConfigured, setNotebookConfigured] = useState<boolean>()
  const [entries, setEntries] = useState<TreeNode[]>([])
  const [treeLoading, setTreeLoading] = useState(true)
  const [treeError, setTreeError] = useState<string>()
  const [selectedPath, setSelectedPath] = useState<string>()
  const [tabs, setTabs] = useState<NoteTab[]>([])
  const [note, setNote] = useState<FileResponse>()
  const [documentStatsSnapshot, setDocumentStatsSnapshot] = useState<DocumentStatsSnapshot>()
  const [noteLoading, setNoteLoading] = useState(false)
  const [noteError, setNoteError] = useState<string>()
  const [saveStatus, setSaveStatus] = useState<SaveStatus>('saved')
  const [saveError, setSaveError] = useState<string>()
  const [saveConflict, setSaveConflict] = useState<{ server: FileResponse; yourContent: string }>()
  const [conflictLoading, setConflictLoading] = useState(false)
  const [conflictError, setConflictError] = useState<string>()
  const [trashOpen, setTrashOpen] = useState(false)
  const [operationError, setOperationError] = useState<string>()
  const [operationNotice, setOperationNotice] = useState<string>()
  const [operationBusy, setOperationBusy] = useState(false)
  const [createRequest, setCreateRequest] = useState<CreateEntryRequest>()
  const [deleteRequest, setDeleteRequest] = useState<TreeNode>()
  const [selectedItem, setSelectedItem] = useState<TreeNode>()
  const [contextMenu, setContextMenu] = useState<MenuState>()
  const [overflowOpen, setOverflowOpen] = useState(false)
  const [renameEntry, setRenameEntry] = useState<TreeNode>()
  const [renameValue, setRenameValue] = useState('')
  const [moveEntry, setMoveEntry] = useState<TreeNode>()
  const [moveDestination, setMoveDestination] = useState('')
  const [moveLinkPreview, setMoveLinkPreview] = useState<{ entry:TreeNode; preview:MoveLinkPreview }>()
  const [moveProtectionChoice, setMoveProtectionChoice] = useState<PendingProtectionMove>()
  const [encryptionStatus, setEncryptionStatus] = useState<EncryptionStatus>()
  const [vaultLocked, setVaultLocked] = useState(true)
  const [unlockDialogOpen, setUnlockDialogOpen] = useState(false)
  const [unlockError, setUnlockError] = useState<string>()
  const [unlockBusy, setUnlockBusy] = useState(false)
  const [expandedFolders, setExpandedFolders] = useState<Set<string>>(loadExpandedFolders)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchResults, setSearchResults] = useState<SearchResult[]>([])
  const [searchLoading, setSearchLoading] = useState(false)
  const [searchError, setSearchError] = useState<string>()
  const searchController = useRef<AbortController | null>(null)
  const searchRequestGeneration = useRef(0)
  const searchVaultUnlocked = useRef(false)
  const encryptionStatusRef = useRef<EncryptionStatus | undefined>(undefined)
  const loadTreeRef = useRef<() => Promise<void>>(async () => undefined)
  const vaultGeneration = useRef(0)
  const activeNotebookIDRef = useRef('')
  const lastServerActivity = useRef(0)
  const activeDraft = useRef<Draft | undefined>(undefined)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savePromise = useRef<Promise<FileResponse> | undefined>(undefined)
  const draftGeneration = useRef(0)
  const editorActivity = useRef(0)
  const autoLockExpire = useRef<() => void>(() => undefined)
  const autoLockController = useRef<AutoLockController | null>(null)
  const noteHeaderStack = useRef<HTMLDivElement>(null)
  const [noteHeaderHeight, setNoteHeaderHeight] = useState(0)
  const initialUnlockAttempted = useRef(false)
  const discardSearchState = useCallback(() => {
    searchRequestGeneration.current++
    searchController.current?.abort()
    searchController.current = null
    setSearchQuery('')
    setSearchResults([])
    setSearchLoading(false)
    setSearchError(undefined)
  }, [])
  activeNotebookIDRef.current = activeNotebookID
  const activeEncryptionStatus = encryptionStatus?.vaultId === activeNotebookID ? encryptionStatus : undefined

  useEffect(() => listenForAuthEvents(() => {
    setReadOnly(true)
    discardSearchState()
  }, discardSearchState), [discardSearchState])

  useEffect(() => {
    const handleOnline = () => setBrowserOnline(true)
    const handleOffline = () => setBrowserOnline(false)
    const handleInstallPrompt = (event: Event) => {
      event.preventDefault()
      if (!installPromptDismissed) setInstallPrompt(event as InstallPromptEvent)
    }
    const handleInstalled = () => setInstallPrompt(undefined)
    window.addEventListener('online', handleOnline)
    window.addEventListener('offline', handleOffline)
    window.addEventListener('beforeinstallprompt', handleInstallPrompt)
    window.addEventListener('appinstalled', handleInstalled)
    return () => {
      window.removeEventListener('online', handleOnline)
      window.removeEventListener('offline', handleOffline)
      window.removeEventListener('beforeinstallprompt', handleInstallPrompt)
      window.removeEventListener('appinstalled', handleInstalled)
    }
  }, [installPromptDismissed])

  useEffect(() => {
    const query = searchQuery.trim()
    if (!query) return
    const controller = new AbortController()
    const generation = ++searchRequestGeneration.current
    searchController.current = controller
    const timer = globalThis.setTimeout(async () => {
      setSearchLoading(true)
      setSearchError(undefined)
      setSearchResults([])
      try {
        const response = await apiFetch('/api/repository/search', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ query }),
          signal: controller.signal,
        })
        const data = await responseJSON<{ results: SearchResult[] }>(response)
        if (!controller.signal.aborted && searchRequestGeneration.current === generation) setSearchResults(data.results)
      } catch (error) {
        if (!controller.signal.aborted && searchRequestGeneration.current === generation) {
          if (error instanceof APIError && error.status === 423) discardSearchState()
          else setSearchError(messageFrom(error))
        }
      } finally {
        if (!controller.signal.aborted && searchRequestGeneration.current === generation) setSearchLoading(false)
      }
    }, 250)
    return () => {
      globalThis.clearTimeout(timer)
      controller.abort()
      if (searchController.current === controller) searchController.current = null
      searchRequestGeneration.current = generation + 1
    }
  }, [activeNotebookID, searchQuery, discardSearchState])

  useEffect(() => {
    if (!activeNotebookID || typeof EventSource === 'undefined') return
    searchVaultUnlocked.current = false
    vaultGeneration.current = 0
    encryptionStatusRef.current = undefined
    const source = new EventSource(`/api/encryption/lock-events?vaultId=${encodeURIComponent(activeNotebookID)}`)
    const handleLockEvent = (event: Event) => {
      try {
        const update = JSON.parse((event as MessageEvent<string>).data) as { vaultId?: string; locked?: boolean; generation?: number }
        if (update.vaultId !== activeNotebookID) return
        const generation = update.generation ?? 0
        if (generation < vaultGeneration.current) return
        vaultGeneration.current = generation
        const current = encryptionStatusRef.current
        if (current?.vaultId === activeNotebookID) {
          const next = { ...current, locked: Boolean(update.locked), generation }
          encryptionStatusRef.current = next
          setEncryptionStatus(next)
        }
        setVaultLocked(Boolean(update.locked))
        if (!update.locked) {
          searchVaultUnlocked.current = true
          if (encryptionStatusRef.current?.protected) void loadTreeRef.current()
          return
        }
        searchVaultUnlocked.current = false
        lastServerActivity.current = 0
        if (saveTimer.current) {
          clearTimeout(saveTimer.current)
          saveTimer.current = undefined
        }
        discardSearchState()
      } catch {
        // Ignore malformed lock events; the protected API still fails closed.
      }
    }
    source.addEventListener('encryption-lock', handleLockEvent)
    return () => {
      source.removeEventListener('encryption-lock', handleLockEvent)
      source.close()
    }
  }, [activeNotebookID, discardSearchState])

  useEffect(() => {
    if (!operationNotice) return
    const timer = globalThis.setTimeout(() => setOperationNotice(undefined), 8000)
    return () => globalThis.clearTimeout(timer)
  }, [operationNotice])

  const loadTree = useCallback(async () => {
    setTreeLoading(true)
    setTreeError(undefined)
    try {
      const response = await apiFetch('/api/repository/tree')
      const data = await responseJSON<{ entries: TreeNode[] }>(response)
      setEntries(data.entries)
      setNotebookConfigured(true)
      setHealth('online')
    } catch (error) {
      setTreeError(messageFrom(error))
      if (error instanceof APIError && error.status === 423) setVaultLocked(true)
      if (messageFrom(error) === 'repository is not configured') setNotebookConfigured(false)
    } finally {
      setTreeLoading(false)
    }
  }, [])
  loadTreeRef.current = loadTree

  const loadNotebookInfo = useCallback(async () => {
    try {
      const response = await apiFetch('/api/notebook')
      const data = await responseJSON<{ name: string; configured: boolean }>(response)
      setNotebookConfigured(data.configured)
      setNotebookName(data.configured ? data.name || 'Notebook' : 'Notebooks')
    } catch {
      setNotebookName('Notebook')
    }
  }, [])

  const loadNotebooks = useCallback(async () => {
    try {
      const response = await apiFetch('/api/notebooks')
      const data = await responseJSON<{ activeId: string; notebooks: NotebookInfo[] }>(response)
      setNotebooks(data.notebooks)
      setActiveNotebookID(data.activeId)
      const active = data.notebooks.find((notebook) => notebook.id === data.activeId)
      if (active) setNotebookName(active.name)
      return data
    } catch {
      // Directly configured notebooks remain usable without a registry.
    }
  }, [])

  const loadEncryptionStatus = useCallback(async () => {
    if (!activeNotebookID) return
    try {
      const response = await apiFetch('/api/encryption/status')
      const status = await responseJSON<EncryptionStatus>(response)
      if (status.vaultId !== activeNotebookIDRef.current || status.generation < vaultGeneration.current) return
      vaultGeneration.current = status.generation
      encryptionStatusRef.current = status
      setEncryptionStatus(status)
      setVaultLocked(status.locked)
      if (status.protected && !status.locked) void loadTree()
    } catch {
      const current = encryptionStatusRef.current
      if (current?.protected && current.vaultId === activeNotebookIDRef.current) {
        const locked = { ...current, locked: true }
        encryptionStatusRef.current = locked
        setEncryptionStatus(locked)
        setVaultLocked(true)
        lastServerActivity.current = 0
        if (saveTimer.current) {
          clearTimeout(saveTimer.current)
          saveTimer.current = undefined
        }
        discardSearchState()
      }
    }
  }, [activeNotebookID, discardSearchState, loadTree])

  useEffect(() => {
    if (!activeNotebookID) return
    let active = true
    const refresh = async () => {
      if (!active) return
      await loadEncryptionStatus()
    }
    void refresh()
    const onFocus = () => { void refresh() }
    const onVisibility = () => { if (document.visibilityState === 'visible') void refresh() }
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      active = false
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [activeNotebookID, loadEncryptionStatus])

  useEffect(() => {
    const reportActivity = () => {
      const status = encryptionStatusRef.current
      if (!status?.protected || status.locked || status.vaultId !== activeNotebookID || document.visibilityState === 'hidden') return
      const now = Date.now()
      if (now - lastServerActivity.current < 20_000) return
      lastServerActivity.current = now
      void apiFetch(`/api/encryption/activity?vaultId=${encodeURIComponent(activeNotebookID)}`, { method: 'POST' }).then((response) => {
        if (response.status === 423) {
          setVaultLocked(true)
          discardSearchState()
          void loadEncryptionStatus()
        }
      }).catch(() => undefined)
    }
    window.addEventListener('pointerdown', reportActivity, true)
    window.addEventListener('keydown', reportActivity, true)
    window.addEventListener('touchstart', reportActivity, true)
    return () => {
      window.removeEventListener('pointerdown', reportActivity, true)
      window.removeEventListener('keydown', reportActivity, true)
      window.removeEventListener('touchstart', reportActivity, true)
    }
  }, [activeNotebookID, discardSearchState, loadEncryptionStatus])

  useEffect(() => {
    if (!initialVaultPassword || !encryptionStatus || initialUnlockAttempted.current) return
    initialUnlockAttempted.current = true
    onInitialVaultPasswordConsumed?.()
    if (!encryptionStatus.protected || encryptionStatus.unlockPasswordMode !== 'login' || !encryptionStatus.locked) return
    void unlockVault(initialVaultPassword, true)
  }, [initialVaultPassword, encryptionStatus, onInitialVaultPasswordConsumed])

  useEffect(() => {
    const checkHealth = () => apiFetch('/api/health').then((response) => {
      if (!response.ok) throw new Error('health check failed')
      setHealth('online')
    }).catch(() => setHealth('offline'))
    void checkHealth()
    const healthTimer = globalThis.setInterval(() => void checkHealth(), 15_000)
    apiFetch('/api/repository/tree').then(responseJSON<{ entries: TreeNode[] }>).then((data) => {
      setEntries(data.entries)
      setNotebookConfigured(true)
      setHealth('online')
    }).catch((error: unknown) => { const message = messageFrom(error); setTreeError(message); if (message === 'repository is not configured') setNotebookConfigured(false) }).finally(() => setTreeLoading(false))
    apiFetch('/api/notebook').then(responseJSON<{ name: string; configured: boolean }>).then((data) => { setNotebookConfigured(data.configured); setNotebookName(data.configured ? data.name || 'Notebook' : 'Notebooks') }).catch(() => setNotebookName('Notebook'))
    apiFetch('/api/notebooks').then(responseJSON<{ activeId: string; notebooks: NotebookInfo[] }>).then((data) => { setNotebooks(data.notebooks); setActiveNotebookID(data.activeId); const active = data.notebooks.find((notebook) => notebook.id === data.activeId); if (active) setNotebookName(active.name) }).catch(() => undefined)

    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      const draft = activeDraft.current
      if (draft && draft.content !== draft.savedContent) {
        event.preventDefault()
        return
      }
    }
    window.addEventListener('beforeunload', warnBeforeUnload)
    return () => {
      window.removeEventListener('beforeunload', warnBeforeUnload)
      globalThis.clearInterval(healthTimer)
      if (saveTimer.current) clearTimeout(saveTimer.current)
    }
  }, [])

  useEffect(() => {
    localStorage.setItem(expandedFoldersStorageKey, JSON.stringify([...expandedFolders]))
  }, [expandedFolders])

  useEffect(() => {
    document.documentElement.classList.toggle('theme-light', theme === 'light')
    document.documentElement.style.colorScheme = theme
    localStorage.setItem(themeStorageKey, theme)
  }, [theme])

  useEffect(() => {
    const element = noteHeaderStack.current
    if (!element) return
    const updateHeight = () => {
      const nextHeight = Math.ceil(element.getBoundingClientRect().height)
      if (nextHeight > 0) setNoteHeaderHeight((current) => current === nextHeight ? current : nextHeight)
    }
    updateHeight()
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(updateHeight)
    observer?.observe(element)
    window.addEventListener('resize', updateHeight)
    window.visualViewport?.addEventListener('resize', updateHeight)
    return () => {
      observer?.disconnect()
      window.removeEventListener('resize', updateHeight)
      window.visualViewport?.removeEventListener('resize', updateHeight)
    }
  }, [])

  useEffect(() => {
    saveAutoLockPreference(localStorage, autoLockMinutes)
  }, [autoLockMinutes])

  async function saveDraft(): Promise<boolean> {
    if (saveTimer.current) clearTimeout(saveTimer.current)
    if (encryptionStatusRef.current?.protected && encryptionStatusRef.current.locked) return false
    if (savePromise.current) {
      try {
        await savePromise.current
      } catch {
        return false
      }
    }

    const draft = activeDraft.current
    if (!draft || draft.content === draft.savedContent) {
      setSaveStatus('saved')
      return true
    }

    const snapshot = { path: draft.path, content: draft.content, version: draft.version }
    setSaveStatus('saving')
    setSaveError(undefined)
    const operation = apiFetch(`/api/repository/file?path=${encodeURIComponent(snapshot.path)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: snapshot.content, version: snapshot.version }),
    }).then(responseJSON<FileResponse>)
    savePromise.current = operation

    try {
      const saved = await operation
      const current = activeDraft.current
      if (current?.path === snapshot.path) {
        current.version = saved.version
        current.savedContent = snapshot.content
      }
    } catch (error) {
		const isConflict = error instanceof APIError && error.status === 409
		if (error instanceof TypeError) setHealth('offline')
		setSaveStatus(isConflict ? 'conflict' : 'error')
      setSaveError(messageFrom(error))
		if (isConflict) {
			try {
				const server = await responseJSON<FileResponse>(await apiFetch(`/api/repository/file?path=${encodeURIComponent(snapshot.path)}`))
				setSaveConflict({ server, yourContent: snapshot.content })
			} catch { /* retry remains available without discarding the draft */ }
		}
      return false
    } finally {
      savePromise.current = undefined
    }

    const current = activeDraft.current
    if (current?.path === snapshot.path && current.content !== current.savedContent) return saveDraft()
    setSaveStatus('saved')
    return true
  }

  async function unlockVault(password: string, openDialogOnFailure = false) {
    if (!encryptionStatusRef.current?.protected || !password) return
    setUnlockBusy(true)
    setUnlockError(undefined)
    try {
      const result = await responseJSON<{ vaultId: string; unlocked: boolean; generation: number }>(await apiFetch('/api/encryption/unlock', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password }),
      }))
      if (result.vaultId !== activeNotebookIDRef.current || result.generation < vaultGeneration.current) {
        throw new Error('Vault lock state changed. Try unlocking again.')
      }
      vaultGeneration.current = result.generation
      const current = encryptionStatusRef.current
      if (current?.vaultId === result.vaultId) {
        const next = { ...current, locked: !result.unlocked, generation: result.generation }
        encryptionStatusRef.current = next
        setEncryptionStatus(next)
      }
      setVaultLocked(!result.unlocked)
      lastServerActivity.current = 0
      setUnlockDialogOpen(false)
      setUnlockError(undefined)
    } catch (error) {
      setUnlockError(messageFrom(error))
      if (openDialogOnFailure) setUnlockDialogOpen(true)
    } finally {
      setUnlockBusy(false)
    }
  }

  async function lockVault() {
    if (!encryptionStatusRef.current?.protected || encryptionStatusRef.current.locked) return
    if (saveTimer.current) {
      clearTimeout(saveTimer.current)
      saveTimer.current = undefined
    }
    setOperationError(undefined)
    try {
      const result = await responseJSON<{ vaultId: string; locked: boolean; generation: number }>(await apiFetch('/api/encryption/lock', { method: 'POST' }))
      if (result.vaultId !== activeNotebookIDRef.current || result.generation < vaultGeneration.current) return
      vaultGeneration.current = result.generation
      const current = encryptionStatusRef.current
      if (current?.vaultId === result.vaultId) {
        const next = { ...current, locked: result.locked, generation: result.generation }
        encryptionStatusRef.current = next
        setEncryptionStatus(next)
      }
      setVaultLocked(result.locked)
      lastServerActivity.current = 0
      discardSearchState()
    } catch (error) {
      setOperationError(messageFrom(error))
    }
  }
  async function applySaveConflict(action: 'keep_mine' | 'use_server' | 'edit_combined', content?: string) {
    const pending = saveConflict
    if (!pending) return
    setConflictError(undefined)
    setConflictLoading(true)
    try {
      let resolved = pending.server
      if (action !== 'use_server') {
        resolved = await responseJSON<FileResponse>(await apiFetch('/api/repository/file/resolve-conflict', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ path: pending.server.path, expectedVersion: pending.server.version, content: action === 'edit_combined' ? content ?? '' : pending.yourContent }),
        }))
      }
      activeDraft.current = { ...resolved, savedContent: resolved.content }
      setNote(resolved)
      setSelectedPath(resolved.path)
      setSelectedItem(findTreeNode(entries, resolved.path) ?? { name: baseName(resolved.path), path: resolved.path, type: 'file' })
      setTabs((current) => current.some((tab) => tab.path === resolved.path) ? current : [...current, { path: resolved.path, readOnly: false }])
      setReadOnly(false)
      setSaveConflict(undefined)
      setSaveStatus('saved')
      setSaveError(undefined)
    } catch (error) {
      setConflictError(messageFrom(error))
      try {
        const server = await responseJSON<FileResponse>(await apiFetch(`/api/repository/file?path=${encodeURIComponent(pending.server.path)}`))
        setSaveConflict({ server, yourContent: pending.yourContent })
      } catch { /* Both versions remain available in memory. */ }
    } finally {
      setConflictLoading(false)
    }
  }

  async function activateCreatedVault() {
    activeDraft.current = undefined
    setTabs([])
    setSelectedPath(undefined)
    setSelectedItem(undefined)
    setNote(undefined)
    setNoteError(undefined)
    setSaveStatus('saved')
    setExpandedFolders(new Set())
    await loadTree()
    await loadNotebookInfo()
    await loadNotebooks()
  }

  async function refreshNotebookRegistrations() {
    const data = await loadNotebooks()
    if (data?.activeId) {
      await loadNotebookInfo()
      return
    }
    activeDraft.current = undefined
    setTabs([])
    setSelectedPath(undefined)
    setSelectedItem(undefined)
    setNote(undefined)
    setEntries([])
    setNotebookConfigured(false)
    setNotebookName('Notebooks')
    setTreeError(undefined)
  }

  async function switchNotebook(notebook: NotebookInfo) {
    setMobileNavigationOpen(false)
    setNotebookSwitcherOpen(false)
    setOperationNotice(undefined)
    if (notebook.id === activeNotebookID) return
    if (!(await saveDraft())) return
    setOperationBusy(true)
    setOperationError(undefined)
    try {
      const response = await apiFetch(`/api/notebooks/${encodeURIComponent(notebook.id)}/activate`, { method: 'POST' })
      await responseJSON<NotebookInfo>(response)
      discardSearchState()
      activeDraft.current = undefined
      setTabs([])
      setSelectedPath(undefined)
      setSelectedItem(undefined)
      setNote(undefined)
      setNoteError(undefined)
      setEntries([])
      setExpandedFolders(new Set())
      setNotebookName(notebook.name)
      setActiveNotebookID(notebook.id)
      await loadTree()
    } catch (error) {
      setOperationError(messageFrom(error))
    } finally {
      setOperationBusy(false)
    }
  }

  function updateReadOnly(value: boolean, path = selectedPath) {
    setReadOnly(value)
    if (path) setTabs((current) => current.map((tab) => tab.path === path ? { ...tab, readOnly: value } : tab))
  }

  async function openNote(path: string, disposition: 'current' | 'new' = 'current') {
    if (path === selectedPath) return
    if (encryptionStatusRef.current?.protected && encryptionStatusRef.current.locked) {
      setUnlockDialogOpen(true)
      return
    }
    if (!(await saveDraft())) return
    setNoteLoading(true)
    setNoteError(undefined)
    setSaveError(undefined)
    try {
      const response = await apiFetch(`/api/repository/file?path=${encodeURIComponent(path)}`)
      const loaded = await responseJSON<FileResponse>(response)
      const existingTab = tabs.find((tab) => tab.path === path)
      setTabs((current) => {
        if (current.some((tab) => tab.path === path)) return current
        const nextTab = { path, readOnly: false }
        if (disposition === 'new' || !selectedPath || current.length === 0) return [...current, nextTab]
        return current.map((tab) => tab.path === selectedPath ? nextTab : tab)
      })
      activeDraft.current = { ...loaded, savedContent: loaded.content }
      setNote(loaded)
      setSelectedPath(path)
      const treeEntry = findTreeNode(entries, path)
      if (treeEntry) setSelectedItem(treeEntry)
      setExpandedFolders((current) => {
        const next = new Set(current)
        let folder = parentPath(path)
        while (folder) {
          next.add(folder)
          folder = parentPath(folder)
        }
        return next
      })
      updateReadOnly(existingTab?.readOnly ?? false, path)
      setSaveStatus('saved')
    } catch (error) {
      setNoteError(messageFrom(error))
      if (error instanceof APIError && error.status === 423) {
        setVaultLocked(true)
        discardSearchState()
      }
    } finally {
      setNoteLoading(false)
    }
  }

  async function closeTab(path: string) {
    const index = tabs.findIndex((tab) => tab.path === path)
    if (index < 0) return
    if (path !== selectedPath) {
      setTabs((current) => current.filter((tab) => tab.path !== path))
      return
    }
    if (!(await saveDraft())) return
    const remaining = tabs.filter((tab) => tab.path !== path)
    const next = remaining[Math.min(index, remaining.length - 1)]
    setTabs(remaining)
    activeDraft.current = undefined
    setNote(undefined)
    setSelectedPath(undefined)
    setSaveStatus('saved')
    setSaveError(undefined)
    if (next) await openNote(next.path)
  }

  async function activateTab(path: string) {
    if (path !== selectedPath) await openNote(path)
  }

  function updateDraft(markdown: string) {
    const draft = activeDraft.current
    if (!draft || draft.content === markdown) return
    draft.content = markdown
    draftGeneration.current += 1
    setNote((current) => current?.path === draft.path ? { ...current, content: markdown } : current)
    editorActivity.current += 1
    autoLockController.current?.activity()
    setSaveStatus('unsaved')
    setSaveError(undefined)
    if (saveTimer.current) clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => { void saveDraft() }, 800)
  }

  function createEntry(type: 'file' | 'directory', relativeTo: TreeNode | undefined = selectedItem) {
    const parent = relativeTo?.type === 'directory' ? relativeTo.path : relativeTo ? parentPath(relativeTo.path) : ''
    const suggested = type === 'file' ? 'New note.md' : 'New folder'
    setCreateRequest({ type, parent, suggested })
  }

  async function submitCreateEntry(value:string) {
    const request = createRequest
    if (!request) return
    let name = value.trim()
    if (!name) return
    if (name.includes('/') || name.includes('\\')) {
      setOperationError('Enter a name only. Choose the destination folder in the tree.')
      return
    }
    if (request.type === 'file' && !name.toLowerCase().endsWith('.md')) name += '.md'
    const path = request.parent ? `${request.parent}/${name}` : name
    setCreateRequest(undefined)
    setOperationBusy(true)
    setOperationError(undefined)
    try {
      const response = await apiFetch('/api/repository/entries', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path, type:request.type }) })
      await responseJSON<{ path: string; type: string }>(response)
      if (request.parent) setExpandedFolders((current) => new Set(current).add(request.parent))
      await loadTree()
      setSelectedItem({ name: baseName(path), path, type:request.type })
      if (request.type === 'file') await openNote(path)
    } catch (error) {
      setOperationError(messageFrom(error))
    } finally {
      setOperationBusy(false)
    }
  }

  async function performMove(entry: TreeNode, target: string, rewriteToken?:string, protectionChoice?:'keep'|'decrypt') {
    if (target === entry.path) return
    if (!(await saveDraft())) return
    setOperationBusy(true)
    setOperationError(undefined)
    try {
      if (!rewriteToken) {
        const previewResponse = await apiFetch('/api/repository/move/preview', { method:'POST', headers:{ 'Content-Type':'application/json' }, body:JSON.stringify({ source:entry.path, target }) })
        const preview = await responseJSON<MoveLinkPreview>(previewResponse)
        if (preview.rewrites.length > 0) {
          setMoveLinkPreview({ entry, preview })
          return
        }
      }
      const response = await apiFetch('/api/repository/move', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ source: entry.path, target, ...(rewriteToken ? { rewriteToken } : {}), ...(protectionChoice ? { protectionChoice } : {}) }) })
      await responseJSON<{ path: string }>(response)
      const affectedPath = selectedPath === entry.path || selectedPath?.startsWith(`${entry.path}/`)
        ? target + selectedPath.slice(entry.path.length)
        : undefined
      const affectedReadOnly = affectedPath ? tabs.find((tab) => tab.path === selectedPath)?.readOnly : undefined
      setTabs((current) => current.map((tab) => tab.path === entry.path || tab.path.startsWith(`${entry.path}/`)
        ? { ...tab, path: target + tab.path.slice(entry.path.length) }
        : tab))
      if (selectedItem && (selectedItem.path === entry.path || selectedItem.path.startsWith(`${entry.path}/`))) {
        const movedSelectionPath = target + selectedItem.path.slice(entry.path.length)
        setSelectedItem({ ...selectedItem, path: movedSelectionPath, name: baseName(movedSelectionPath) })
      }
      setExpandedFolders((current) => {
        const next = new Set<string>()
        for (const folder of current) {
          next.add(folder === entry.path || folder.startsWith(`${entry.path}/`) ? target + folder.slice(entry.path.length) : folder)
        }
        return next
      })
      if (affectedPath) {
        activeDraft.current = undefined
        setNote(undefined)
        setSelectedPath(undefined)
      }
      await loadTree()
      if (affectedPath) {
        await openNote(affectedPath)
        updateReadOnly(affectedReadOnly ?? false, affectedPath)
      }
    } catch (error) {
      if (error instanceof APIError && error.code === 'move_protection_choice_required') {
        setMoveProtectionChoice({ entry, target, rewriteToken })
      } else {
        setOperationError(messageFrom(error))
      }
    } finally {
      setOperationBusy(false)
    }
  }

  async function confirmMoveLinkRewrites() {
    const pending = moveLinkPreview
    if (!pending) return
    setMoveLinkPreview(undefined)
    await performMove(pending.entry, pending.preview.target, pending.preview.token)
  }

  async function confirmProtectionMove(choice: 'keep' | 'decrypt') {
    const pending = moveProtectionChoice
    if (!pending) return
    setMoveProtectionChoice(undefined)
    await performMove(pending.entry, pending.target, pending.rewriteToken, choice)
  }

  function beginRename(entry: TreeNode) {
    setContextMenu(undefined)
    setOverflowOpen(false)
    setRenameEntry(entry)
    setRenameValue(entry.name)
  }

  async function commitRename() {
    const entry = renameEntry
    let name = renameValue.trim()
    if (!entry || !name) return
    if (name.includes('/') || name.includes('\\')) {
      setOperationError('A name cannot contain path separators.')
      return
    }
    if (entry.type === 'file' && !name.toLowerCase().endsWith('.md')) name += '.md'
    setRenameEntry(undefined)
    const parent = parentPath(entry.path)
    await performMove(entry, parent ? `${parent}/${name}` : name)
  }

  function beginMove(entry: TreeNode) {
    setContextMenu(undefined)
    setOverflowOpen(false)
    setMoveEntry(entry)
    setMoveDestination(parentPath(entry.path))
  }

  async function confirmMove() {
    if (!moveEntry) return
    const entry = moveEntry
    const target = moveDestination ? `${moveDestination}/${entry.name}` : entry.name
    setMoveEntry(undefined)
    await performMove(entry, target)
  }

  async function deleteEntry(entry: TreeNode) {
    if (!(await saveDraft())) return
    setOperationBusy(true)
    setOperationError(undefined)
    try {
      const response = await apiFetch(`/api/repository/entry?path=${encodeURIComponent(entry.path)}`, { method: 'DELETE' })
      if (!response.ok) await responseJSON<never>(response)
      const activeDeleted = selectedPath === entry.path || selectedPath?.startsWith(`${entry.path}/`)
      const activeIndex = tabs.findIndex((tab) => tab.path === selectedPath)
      const remainingTabs = tabs.filter((tab) => tab.path !== entry.path && !tab.path.startsWith(`${entry.path}/`))
      setTabs(remainingTabs)
      if (activeDeleted) {
        activeDraft.current = undefined
        setNote(undefined)
        setSelectedPath(undefined)
        setSaveStatus('saved')
        setSaveError(undefined)
      }
      if (selectedItem?.path === entry.path || selectedItem?.path.startsWith(`${entry.path}/`)) setSelectedItem(undefined)
      setExpandedFolders((current) => new Set([...current].filter((folder) => folder !== entry.path && !folder.startsWith(`${entry.path}/`))))
      await loadTree()
      if (activeDeleted && remainingTabs.length > 0) {
        const next = remainingTabs[Math.min(Math.max(activeIndex, 0), remainingTabs.length - 1)]
        await openNote(next.path)
      }
    } catch (error) {
      setOperationError(messageFrom(error))
    } finally {
      setOperationBusy(false)
    }
  }

  function requestDeleteEntry(entry:TreeNode) {
    setContextMenu(undefined)
    setOverflowOpen(false)
    setDeleteRequest(entry)
  }

  function selectEntry(entry: TreeNode, disposition: 'current' | 'new' = 'current') {
    setSelectedItem(entry)
    setOverflowOpen(false)
    if (entry.type === 'file') {
      setMobileNavigationOpen(false)
      void openNote(entry.path, disposition)
    }
  }

  function selectSearchResult(result: SearchResult) {
    if (result.type !== 'directory') {
      setMobileNavigationOpen(false)
      const entry = findTreeNode(entries, result.path)
      if (entry) setSelectedItem(entry)
      setExpandedFolders((current) => {
        const next = new Set(current)
        let folder = parentPath(result.path)
        while (folder) {
          next.add(folder)
          folder = parentPath(folder)
        }
        return next
      })
      void openNote(result.path)
      return
    }
    const entry = findTreeNode(entries, result.path)
    if (entry) setSelectedItem(entry)
    setExpandedFolders((current) => {
      const next = new Set(current)
      let folder = result.path
      while (folder) {
        next.add(folder)
        folder = parentPath(folder)
      }
      return next
    })
    setSearchQuery('')
  }

  function openInternalNoteLink(path:string, disposition:'current'|'new') {
    const entry = findTreeNode(entries, path)
    if (!entry || entry.type !== 'file') {
      setOperationError(`Linked note not found: ${path}`)
      return
    }
    setOperationError(undefined)
    setSelectedItem(entry)
    setExpandedFolders((current) => {
      const next = new Set(current)
      let folder = parentPath(path)
      while (folder) { next.add(folder); folder = parentPath(folder) }
      return next
    })
    void openNote(path, disposition)
  }

  function showContextMenu(entry: TreeNode, event: MouseEvent) {
    event.preventDefault()
    setSelectedItem(entry)
    setOverflowOpen(false)
    setContextMenu({ entry, x: Math.max(8, Math.min(event.clientX, window.innerWidth - 176)), y: Math.max(8, Math.min(event.clientY, window.innerHeight - 132)) })
  }

  function showRootContextMenu(event: MouseEvent) {
    event.preventDefault()
    setSelectedItem(undefined)
    setOverflowOpen(false)
    setContextMenu({ x: Math.max(8, Math.min(event.clientX, window.innerWidth - 176)), y: Math.max(8, Math.min(event.clientY, window.innerHeight - 132)) })
  }

  async function logout() {
    setOperationError(undefined)
    try {
      await responseJSON(await apiFetch('/api/auth/logout', { method: 'POST' }))
      onLoggedOut()
    } catch (caught) {
      setOperationError(messageFrom(caught))
    }
  }

  function openEntryInNewTab(entry: TreeNode) {
    setContextMenu(undefined)
    setOverflowOpen(false)
    setSelectedItem(entry)
    setMobileNavigationOpen(false)
    void openNote(entry.path, 'new')
  }

  function toggleFolder(path: string) {
    setExpandedFolders((current) => {
      const next = new Set(current)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  useEffect(() => {
    autoLockExpire.current = () => {
      const path = selectedPath
      const activity = editorActivity.current
      if (!path || readOnly) return
      void saveDraft().then((saved) => {
        if (saved && activeDraft.current?.path === path && editorActivity.current === activity) updateReadOnly(true, path)
      })
    }
  })

  useEffect(() => {
    const controller = new AutoLockController(() => autoLockExpire.current())
    autoLockController.current = controller
    return () => controller.dispose()
  }, [])

  useEffect(() => {
    autoLockController.current?.update(autoLockMinutes, Boolean(selectedPath) && !readOnly)
  }, [autoLockMinutes, readOnly, selectedPath])

  useEffect(() => {
    const handleTabKeys = (event: globalThis.KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'w' && selectedPath) {
        event.preventDefault()
        void closeTab(selectedPath)
        return
      }
      if (!event.ctrlKey || event.key !== 'Tab' || tabs.length < 2) return
      event.preventDefault()
      const currentIndex = Math.max(0, tabs.findIndex((tab) => tab.path === selectedPath))
      const direction = event.shiftKey ? -1 : 1
      const nextIndex = (currentIndex + direction + tabs.length) % tabs.length
      void activateTab(tabs[nextIndex].path)
    }
    window.addEventListener('keydown', handleTabKeys)
    return () => window.removeEventListener('keydown', handleTabKeys)
  })

  async function toggleReadOnly() {
    if (readOnly) {
      updateReadOnly(false)
      return
    }
    if (await saveDraft()) updateReadOnly(true)
  }

  const selectedFolderPath = selectedItem?.type === 'directory' ? selectedItem.path : selectedItem ? parentPath(selectedItem.path) : ''
  const createInName = selectedFolderPath ? baseName(selectedFolderPath) : notebookName

  async function installApplication() {
    if (!installPrompt) return
    await installPrompt.prompt()
    await installPrompt.userChoice
    setInstallPrompt(undefined)
  }

  function dismissInstallPrompt() {
    localStorage.setItem(installPromptDismissedStorageKey, 'true')
    setInstallPromptDismissed(true)
    setInstallPrompt(undefined)
  }

  function toggleNotebookSidebar() {
    setNotebookSidebarCollapsed((collapsed) => {
      const next = !collapsed
      try {
        if (next) localStorage.setItem(notebookSidebarCollapsedStorageKey, 'true')
        else localStorage.removeItem(notebookSidebarCollapsedStorageKey)
      } catch { /* Keep the in-memory control usable when browser storage is unavailable. */ }
      return next
    })
  }

  const editorDocumentKey = note ? `${note.path}:${readOnly ? 'read' : 'edit'}` : ''
  const visibleDocumentStats = documentStatsSnapshot?.documentKey === editorDocumentKey ? documentStatsSnapshot.stats : emptyDocumentStats
  const publishDocumentStats = useCallback((stats: DocumentStats) => {
    setDocumentStatsSnapshot((previous) => previous?.documentKey === editorDocumentKey && previous.stats === stats
      ? previous
      : { documentKey: editorDocumentKey, stats })
  }, [editorDocumentKey])

  return (
    <div className="varianotes-app-shell flex min-h-0 flex-col bg-zinc-950 text-zinc-100 lg:flex-row">
      {mobileNavigationOpen && <button type="button" aria-label="Close notebook navigation" className="fixed inset-0 z-30 bg-black/65 lg:hidden" onClick={() => setMobileNavigationOpen(false)} />}
      <aside id="notebook-navigation" aria-label="Notebook navigation" className={`fixed inset-y-0 left-0 z-40 flex w-[min(20rem,calc(100vw-3rem))] shrink-0 flex-col border-r border-zinc-800 bg-zinc-900 shadow-2xl transition-[transform,width] duration-200 lg:static lg:z-auto lg:translate-x-0 lg:bg-zinc-900/60 lg:shadow-none ${notebookSidebarCollapsed ? 'lg:w-12' : 'lg:w-80'} ${mobileNavigationOpen ? 'translate-x-0' : '-translate-x-full'}`}>
        {notebookSidebarCollapsed && <div className="hidden flex-1 flex-col items-center pt-2 lg:flex"><button type="button" aria-label="Expand notebook sidebar" aria-controls="notebook-navigation-content" aria-expanded="false" onClick={toggleNotebookSidebar} className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md border border-zinc-700 text-lg text-zinc-300 hover:bg-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500" title="Expand notebook sidebar">›</button></div>}
        <div id="notebook-navigation-content" className={`flex min-h-0 flex-1 flex-col ${notebookSidebarCollapsed ? 'lg:hidden' : ''}`}>
        <header className="relative border-b border-zinc-800 px-5 py-5"><div className="flex items-center justify-between gap-3"><div className="min-w-0 flex-1"><p className="text-xs font-semibold uppercase tracking-[0.22em] text-amber-400">VariaNotes</p><button type="button" aria-haspopup="menu" aria-expanded={notebookSwitcherOpen} onClick={() => setNotebookSwitcherOpen((open) => !open)} className="mt-1 flex min-h-9 max-w-full items-center gap-2 rounded-md pr-2 text-left text-lg font-semibold outline-none hover:text-amber-200 focus-visible:ring-2 focus-visible:ring-amber-500"><span className="truncate">Vaults</span><span aria-hidden="true" className="text-sm text-zinc-500">▾</span></button></div><div className="flex items-center gap-2"><button type="button" onClick={() => setSettingsOpen(true)} className="rounded-md border border-zinc-700 p-2 text-zinc-300 hover:bg-zinc-800 hover:text-white" aria-label="Settings" title="Settings"><SettingsIcon /></button>{authMode === 'local' && <button type="button" onClick={() => void logout()} className="rounded-md border border-zinc-700 p-2 text-zinc-300 hover:bg-zinc-800 hover:text-white" aria-label="Sign out" title="Sign out"><LogoutIcon /></button>}<button type="button" onClick={() => setTheme((current) => current === 'dark' ? 'light' : 'dark')} className="rounded-md border border-zinc-700 px-2.5 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800 hover:text-white" aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`} title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}><span aria-hidden="true">{theme === 'dark' ? '☀' : '☾'}</span></button><button type="button" aria-label="Collapse notebook sidebar" aria-controls="notebook-navigation-content" aria-expanded="true" onClick={toggleNotebookSidebar} className="hidden h-10 w-10 shrink-0 items-center justify-center rounded-md border border-zinc-700 text-lg text-zinc-300 hover:bg-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500 lg:flex" title="Collapse notebook sidebar">‹</button></div></div>{notebookSwitcherOpen && <><button type="button" aria-label="Close notebook switcher" className="fixed inset-0 z-20 cursor-default" onClick={() => setNotebookSwitcherOpen(false)} /><div role="menu" aria-label="Vaults" className="absolute top-[4.8rem] right-3 left-3 z-30 rounded-lg border border-zinc-700 bg-zinc-900 p-1.5 shadow-2xl">{notebooks.map((notebook) => <button key={notebook.id} type="button" role="menuitemradio" aria-checked={notebook.id === activeNotebookID} onClick={() => void switchNotebook(notebook)} className="flex min-h-11 w-full items-center gap-2 rounded-md px-3 text-left text-sm text-zinc-200 hover:bg-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500"><span className="w-4 text-amber-400" aria-hidden="true">{notebook.id === activeNotebookID ? '✓' : ''}</span><span className="truncate">{notebook.name}</span></button>)}<div className="my-1 border-t border-zinc-700" /><button type="button" role="menuitem" onClick={() => { setNotebookSwitcherOpen(false); setAddNotebookOpen(true) }} className="min-h-11 w-full rounded-md px-3 text-left text-sm text-amber-300 hover:bg-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500">+ Add Vault</button><button type="button" role="menuitem" onClick={() => { setNotebookSwitcherOpen(false); setManageNotebooksOpen(true) }} className="min-h-11 w-full rounded-md px-3 text-left text-sm text-zinc-300 hover:bg-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500">Manage Vaults</button></div></>}</header>
        <div className="relative grid grid-cols-[1fr_1fr_auto_auto_auto] gap-1.5 border-b border-zinc-800 p-3">
          <TreeAction disabled={operationBusy || notebookConfigured === false} onClick={() => void createEntry('file')}>New Note</TreeAction>
          <TreeAction disabled={operationBusy || notebookConfigured === false} onClick={() => void createEntry('directory')}>New Folder</TreeAction>
          <TreeAction label="Refresh tree" disabled={operationBusy || notebookConfigured === false} onClick={() => void loadTree()}>↻</TreeAction>
          <TreeAction label="Selected item actions" disabled={operationBusy || !selectedItem} onClick={() => setOverflowOpen((open) => !open)}>•••</TreeAction>
          <TreeAction label="Open Trash" disabled={operationBusy || notebookConfigured === false} onClick={() => setTrashOpen(true)}>♲</TreeAction>
          {overflowOpen && <button type="button" className="fixed inset-0 z-20 cursor-default" aria-label="Close action menu" onClick={() => setOverflowOpen(false)} />}
          {overflowOpen && selectedItem && <div className="absolute top-12 right-3 z-30"><ActionMenu entry={selectedItem} onNewNote={(entry) => { setOverflowOpen(false); createEntry('file', entry) }} onNewFolder={(entry) => { setOverflowOpen(false); createEntry('directory', entry) }} onOpenNewTab={openEntryInNewTab} onRename={beginRename} onMove={beginMove} onDelete={requestDeleteEntry} /></div>}
        </div>
        {notebookConfigured !== false && <div className="border-b border-zinc-800 px-3 py-2 text-xs text-zinc-500">Create in: <span className="text-zinc-300">{createInName}</span></div>}
        {notebookConfigured !== false && <div className="border-b border-zinc-800 p-3">
          <div className="relative">
            <span aria-hidden="true" className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-sm text-zinc-500">⌕</span>
            <input type="search" value={searchQuery} onChange={(event) => { searchController.current?.abort(); searchController.current = null; searchRequestGeneration.current++; setSearchResults([]); setSearchLoading(false); setSearchError(undefined); setSearchQuery(event.target.value) }} placeholder="Search this notebook" aria-label="Search this notebook" className="min-h-10 w-full rounded-md border border-zinc-700 bg-zinc-950 py-2 pr-9 pl-9 text-sm text-zinc-200 outline-none placeholder:text-zinc-600 focus:border-amber-500 focus:ring-1 focus:ring-amber-500" />
            {searchQuery && <button type="button" onClick={discardSearchState} aria-label="Clear search" className="absolute top-1/2 right-1.5 min-h-8 min-w-8 -translate-y-1/2 rounded text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200">×</button>}
          </div>
        </div>}
        <div className="max-h-80 overflow-y-auto p-3 lg:max-h-none lg:flex-1">
          {operationError && <div className="mb-3 rounded-lg border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{operationError}</div>}
          {operationNotice && <div role="status" className="mb-3 flex items-start justify-between gap-2 rounded-lg border border-emerald-900/70 bg-emerald-950/20 p-3 text-sm text-emerald-200"><span>{operationNotice}</span><button type="button" aria-label="Dismiss notification" onClick={() => setOperationNotice(undefined)} className="-m-1 min-h-8 min-w-8 shrink-0 rounded text-emerald-300 hover:bg-emerald-900/40">×</button></div>}
          {searchQuery.trim() && <SearchResults query={searchQuery.trim()} results={searchResults} loading={searchLoading} error={searchError} onSelect={selectSearchResult} />}
          {!searchQuery.trim() && <>
          {treeLoading && <SidebarMessage>Loading vault…</SidebarMessage>}
          {notebookConfigured === false && <div className="rounded-lg border border-zinc-700 bg-zinc-950/40 p-4 text-sm text-zinc-300"><p className="font-medium text-zinc-100">No vault yet</p><p className="mt-1 text-xs leading-5 text-zinc-500">Create a vault or adopt an existing folder to start taking notes.</p><button type="button" className="mt-3 min-h-10 rounded-md bg-amber-500 px-3 text-xs font-semibold text-zinc-950 hover:bg-amber-400" onClick={() => setAddNotebookOpen(true)}>Add Vault</button></div>}
          {treeError && notebookConfigured !== false && <div className="rounded-lg border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200"><p>{treeError}</p><button className="mt-3 rounded-md bg-red-900/60 px-3 py-1.5 text-xs hover:bg-red-800" onClick={() => void loadTree()}>Try again</button></div>}
          {!treeLoading && !treeError && <button type="button" onContextMenu={showRootContextMenu} className={`mb-1 flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm ${!selectedItem ? 'bg-amber-400/15 text-amber-200' : 'text-zinc-400 hover:bg-zinc-800 hover:text-white'}`} onClick={() => { setSelectedItem(undefined); setOverflowOpen(false) }}><span aria-hidden="true">⌂</span><span className="truncate">{notebookName}</span></button>}
          {!treeLoading && !treeError && entries.length === 0 && <SidebarMessage>No Markdown files found.</SidebarMessage>}
          {!treeLoading && !treeError && entries.length > 0 && <nav aria-label="Notebook notes"><ul className="space-y-0.5">{entries.map((entry) => <TreeEntry key={entry.path} entry={entry} selectedPath={selectedItem?.path} expandedFolders={expandedFolders} renamePath={renameEntry?.path} renameValue={renameValue} onSelect={selectEntry} onToggleFolder={toggleFolder} onContextMenu={showContextMenu} onRenameValue={setRenameValue} onRenameCommit={() => void commitRename()} onRenameCancel={() => setRenameEntry(undefined)} />)}</ul></nav>}
          </>}
        </div>
        </div>
      </aside>

      <main className="varianotes-note-scroll flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto" style={{ '--editor-toolbar-top': `${noteHeaderHeight || (tabs.length > 0 ? 99 : 55)}px` } as CSSProperties}>
        <div ref={noteHeaderStack} className="varianotes-note-header-stack sticky top-0 z-10 bg-zinc-950/90 backdrop-blur">
        <header className="flex items-center justify-between gap-2 border-b border-zinc-800 px-3 py-3 sm:gap-4 sm:px-8">
          <div className="flex min-w-0 items-center gap-2"><button type="button" aria-label="Open notebook navigation" aria-controls="notebook-navigation" aria-expanded={mobileNavigationOpen} onClick={() => setMobileNavigationOpen(true)} className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-zinc-700 text-lg text-zinc-300 hover:bg-zinc-800 lg:hidden">☰</button><p className="min-w-0 truncate text-sm text-zinc-400">{selectedPath ?? 'Select a Markdown file'}</p></div>
          <div className="flex shrink-0 items-center gap-2">{activeEncryptionStatus?.protected && <><span className={`hidden text-xs sm:inline ${vaultLocked ? 'text-amber-300' : 'text-emerald-300'}`}>{vaultLocked ? 'Vault locked' : 'Vault unlocked'}</span><button type="button" onClick={() => vaultLocked ? setUnlockDialogOpen(true) : void lockVault()} className="min-h-9 rounded-md border border-zinc-700 px-3 text-xs font-semibold text-zinc-200 hover:bg-zinc-800">{vaultLocked ? 'Unlock vault' : 'Lock vault'}</button></>}{selectedPath && <><button type="button" disabled={vaultLocked && Boolean(activeEncryptionStatus?.protected)} onClick={() => void toggleReadOnly()} className={`rounded-md border px-3 py-1.5 text-xs font-semibold ${readOnly ? 'border-amber-500 bg-amber-400/15 text-amber-200' : 'border-zinc-700 text-zinc-200 hover:bg-zinc-800'}`} aria-pressed={readOnly}>{readOnly ? '🔒 Read only' : '✎ Edit'}</button><button type="button" disabled={vaultLocked && Boolean(activeEncryptionStatus?.protected) || readOnly || saveStatus === 'saved' || saveStatus === 'saving'} onClick={() => void saveDraft()} className="rounded-md border border-zinc-700 px-3 py-1.5 text-xs font-medium text-zinc-200 hover:bg-zinc-800 disabled:cursor-default disabled:opacity-40">Save</button></>}</div>
        </header>
        {tabs.length > 0 && <NoteTabs tabs={tabs} activePath={selectedPath} onActivate={(path) => void activateTab(path)} onClose={(path) => void closeTab(path)} />}
        </div>
        {(!browserOnline || health === 'offline') && <div role="status" className="border-b border-amber-800/70 bg-amber-950/40 px-4 py-2 text-sm text-amber-100 sm:px-8"><strong>Server unavailable.</strong> Vault files are online-only. Unsaved text remains only in this open browser session and has not been saved.</div>}

        {installPrompt && !installPromptDismissed && <div className="flex items-center justify-between gap-3 border-b border-zinc-800 bg-zinc-900/60 px-4 py-2 text-xs text-zinc-300 sm:px-8"><span>Install VariaNotes for a standalone app experience.</span><div className="flex shrink-0 items-center gap-1"><button type="button" onClick={() => void installApplication()} className="min-h-9 rounded-md border border-zinc-600 px-3 font-medium hover:bg-zinc-800">Install app</button><button type="button" onClick={dismissInstallPrompt} aria-label="Dismiss install suggestion" title="Dismiss" className="flex min-h-9 min-w-9 items-center justify-center rounded-md text-lg text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200">×</button></div></div>}

        <article className={`mx-auto w-full ${notebookSidebarCollapsed ? 'max-w-none' : 'max-w-4xl'} flex-1 px-5 sm:px-8 ${selectedPath ? 'pt-2 pb-8 sm:pt-2 sm:pb-12' : 'py-8 sm:py-12'}`}>
          {activeEncryptionStatus?.protected && vaultLocked
            ? <section role="status" aria-labelledby="vault-locked-title" className="mx-auto max-w-xl rounded-xl border border-amber-800/70 bg-amber-950/20 p-5"><h2 id="vault-locked-title" className="text-lg font-semibold text-amber-100">Protected vault is locked</h2><p className="mt-2 text-sm leading-6 text-zinc-300">Protected note contents and attachments are hidden until you unlock this vault. Any unsaved draft remains only in this tab's memory and will reappear after unlock. Closing or reloading this tab loses that draft.</p><button type="button" onClick={() => { setUnlockError(undefined); setUnlockDialogOpen(true) }} className="mt-4 min-h-10 rounded-md bg-amber-500 px-4 text-sm font-semibold text-zinc-950 hover:bg-amber-400">Unlock vault</button></section>
            : !selectedPath && <EmptyState notebookConfigured={notebookConfigured !== false} onAddNotebook={() => setAddNotebookOpen(true)} />}
          {noteLoading && <p className="text-sm text-zinc-400">Loading note…</p>}
          {!activeEncryptionStatus?.protected || !vaultLocked ? noteError && <ErrorMessage>{noteError}</ErrorMessage> : null}
		  {(!activeEncryptionStatus?.protected || !vaultLocked) && saveError && <ErrorMessage>{saveStatus === 'conflict' ? 'This note changed elsewhere. Your version is still preserved in this editor and has not overwritten the other version. Review both versions before choosing the result.' : `This note could not be saved on the VariaNotes server. Your current editor content is still visible. Try saving again before leaving the note. Details: ${saveError}`}</ErrorMessage>}
          {(!activeEncryptionStatus?.protected || !vaultLocked) && !noteLoading && note && <Suspense fallback={<p className="text-sm text-zinc-400">Loading editor…</p>}><MarkdownEditor key={editorDocumentKey} documentKey={editorDocumentKey} notePath={note.path} markdown={note.content} readOnly={readOnly} onChange={updateDraft} onStats={publishDocumentStats} notePaths={markdownPaths(entries)} onOpenNoteLink={openInternalNoteLink} stickyToolbar /></Suspense>}
        </article>
		{selectedPath && note && (!activeEncryptionStatus?.protected || !vaultLocked) && <DocumentStatusBar status={saveStatus} stats={visibleDocumentStats} />}
      </main>
      {contextMenu && <div className="fixed inset-0 z-40" onClick={() => setContextMenu(undefined)} onContextMenu={(event) => { event.preventDefault(); setContextMenu(undefined) }}><div className="fixed" style={{ left: contextMenu.x, top: contextMenu.y }} onClick={(event) => event.stopPropagation()}><ActionMenu entry={contextMenu.entry} onNewNote={(entry) => { setContextMenu(undefined); createEntry('file', entry) }} onNewFolder={(entry) => { setContextMenu(undefined); createEntry('directory', entry) }} onOpenNewTab={openEntryInNewTab} onRename={beginRename} onMove={beginMove} onDelete={requestDeleteEntry} /></div></div>}
      {createRequest && <TextInputDialog title={createRequest.type === 'file' ? 'New Note' : 'New Folder'} label={createRequest.type === 'file' ? `Note name${createRequest.parent ? ` in ${createRequest.parent}` : ''}` : `Folder name${createRequest.parent ? ` in ${createRequest.parent}` : ''}`} initialValue={createRequest.suggested} confirmLabel="Create" onCancel={() => setCreateRequest(undefined)} onConfirm={(value) => void submitCreateEntry(value)} />}
      {deleteRequest && <ConfirmationDialog title="Move to Trash?" message={`Move “${deleteRequest.path}” to Trash?${deleteRequest.type === 'file' ? ' Its owned image assets will move with it.' : ' Everything inside this folder will move with it.'} You can restore it later.`} confirmLabel="Move to Trash" danger onCancel={() => setDeleteRequest(undefined)} onConfirm={() => { const entry = deleteRequest; setDeleteRequest(undefined); void deleteEntry(entry) }} />}
      {moveEntry && <FolderPicker entries={entries} notebookName={notebookName} moving={moveEntry} destination={moveDestination} onDestination={setMoveDestination} onCancel={() => setMoveEntry(undefined)} onConfirm={() => void confirmMove()} />}
      {moveLinkPreview && <MoveLinkPreviewDialog preview={moveLinkPreview.preview} onCancel={() => setMoveLinkPreview(undefined)} onConfirm={() => void confirmMoveLinkRewrites()} />}
      {moveProtectionChoice && <MoveProtectionDialog entry={moveProtectionChoice.entry} target={moveProtectionChoice.target} busy={operationBusy} onCancel={() => setMoveProtectionChoice(undefined)} onChoose={(choice) => void confirmProtectionMove(choice)} />}
      {unlockDialogOpen && activeEncryptionStatus?.protected && <VaultUnlockDialog passwordMode={activeEncryptionStatus.unlockPasswordMode} busy={unlockBusy} error={unlockError} onCancel={() => { setUnlockDialogOpen(false); setUnlockError(undefined) }} onUnlock={(password) => void unlockVault(password)} />}
      {settingsOpen && <SettingsDialog mode="settings" authMode={authMode} runningVersion={runningVersion} onLoggedOut={onLoggedOut} autoLockMinutes={autoLockMinutes} onAutoLockMinutes={setAutoLockMinutes} onClose={() => setSettingsOpen(false)} />}
      {addNotebookOpen && <SettingsDialog mode="onboarding" autoLockMinutes={autoLockMinutes} onAutoLockMinutes={setAutoLockMinutes} onNotebookAdded={async () => { await activateCreatedVault(); setAddNotebookOpen(false); setManageNotebooksOpen(true) }} onClose={() => setAddNotebookOpen(false)} />}
      {manageNotebooksOpen && <ManageNotebooksDialog notebooks={notebooks} activeNotebookID={activeNotebookID} operationsBlocked={operationBusy || saveStatus === 'saving' || saveStatus === 'unsaved'} onChanged={refreshNotebookRegistrations} onClose={() => setManageNotebooksOpen(false)} />}


	  {saveConflict && <SaveConflictDialog pending={saveConflict} busy={conflictLoading} error={conflictError} onClose={() => setSaveConflict(undefined)} onResolve={(action, content) => void applySaveConflict(action, content)} />}

      {trashOpen && <TrashDialog onChanged={loadTree} onClose={() => setTrashOpen(false)} />}
    </div>
  )
}

export function SettingsDialog({ mode = 'settings', authMode = 'disabled', runningVersion = 'dev', onLoggedOut = () => undefined, autoLockMinutes, onAutoLockMinutes, onNotebookAdded, onClose }: { mode?: 'settings' | 'onboarding'; authMode?: 'local' | 'disabled'; runningVersion?: string; onLoggedOut?: () => void; autoLockMinutes: AutoLockMinutes; onAutoLockMinutes: (value: AutoLockMinutes) => void; onNotebookAdded?: () => Promise<void> | void; onClose: () => void }) {
  const [vaultKind, setVaultKind] = useState<'new' | 'adopt'>('new')
  const [vaultName, setVaultName] = useState('')
  const [folderName, setFolderName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [cleanupAssets, setCleanupAssets] = useState<CleanupAsset[]>()
  const [selectedAssets, setSelectedAssets] = useState<Set<string>>(new Set())
  const [cleanupBusy, setCleanupBusy] = useState(false)
  const [cleanupError, setCleanupError] = useState<string>()
  const [cleanupFailures, setCleanupFailures] = useState<CleanupFailure[]>([])
  const [confirmCleanup, setConfirmCleanup] = useState(false)

  async function submitVault(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const endpoint = vaultKind === 'new' ? '/api/notebooks/local' : '/api/notebooks/adopt'
      const body = vaultKind === 'new' ? { name: vaultName } : { name: vaultName, folder: folderName }
      await responseJSON<{ id: string; name: string }>(await apiFetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }))
      await onNotebookAdded?.()
      onClose()
    } catch (caught) {
      setError(messageFrom(caught))
    } finally {
      setBusy(false)
    }
  }

  async function scanAssets() {
    setCleanupBusy(true)
    setCleanupError(undefined)
    setCleanupFailures([])
    try {
      const data = await responseJSON<{ assets: CleanupAsset[] }>(await apiFetch('/api/repository/assets/unreferenced'))
      setCleanupAssets(data.assets)
      setSelectedAssets(new Set(data.assets.map((asset) => asset.path)))
    } catch (caught) {
      setCleanupError(messageFrom(caught))
    } finally {
      setCleanupBusy(false)
    }
  }

  async function deleteSelectedAssets() {
    const paths = [...selectedAssets]
    if (paths.length === 0) return
    setConfirmCleanup(false)
    setCleanupBusy(true)
    setCleanupError(undefined)
    setCleanupFailures([])
    try {
      const result = await responseJSON<{ deleted: string[]; failures: CleanupFailure[] }>(await apiFetch('/api/repository/assets/cleanup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ paths }),
      }))
      const deleted = new Set(result.deleted)
      setCleanupAssets((current) => current?.filter((asset) => !deleted.has(asset.path)))
      setSelectedAssets(new Set(result.failures.map((failure) => failure.path)))
      setCleanupFailures(result.failures)
    } catch (caught) {
      setCleanupError(messageFrom(caught))
    } finally {
      setCleanupBusy(false)
    }
  }

  const selectedCount = selectedAssets.size
  const totalSize = cleanupAssets?.filter((asset) => selectedAssets.has(asset.path)).reduce((total, asset) => total + asset.size, 0) ?? 0

  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-3 sm:p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
    <section role="dialog" aria-modal="true" aria-labelledby="settings-title" className="flex max-h-[90vh] w-full max-w-2xl flex-col rounded-xl border border-zinc-700 bg-zinc-900 shadow-2xl">
      <header className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-zinc-800 px-5 py-4">
        <h2 id="settings-title" className="text-lg font-semibold">{mode === 'onboarding' ? 'Add Vault' : 'Settings'}</h2>
        {mode === 'settings' && <code aria-label="Application version" title={`VariaNotes ${runningVersion || 'dev'}`} className="ml-auto select-text whitespace-nowrap font-mono text-xs text-zinc-500">{runningVersion || 'dev'}</code>}
        <button type="button" onClick={onClose} aria-label="Close" className="min-h-9 min-w-9 rounded text-xl text-zinc-500 hover:bg-zinc-800">×</button>
      </header>
      <div className="min-h-0 space-y-5 overflow-y-auto p-5">
        {mode === 'onboarding' && <section>
          <h3 className="text-sm font-semibold text-zinc-200">Create a vault or adopt an existing folder</h3>
          <p className="mt-1 text-xs leading-5 text-zinc-500">Vaults are ordinary folders on this server. Every device reads and saves the same files while online.</p>
          <div className="mt-3 grid gap-2 sm:grid-cols-2">
            <button type="button" aria-pressed={vaultKind === 'new'} onClick={() => setVaultKind('new')} className={`min-h-12 rounded-md border px-3 text-left text-sm ${vaultKind === 'new' ? 'border-amber-500 bg-amber-400/10 text-amber-100' : 'border-zinc-700 text-zinc-300'}`}>Create an empty vault</button>
            <button type="button" aria-pressed={vaultKind === 'adopt'} onClick={() => setVaultKind('adopt')} className={`min-h-12 rounded-md border px-3 text-left text-sm ${vaultKind === 'adopt' ? 'border-amber-500 bg-amber-400/10 text-amber-100' : 'border-zinc-700 text-zinc-300'}`}>Adopt an existing folder</button>
          </div>
          <form className="mt-4 space-y-3" onSubmit={(event) => void submitVault(event)}>
            <label className="block text-sm text-zinc-300">Vault name<input required maxLength={100} value={vaultName} onChange={(event) => setVaultName(event.target.value)} className="mt-1.5 min-h-10 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500" placeholder="Personal" /></label>
            {vaultKind === 'adopt' && <>
              <label className="block text-sm text-zinc-300">Folder name under the configured vault directory<input required value={folderName} onChange={(event) => setFolderName(event.target.value)} className="mt-1.5 min-h-10 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500" placeholder="my-existing-notes" /></label>
              <p className="rounded-md border border-amber-800/50 bg-amber-950/20 p-3 text-xs leading-5 text-amber-100/80">If this folder has a legacy <code>.git</code> directory, VariaNotes will leave it untouched and will not use it. Old commits inside that folder may still contain deleted notes or secrets.</p>
            </>}
            {error && <p role="alert" className="rounded-md border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{error}</p>}
            <div className="flex justify-end gap-2"><button type="button" onClick={onClose} className="min-h-10 rounded-md border border-zinc-700 px-4 text-sm text-zinc-300">Cancel</button><button disabled={busy || !vaultName.trim() || (vaultKind === 'adopt' && !folderName.trim())} className="min-h-10 rounded-md bg-amber-500 px-4 text-sm font-semibold text-zinc-950 disabled:opacity-40">{busy ? 'Opening vault…' : vaultKind === 'new' ? 'Create vault' : 'Adopt vault'}</button></div>
          </form>
        </section>}
        {mode === 'settings' && <>
          <section aria-labelledby="settings-storage-title"><h3 id="settings-storage-title" className="text-sm font-semibold text-zinc-200">Storage and recovery</h3><p className="mt-1 text-xs leading-5 text-zinc-400">The server folder is the canonical copy. The operator is responsible for backups and restore. Export a vault from Manage Vaults; an export is a portable copy, not automatic backup.</p><p className="mt-2 text-xs leading-5 text-zinc-500">Exports containing protected content are unencrypted ZIP files. Earlier backups and copies may also contain plaintext. VariaNotes does not erase prior copies or provide a backup service.</p><p className="mt-2 text-xs leading-5 text-zinc-500">Note content is online-only. When the server is unavailable, VariaNotes cannot load or save vault files.</p></section>
          <section aria-labelledby="auto-lock-title"><h3 id="auto-lock-title" className="text-sm font-semibold text-zinc-200">Editor</h3><p className="mt-1 text-xs leading-5 text-zinc-500">This only makes the editor read-only and saves its draft. Protected vaults have a separate server-side lock after 15 minutes without activity.</p><label className="mt-2 block text-sm text-zinc-300">Make editor read-only after<select value={autoLockMinutes} onChange={(event) => onAutoLockMinutes(parseAutoLockMinutes(event.target.value))} className="mt-1.5 min-h-10 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500">{autoLockOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label></section>
          {authMode === 'local' && <EncryptionRecoverySettings />}
          {authMode === 'local' && <SecurityPanel authMode={authMode} onLoggedOut={onLoggedOut} />}
          <section aria-labelledby="asset-cleanup-title" className="rounded-md border border-zinc-800 p-3"><div className="flex flex-wrap items-center justify-between gap-2"><div><h3 id="asset-cleanup-title" className="text-sm font-semibold text-zinc-200">Unused image assets</h3><p className="mt-1 text-xs text-zinc-500">Review unreferenced image files before removing them from this vault.</p></div><button type="button" disabled={cleanupBusy} onClick={() => void scanAssets()} className="min-h-9 rounded border border-zinc-700 px-3 text-xs text-zinc-200">{cleanupBusy ? 'Scanning…' : cleanupAssets ? 'Scan again' : 'Scan vault'}</button></div>
            {cleanupError && <p role="alert" className="mt-3 text-xs text-red-300">{cleanupError}</p>}
            {cleanupAssets && <><p className="mt-3 text-xs text-zinc-400">{cleanupAssets.length} unused {cleanupAssets.length === 1 ? 'asset' : 'assets'} found.</p><ul className="mt-2 max-h-48 space-y-1 overflow-y-auto">{cleanupAssets.map((asset) => <li key={asset.path}><label className="flex min-h-9 items-center gap-2 rounded px-2 text-xs hover:bg-zinc-800"><input type="checkbox" checked={selectedAssets.has(asset.path)} onChange={() => setSelectedAssets((current) => { const next = new Set(current); if (next.has(asset.path)) next.delete(asset.path); else next.add(asset.path); return next })} className="h-4 w-4 accent-amber-500"/><span className="min-w-0 flex-1 truncate">{asset.path}</span><span className="text-zinc-500">{formatBytes(asset.size)}</span></label></li>)}</ul>{cleanupFailures.map((failure) => <p key={failure.path} className="mt-1 text-xs text-red-300">{failure.path}: {failure.error}</p>)}<button type="button" disabled={cleanupBusy || selectedCount === 0} onClick={() => setConfirmCleanup(true)} className="mt-3 min-h-9 rounded border border-red-900 px-3 text-xs text-red-300 disabled:opacity-40">Remove {selectedCount} selected ({formatBytes(totalSize)})</button></>}
          </section>
        </>}
      </div>
      {mode === 'settings' && <footer className="flex justify-end border-t border-zinc-800 p-4"><button type="button" onClick={onClose} className="min-h-10 rounded-md bg-amber-500 px-4 text-sm font-medium text-zinc-950">Done</button></footer>}
    </section>
    {confirmCleanup && <ConfirmationDialog title="Remove unused assets?" message={`Permanently remove ${selectedCount} unreferenced image assets (${formatBytes(totalSize)}) from the active vault?`} confirmLabel="Remove assets" danger busy={cleanupBusy} onCancel={() => setConfirmCleanup(false)} onConfirm={() => void deleteSelectedAssets()} />}
  </div>
}

function EncryptionRecoverySettings() {
  const [status, setStatus] = useState<EncryptionStatus>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [notice, setNotice] = useState<string>()
  const [challenge, setChallenge] = useState<string>()
  const [recoveryCode, setRecoveryCode] = useState('')

  const load = useCallback(async () => {
    try {
      setStatus(await responseJSON<EncryptionStatus>(await apiFetch('/api/encryption/status')))
    } catch (caught) {
      setError(messageFrom(caught))
    }
  }, [])

  useEffect(() => {
    let active = true
    queueMicrotask(() => { if (active) void load() })
    return () => { active = false }
  }, [load])

  async function prepare(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const values = new FormData(form)
    setBusy(true)
    setError(undefined)
    setNotice(undefined)
    try {
      const payload: Record<string, string> = {
        currentPassword: String(values.get('encryptionRecoveryCurrentPassword') ?? ''),
        mfaCode: String(values.get('encryptionRecoveryMfa') ?? ''),
      }
      if (status?.unlockPasswordMode === 'separate') payload.encryptionPassword = String(values.get('encryptionRecoveryPassword') ?? '')
      const result = await responseJSON<{ challengeId: string; recoveryCode: string }>(await apiFetch('/api/encryption/recovery-code', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
      }))
      form.reset()
      setChallenge(result.challengeId)
      setRecoveryCode(result.recoveryCode)
    } catch (caught) {
      setError(messageFrom(caught))
    } finally {
      setBusy(false)
    }
  }

  async function confirm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!challenge) return
    const values = new FormData(event.currentTarget)
    setBusy(true)
    setError(undefined)
    setNotice(undefined)
    try {
      await responseJSON(await apiFetch('/api/encryption/recovery-code/confirm', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ challengeId: challenge, recoveryCode: String(values.get('confirmRecoveryCode') ?? ''), confirmed: values.get('recoveryCodeStored') === 'on' }),
      }))
      setChallenge(undefined)
      setRecoveryCode('')
      setNotice('Recovery code saved and confirmed. Keep it separately from the server and your backups.')
      await load()
    } catch (caught) {
      setError(messageFrom(caught))
    } finally {
      setBusy(false)
    }
  }

  const copyCode = async () => {
    if (!navigator.clipboard) {
      setError('Clipboard is unavailable; select and copy the recovery code manually.')
      return
    }
    try {
      await navigator.clipboard.writeText(recoveryCode)
      setNotice('Recovery code copied. Store it separately before confirming.')
    } catch {
      setError('Clipboard is unavailable; select and copy the recovery code manually.')
    }
  }
  return <section aria-labelledby="encryption-recovery-title" className="rounded-md border border-zinc-800 p-3"><h3 id="encryption-recovery-title" className="text-sm font-semibold text-zinc-200">Vault protection and recovery</h3>{error && <p role="alert" className="mt-3 rounded border border-red-900/70 bg-red-950/30 p-3 text-xs text-red-200">{error}</p>}{notice && <p role="status" className="mt-3 rounded border border-emerald-900/70 bg-emerald-950/20 p-3 text-xs text-emerald-300">{notice}</p>}{!status && !error && <p className="mt-2 text-xs text-zinc-500">Loading protection status…</p>}{status && <><p className="mt-2 text-xs leading-5 text-zinc-400">{status.protected ? `This vault is protected. A recovery code is ${status.recoveryCodeConfigured ? 'configured' : 'not configured'}.` : 'This vault is not protected.'}</p><p className="mt-2 rounded border border-amber-800/60 bg-amber-950/20 p-3 text-xs leading-5 text-amber-100">Encryption activation remains disabled in the interface and API until the full M31 release gate and independent security review pass. Recovery codes are shown once, remain only in this browser tab until you confirm, and must be stored separately. Earlier backups keep their existing recovery envelopes.</p>{status.protected && !challenge && <form onSubmit={(event) => void prepare(event)} className="mt-4 space-y-3"><h4 className="text-xs font-medium text-zinc-200">{status.recoveryCodeConfigured ? 'Replace recovery code' : 'Set up recovery code'}</h4><p className="text-xs leading-5 text-zinc-500">This creates a one-time code for recovering encrypted vault keys while preserving ciphertext. {status.unlockPasswordMode === 'separate' ? 'The separate encryption password is also required.' : 'Use your current sign-in password.'}</p><SecurityPassword name="encryptionRecoveryCurrentPassword" label="Current sign-in password" autoComplete="current-password"/>{status.unlockPasswordMode === 'separate' && <SecurityPassword name="encryptionRecoveryPassword" label="Current encryption password" autoComplete="current-password"/>}{status.mfaEnabled && <SecurityCode name="encryptionRecoveryMfa" label="Authenticator or recovery code"/>}<button type="submit" disabled={busy} className="min-h-10 rounded-md border border-zinc-700 px-3 text-xs text-zinc-200 disabled:opacity-50">{busy ? 'Preparing…' : status.recoveryCodeConfigured ? 'Replace recovery code' : 'Create recovery code'}</button></form>}{challenge && <form onSubmit={(event) => void confirm(event)} className="mt-4 space-y-3"><h4 className="text-xs font-medium text-zinc-200">Save and confirm this code</h4><p className="text-xs leading-5 text-amber-100">Store this code somewhere separate from VariaNotes. It is displayed only during this step; it will not be stored in browser storage.</p><div className="rounded border border-zinc-700 bg-zinc-950 p-3"><code className="select-all break-all font-mono text-sm text-zinc-100">{recoveryCode}</code><button type="button" onClick={() => void copyCode()} className="mt-3 min-h-9 rounded border border-zinc-700 px-3 text-xs text-zinc-300">Copy recovery code</button></div><label className="block text-xs text-zinc-300">Re-enter the recovery code to confirm<input name="confirmRecoveryCode" required autoComplete="off" spellCheck={false} className="mt-1.5 min-h-10 w-full rounded border border-zinc-700 bg-zinc-950 px-3 font-mono text-xs"/></label><label className="flex items-start gap-2 text-xs leading-5 text-zinc-300"><input name="recoveryCodeStored" type="checkbox" required className="mt-0.5 h-4 w-4"/>I stored this code separately and understand that replacing it does not update older backups.</label><div className="flex justify-end gap-2"><button type="button" disabled={busy} onClick={() => { setChallenge(undefined); setRecoveryCode(''); setError(undefined); setNotice(undefined) }} className="min-h-9 rounded px-3 text-xs text-zinc-400">Cancel</button><button type="submit" disabled={busy} className="min-h-9 rounded bg-amber-500 px-3 text-xs font-semibold text-zinc-950 disabled:opacity-50">{busy ? 'Confirming…' : 'Confirm saved code'}</button></div></form>}</>}</section>
}

function SecurityPanel({ authMode, onLoggedOut }: { authMode:'local'|'disabled'; onLoggedOut:()=>void }) {
  const [settings, setSettings] = useState<ServerSessionSettings>()
  const [sessions, setSessions] = useState<SecuritySession[]>([])
  const [mfaEnabled, setMFAEnabled] = useState(false)
  const [error, setError] = useState<string>()
  const [notice, setNotice] = useState<string>()
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (authMode !== 'local') return
    try {
      const [securityResponse, sessionsResponse] = await Promise.all([apiFetch('/api/auth/security'), apiFetch('/api/auth/sessions')])
      const security = await responseJSON<{sessionSettings:ServerSessionSettings;mfaEnabled:boolean}>(securityResponse)
      setSettings(security.sessionSettings)
      setMFAEnabled(security.mfaEnabled)
      setSessions((await responseJSON<{sessions:SecuritySession[]}>(sessionsResponse)).sessions)
    } catch (caught) { setError(messageFrom(caught)) }
  }, [authMode])
  useEffect(() => { queueMicrotask(() => void load()) }, [load])

  async function changePassword(event:FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form=event.currentTarget; const data=new FormData(form)
    if (data.get('newPassword') !== data.get('confirmNewPassword')) { setError('New passwords do not match.'); return }
    setBusy(true); setError(undefined); setNotice(undefined)
    try {
      const response=await apiFetch('/api/auth/password',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({currentPassword:data.get('currentPassword'),newPassword:data.get('newPassword'),mfaCode:data.get('passwordMfaCode')})})
      const result=await responseJSON<{csrfToken:string}>(response); setCSRFToken(result.csrfToken); form.reset(); setNotice('Password changed. All other sessions were signed out.'); notifyAuthChanged(); await load()
    } catch(caught){setError(messageFrom(caught))} finally{setBusy(false)}
  }

  async function saveDurations(event:FormEvent<HTMLFormElement>) {
    event.preventDefault(); if(!settings)return; const form=event.currentTarget; const data=new FormData(form); setBusy(true);setError(undefined);setNotice(undefined)
    try { const response=await apiFetch('/api/auth/security/session-settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({...settings,currentPassword:data.get('settingsPassword'),mfaCode:data.get('settingsMfaCode')})}); const result=await responseJSON<{sessionSettings:ServerSessionSettings}>(response);setSettings(result.sessionSettings);form.reset();setNotice('Session durations saved. They apply to new logins.') } catch(caught){setError(messageFrom(caught))} finally{setBusy(false)}
  }

  async function revoke(path:string) { setBusy(true);setError(undefined);try{await responseJSON(await apiFetch(path,{method:'DELETE'}));await load()}catch(caught){setError(messageFrom(caught))}finally{setBusy(false)} }
  async function logout(){setBusy(true);try{await responseJSON(await apiFetch('/api/auth/logout',{method:'POST'}));onLoggedOut()}catch(caught){setError(messageFrom(caught));setBusy(false)}}

  return <section className="order-3 mt-6 border-t border-zinc-800 pt-5" aria-labelledby="security-title"><h3 id="security-title" className="text-sm font-semibold text-zinc-200">Security</h3>{authMode==='disabled'?<p role="alert" className="mt-3 rounded-md border border-amber-700/70 bg-amber-950/25 p-3 text-xs leading-5 text-amber-200"><strong>Built-in authentication is disabled.</strong> Restrict access through localhost, a private LAN/VPN/Tailscale network, or a deliberately configured external protection layer. Interactive forward-auth may expire independently and return login HTML to this PWA.</p>:<div className="mt-3 space-y-5">{error&&<p role="alert" className="rounded-md border border-red-900/70 bg-red-950/30 p-3 text-xs text-red-200">{error}</p>}{notice&&<p role="status" className="rounded-md border border-emerald-900/70 bg-emerald-950/20 p-3 text-xs text-emerald-300">{notice}</p>}<MFASettings enabled={mfaEnabled} busy={busy} setBusy={setBusy} onChanged={async()=>{await load()}} onLoggedOut={onLoggedOut} setError={setError} setNotice={setNotice}/><form onSubmit={(event)=>void changePassword(event)} className="space-y-3 rounded-md border border-zinc-800 p-3"><h4 className="text-sm font-medium">Change password</h4><p className="text-xs leading-5 text-zinc-500">Your current password confirms this sensitive change. All other sessions are revoked.</p><SecurityPassword name="currentPassword" label="Current password" autoComplete="current-password"/>{mfaEnabled&&<SecurityCode name="passwordMfaCode" label="Authenticator or recovery code"/>}<SecurityPassword name="newPassword" label="New password" autoComplete="new-password"/><SecurityPassword name="confirmNewPassword" label="Confirm new password" autoComplete="new-password"/><button disabled={busy} className="min-h-10 rounded-md border border-zinc-700 px-3 text-xs hover:bg-zinc-800 disabled:opacity-50">Change password</button></form>{settings&&<form onSubmit={(event)=>void saveDurations(event)} className="space-y-3 rounded-md border border-zinc-800 p-3"><h4 className="text-sm font-medium">Session durations</h4><label className="block text-xs text-zinc-300">Normal login lifetime<input type="number" min="1" max="24" value={settings.lifetimeHours} onChange={(event)=>setSettings({...settings,lifetimeHours:Number(event.target.value)})} className="mt-1 w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2"/> hours</label><label className="block text-xs text-zinc-300">Idle timeout<input type="number" min="1" max="720" value={settings.idleHours} onChange={(event)=>setSettings({...settings,idleHours:Number(event.target.value)})} className="mt-1 w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2"/> hours</label><label className="block text-xs text-zinc-300">Remembered device lifetime<input type="number" min="1" max="90" value={settings.rememberDays} onChange={(event)=>setSettings({...settings,rememberDays:Number(event.target.value)})} className="mt-1 w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2"/> days</label><SecurityPassword name="settingsPassword" label="Current password to save" autoComplete="current-password"/>{mfaEnabled&&<SecurityCode name="settingsMfaCode" label="Authenticator or recovery code"/>}<button disabled={busy} className="min-h-10 rounded-md border border-zinc-700 px-3 text-xs hover:bg-zinc-800 disabled:opacity-50">Save durations</button></form>}<div className="rounded-md border border-zinc-800 p-3"><div className="flex items-center justify-between gap-2"><div><h4 className="text-sm font-medium">Browser sessions</h4><p className="mt-1 text-xs text-zinc-500">Device descriptions use only the browser user agent.</p></div><button type="button" disabled={busy} onClick={()=>void revoke('/api/auth/sessions/others')} className="min-h-10 rounded px-2 text-xs text-amber-300 hover:bg-zinc-800">Sign out others</button></div><ul className="mt-3 space-y-2">{sessions.filter((session)=>!session.revokedAt).map((session)=><li key={session.id} className="rounded border border-zinc-800 p-2 text-xs"><div className="flex items-start justify-between gap-2"><div className="min-w-0"><p className="truncate text-zinc-300">{session.clientDescription||'Unknown client'} {session.current&&<span className="text-emerald-400">· Current</span>}</p><p className="mt-1 text-zinc-500">Active {new Date(session.lastActivityAt).toLocaleString()}</p></div>{!session.current&&<button type="button" disabled={busy} onClick={()=>void revoke(`/api/auth/sessions/${encodeURIComponent(session.id)}`)} className="min-h-9 shrink-0 rounded px-2 text-red-300 hover:bg-red-950/40">Revoke</button>}</div></li>)}</ul><button type="button" disabled={busy} onClick={()=>void logout()} className="mt-4 min-h-10 rounded-md border border-red-900 px-3 text-xs text-red-300 hover:bg-red-950/40">Sign out this device</button></div></div>}</section>
}

type MFAEnrollment = { secret:string; qrCode:string; recoveryCodes:string[] }
function MFASettings({enabled,busy,setBusy,onChanged,onLoggedOut,setError,setNotice}:{enabled:boolean;busy:boolean;setBusy:(value:boolean)=>void;onChanged:()=>Promise<void>;onLoggedOut:()=>void;setError:(value:string|undefined)=>void;setNotice:(value:string|undefined)=>void}) {
  const [enrollment,setEnrollment]=useState<MFAEnrollment>()
  const [shownCodes,setShownCodes]=useState<string[]>()
  async function begin(event:FormEvent<HTMLFormElement>){event.preventDefault();const form=event.currentTarget;const data=new FormData(form);setBusy(true);setError(undefined);try{const response=await apiFetch('/api/auth/mfa/enroll',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({currentPassword:data.get('mfaPassword'),currentFactor:data.get('currentFactor')})});setEnrollment(await responseJSON<MFAEnrollment>(response));form.reset()}catch(caught){setError(messageFrom(caught))}finally{setBusy(false)}}
  async function confirm(event:FormEvent<HTMLFormElement>){event.preventDefault();const form=event.currentTarget;const data=new FormData(form);setBusy(true);setError(undefined);try{const result=await responseJSON<{csrfToken:string}>(await apiFetch('/api/auth/mfa/confirm',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:data.get('confirmationCode'),recoveryCodesStored:data.get('codesStored')==='on'})}));setCSRFToken(result.csrfToken);setEnrollment(undefined);setNotice('Two-factor authentication enabled.');notifyAuthChanged();await onChanged()}catch(caught){setError(messageFrom(caught))}finally{setBusy(false)}}
  async function disable(event:FormEvent<HTMLFormElement>){event.preventDefault();const form=event.currentTarget;const data=new FormData(form);setBusy(true);setError(undefined);try{await responseJSON(await apiFetch('/api/auth/mfa',{method:'DELETE',headers:{'Content-Type':'application/json'},body:JSON.stringify({currentPassword:data.get('disablePassword'),code:data.get('disableCode')})}));onLoggedOut()}catch(caught){setError(messageFrom(caught));setBusy(false)}}
  async function regenerate(event:FormEvent<HTMLFormElement>){event.preventDefault();const form=event.currentTarget;const data=new FormData(form);setBusy(true);setError(undefined);try{const result=await responseJSON<{recoveryCodes:string[]}>(await apiFetch('/api/auth/mfa/recovery-codes',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({currentPassword:data.get('recoveryPassword'),code:data.get('recoveryCode')})}));setShownCodes(result.recoveryCodes);form.reset()}catch(caught){setError(messageFrom(caught))}finally{setBusy(false)}}
  async function cancelEnrollment(){setBusy(true);setError(undefined);try{await responseJSON(await apiFetch('/api/auth/mfa/enrollment',{method:'DELETE'}));setEnrollment(undefined);setNotice('MFA setup cancelled.')}catch(caught){setError(messageFrom(caught))}finally{setBusy(false)}}
  return <div className="rounded-md border border-zinc-800 p-3"><h4 className="text-sm font-medium">Two-factor authentication</h4><p className="mt-1 text-xs leading-5 text-zinc-500">{enabled?'Enabled. A code is required after your password.':'Optional. Use any standard TOTP authenticator app.'}</p>{!enrollment&&<form onSubmit={(event)=>void begin(event)} className="mt-3 space-y-3"><SecurityPassword name="mfaPassword" label="Current password" autoComplete="current-password"/>{enabled&&<SecurityCode name="currentFactor" label="Current authenticator or recovery code"/>}<button disabled={busy} className="min-h-10 rounded border border-zinc-700 px-3 text-xs hover:bg-zinc-800">{enabled?'Replace authenticator':'Set up authenticator'}</button></form>}{enrollment&&<form onSubmit={(event)=>void confirm(event)} className="mt-4 space-y-3 rounded border border-amber-800/60 p-3"><p className="text-xs text-zinc-300">Scan this QR code locally with your authenticator app.</p><img src={enrollment.qrCode} alt="TOTP enrollment QR code" className="mx-auto h-56 w-56 rounded bg-white p-2"/><p className="break-all font-mono text-xs text-zinc-400">Manual secret: {enrollment.secret}</p><RecoveryCodes codes={enrollment.recoveryCodes}/><label className="flex items-start gap-2 text-xs text-zinc-300"><input name="codesStored" type="checkbox" required className="mt-0.5 h-4 w-4"/>I stored these recovery codes safely.</label><SecurityCode name="confirmationCode" label="Current code from the new authenticator"/><div className="flex flex-wrap gap-2"><button disabled={busy} className="min-h-10 rounded bg-amber-500 px-3 text-xs font-medium text-zinc-950">Enable MFA</button><button type="button" disabled={busy} onClick={()=>void cancelEnrollment()} className="min-h-10 rounded border border-zinc-700 px-3 text-xs text-zinc-300">Cancel setup</button></div></form>}{enabled&&<><form onSubmit={(event)=>void regenerate(event)} className="mt-4 space-y-3 border-t border-zinc-800 pt-4"><h5 className="text-xs font-medium">Generate new recovery codes</h5><SecurityPassword name="recoveryPassword" label="Current password" autoComplete="current-password"/><SecurityCode name="recoveryCode" label="Authenticator or recovery code"/><button disabled={busy} className="min-h-10 rounded border border-zinc-700 px-3 text-xs">Regenerate codes</button></form>{shownCodes&&<RecoveryCodes codes={shownCodes}/>}<form onSubmit={(event)=>void disable(event)} className="mt-4 space-y-3 border-t border-zinc-800 pt-4"><h5 className="text-xs font-medium text-red-300">Disable MFA</h5><SecurityPassword name="disablePassword" label="Current password" autoComplete="current-password"/><SecurityCode name="disableCode" label="Authenticator or recovery code"/><button disabled={busy} className="min-h-10 rounded border border-red-900 px-3 text-xs text-red-300">Disable and sign out all devices</button></form></>}</div>
}

function RecoveryCodes({codes}:{codes:string[]}) { return <div className="rounded border border-zinc-700 bg-zinc-950 p-3"><p className="mb-2 text-xs font-medium text-amber-300">Recovery codes — shown once</p><ul className="grid gap-1 font-mono text-xs sm:grid-cols-2">{codes.map((code)=><li key={code}>{code}</li>)}</ul></div> }
function SecurityCode({name,label}:{name:string;label:string}) { return <label className="block text-xs text-zinc-300">{label}<input name={name} required autoComplete="one-time-code" spellCheck={false} className="mt-1.5 min-h-10 w-full rounded border border-zinc-700 bg-zinc-950 px-3 font-mono text-sm"/></label> }

function SecurityPassword({name,label,autoComplete}:{name:string;label:string;autoComplete:string}) { return <label className="block text-xs text-zinc-300">{label}<input name={name} type="password" required minLength={15} maxLength={1024} autoComplete={autoComplete} className="mt-1.5 min-h-10 w-full rounded border border-zinc-700 bg-zinc-950 px-3 text-sm"/></label> }

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let value = bytes / 1024
  let unit = units[0]
  for (let index = 1; index < units.length && value >= 1024; index += 1) {
    value /= 1024
    unit = units[index]
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${unit}`
}

function ManageNotebooksDialog({ notebooks, activeNotebookID, operationsBlocked, onChanged, onClose }: { notebooks: NotebookInfo[]; activeNotebookID: string; operationsBlocked: boolean; onChanged: () => Promise<void>; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [health, setHealth] = useState<Record<string, NotebookHealth>>({})
  const [editing, setEditing] = useState<NotebookInfo>()
  const [name, setName] = useState('')
  const [removing, setRemoving] = useState<NotebookInfo>()
  const [deleteLocal, setDeleteLocal] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const [exporting, setExporting] = useState<NotebookInfo>()
  const [exportPassword, setExportPassword] = useState('')
  const [exportWarningConfirmed, setExportWarningConfirmed] = useState(false)

  const checkHealth = useCallback(async (notebook: NotebookInfo) => {
    try {
      const result = await responseJSON<NotebookHealth>(await apiFetch(`/api/notebooks/${encodeURIComponent(notebook.id)}/health`))
      setHealth((current) => ({ ...current, [notebook.id]: result }))
    } catch (caught) {
      setError(messageFrom(caught))
    }
  }, [])
  useEffect(() => { notebooks.forEach((notebook) => void checkHealth(notebook)) }, [notebooks, checkHealth])

  async function saveName(event: FormEvent) {
    event.preventDefault()
    if (!editing) return
    setBusy(true)
    setError(undefined)
    try {
      await responseJSON(await apiFetch(`/api/notebooks/${encodeURIComponent(editing.id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) }))
      setEditing(undefined)
      await onChanged()
    } catch (caught) {
      setError(messageFrom(caught))
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    if (!removing) return
    setBusy(true)
    setError(undefined)
    try {
      const response = await apiFetch(`/api/notebooks/${encodeURIComponent(removing.id)}${deleteLocal ? '?deleteLocal=true' : ''}`, { method: 'DELETE', ...(deleteLocal ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirmation }) } : {}) })
      if (!response.ok) await responseJSON(response)
      setRemoving(undefined)
      await onChanged()
    } catch (caught) {
      setError(messageFrom(caught))
    } finally {
      setBusy(false)
    }
  }

  async function exportVault(notebook: NotebookInfo) {
    setBusy(true)
    setError(undefined)
    try {
      const response = await apiFetch(`/api/notebooks/${encodeURIComponent(notebook.id)}/export`)
      if (response.status === 428) {
        const body = await response.json() as { code?: string }
        if (body.code === 'export_reauthentication_required') {
          setExporting(notebook)
          setExportPassword('')
          setExportWarningConfirmed(false)
          return
        }
        throw new APIError('The vault export could not be prepared.', response.status)
      }
      if (!response.ok) await responseJSON(response)
      await downloadNotebookExport(response, notebook)
    } catch (caught) {
      setError(messageFrom(caught))
    } finally {
      setBusy(false)
    }
  }

  async function reauthenticateExport(event: FormEvent) {
    event.preventDefault()
    if (!exporting || !exportWarningConfirmed) return
    setBusy(true)
    setError(undefined)
    try {
      const notebook = exporting
      const grant = await responseJSON<{ token: string }>(await apiFetch(`/api/notebooks/${encodeURIComponent(notebook.id)}/export/reauth`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: exportPassword }),
      }))
      const response = await apiFetch(`/api/notebooks/${encodeURIComponent(notebook.id)}/export`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: grant.token }),
      })
      if (!response.ok) await responseJSON(response)
      await downloadNotebookExport(response, notebook)
      setExporting(undefined)
      setExportWarningConfirmed(false)
    } catch (caught) {
      setError(messageFrom(caught))
    } finally {
      setExportPassword('')
      setBusy(false)
    }
  }

  async function downloadNotebookExport(response: Response, notebook: NotebookInfo) {
    const blob = await response.blob()
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `${notebook.name.replace(/[\\/:*?"<>|]/g, '_') || 'Vault'}.zip`
    anchor.click()
    globalThis.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  return <>
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-3 sm:p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section role="dialog" aria-modal="true" aria-labelledby="manage-notebooks-title" className="flex max-h-[90vh] w-full max-w-2xl flex-col rounded-xl border border-zinc-700 bg-zinc-900 shadow-2xl">
        <header className="border-b border-zinc-800 px-5 py-4"><h2 id="manage-notebooks-title" className="text-lg font-semibold">Manage Vaults</h2><p className="mt-1 text-xs text-zinc-500">All vaults live in folders on this server. Export a ZIP or remove a registration.</p></header>
        <div className="overflow-y-auto p-4 sm:p-5">
          {error && <ErrorMessage>{error}</ErrorMessage>}
          {operationsBlocked && <p role="status" className="mb-3 rounded border border-amber-800/60 p-3 text-xs text-amber-200">Finish the current save or vault operation before changing vaults.</p>}
          <div className="space-y-3">{notebooks.map((notebook) => {
            const item = health[notebook.id]
            return <section key={notebook.id} className="rounded-lg border border-zinc-800 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">{editing?.id === notebook.id ? <form onSubmit={(event) => void saveName(event)} className="flex flex-1 gap-2"><label className="sr-only" htmlFor={`vault-name-${notebook.id}`}>Vault name</label><input id={`vault-name-${notebook.id}`} autoFocus required maxLength={100} value={name} onChange={(event) => setName(event.target.value)} className="min-h-10 flex-1 rounded border border-zinc-700 bg-zinc-950 px-2 text-sm"/><button disabled={busy || operationsBlocked} className="rounded border border-zinc-700 px-3 text-xs">Save</button><button type="button" onClick={() => setEditing(undefined)} className="px-2 text-xs text-zinc-400">Cancel</button></form> : <><div><h3 className="font-medium text-zinc-100">{notebook.name}</h3><p className="text-[11px] text-zinc-500">{notebook.id === activeNotebookID ? 'Currently open' : 'Not currently open'}</p></div><button type="button" disabled={busy || operationsBlocked} onClick={() => { setEditing(notebook); setName(notebook.name) }} className="min-h-10 rounded px-3 text-xs text-amber-300 hover:bg-zinc-800">Rename</button></>}</div>
              <div className="mt-3 grid gap-2 text-xs sm:grid-cols-2">{item ? <><HealthLine label="Vault files" check={item.localFiles}/><HealthLine label="Write access" check={item.writeAccess}/></> : <p className="text-zinc-500">Checking vault…</p>}</div>
              <div className="mt-3 flex flex-wrap gap-2"><button type="button" disabled={busy} onClick={() => void exportVault(notebook)} className="min-h-10 rounded border border-amber-700/60 px-3 text-xs text-amber-200 hover:bg-amber-950/30">Export ZIP</button>{(notebook.id !== activeNotebookID || notebooks.length === 1) && <button type="button" disabled={busy || operationsBlocked} onClick={() => { setRemoving(notebook); setDeleteLocal(false); setConfirmation('') }} className="min-h-10 rounded border border-red-900/70 px-3 text-xs text-red-300">Remove…</button>}{item?.writeAccess.state === 'failed' && <span className="self-center text-xs text-amber-300">Check the mounted folder permissions.</span>}</div>
            </section>
          })}</div>
          {notebooks.length === 0 && <p className="rounded-md border border-zinc-800 p-4 text-sm text-zinc-400">No registered vaults.</p>}
        </div>
        <footer className="flex justify-end border-t border-zinc-800 p-4"><button type="button" onClick={onClose} className="rounded-md bg-amber-500 px-4 py-2 text-sm font-medium text-zinc-950">Done</button></footer>
        {exporting && <div role="dialog" aria-modal="true" aria-labelledby="export-protected-title" className="fixed inset-0 z-[70] flex items-center justify-center bg-black/75 p-4"><form onSubmit={(event) => void reauthenticateExport(event)} className="w-full max-w-md rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h3 id="export-protected-title" className="text-lg font-semibold">Confirm protected export</h3><p className="mt-2 text-sm leading-6 text-zinc-300">Enter the current vault password again to export {exporting.name}.</p><label className="mt-4 block text-sm text-zinc-300">Vault password<input type="password" autoComplete="current-password" required maxLength={1024} value={exportPassword} onChange={(event) => setExportPassword(event.target.value)} className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500"/></label><p className="mt-3 rounded border border-amber-800/70 bg-amber-950/20 p-3 text-xs leading-5 text-amber-100">This ZIP is unencrypted and contains plaintext. Earlier backups may also contain plaintext copies.</p><label className="mt-3 flex items-start gap-2 text-xs leading-5 text-zinc-300"><input type="checkbox" required checked={exportWarningConfirmed} onChange={(event) => setExportWarningConfirmed(event.target.checked)} className="mt-0.5 h-4 w-4"/>I understand this export is unencrypted and earlier backups may contain plaintext.</label>{error && <ErrorMessage>{error}</ErrorMessage>}<div className="mt-5 flex justify-end gap-2"><button type="button" disabled={busy} onClick={() => { setExporting(undefined); setExportPassword(''); setExportWarningConfirmed(false); setError(undefined) }} className="min-h-10 rounded px-3 text-xs text-zinc-300">Cancel</button><button type="submit" disabled={busy || !exportWarningConfirmed} className="min-h-10 rounded bg-amber-500 px-3 text-xs font-semibold text-zinc-950 disabled:opacity-50">{busy ? 'Preparing ZIP…' : 'Export unencrypted ZIP'}</button></div></form></div>}
      </section>
    </div>
    {removing && <div role="alertdialog" aria-modal="true" aria-labelledby="remove-vault-title" className="fixed inset-0 z-[60] flex items-center justify-center bg-black/75 p-4"><div className="w-full max-w-md rounded-lg border border-zinc-700 bg-zinc-900 p-5"><h3 id="remove-vault-title" className="font-semibold">Remove {removing.name}?</h3><p className="mt-2 text-xs leading-5 text-zinc-400">The safe default removes only the registration. Vault files remain on disk.</p><label className="mt-4 flex items-start gap-2 text-xs text-red-200"><input type="checkbox" checked={deleteLocal} onChange={(event) => { setDeleteLocal(event.target.checked); setConfirmation('') }} className="mt-0.5"/>Also permanently delete this local vault folder.</label>{deleteLocal && <><p className="mt-2 text-xs leading-5 text-amber-200">This permanently removes every file in the folder, including notes, assets, Trash, and any legacy <code>.git</code> history. Operator backups may retain copies.</p><label className="mt-3 block text-xs text-zinc-300">Type <strong>{removing.name}</strong> to confirm<input value={confirmation} onChange={(event) => setConfirmation(event.target.value)} className="mt-1 min-h-10 w-full rounded border border-red-800 bg-zinc-950 px-2"/></label></>}<div className="mt-5 flex justify-end gap-2"><button type="button" onClick={() => setRemoving(undefined)} className="min-h-10 px-3 text-xs">Cancel</button><button type="button" disabled={busy || operationsBlocked || (deleteLocal && confirmation !== removing.name)} onClick={() => void remove()} className="min-h-10 rounded border border-red-800 px-3 text-xs text-red-200">{deleteLocal ? 'Delete vault folder' : 'Remove registration'}</button></div></div></div>}
  </>
}

function HealthLine({ label, check }: { label: string; check: NotebookCheck }) {
  const okay = check.state === 'ok'
  return <div title={check.message} className="flex items-center justify-between gap-2 rounded bg-zinc-950/60 px-2 py-2"><span className="text-zinc-400">{label}</span><span className={okay ? 'text-emerald-400' : 'text-amber-300'}>{okay ? 'Ready' : 'Needs attention'}</span></div>
}

function SettingsIcon() {
  return <svg aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M12 15.25a3.25 3.25 0 1 0 0-6.5 3.25 3.25 0 0 0 0 6.5Z"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.86 2.86-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1.1V21H9.55v-.1A1.7 1.7 0 0 0 8.4 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.86-2.86.06-.06A1.7 1.7 0 0 0 4 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1.1-.4H2.2V9.55h.1A1.7 1.7 0 0 0 4 8.4a1.7 1.7 0 0 0-.34-1.88l-.06-.06L6.46 3.6l.06.06A1.7 1.7 0 0 0 8.4 4a1.7 1.7 0 0 0 1-.6 1.7 1.7 0 0 0 .4-1.1v-.1h4.05v.1A1.7 1.7 0 0 0 15 4a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.86 2.86-.06.06A1.7 1.7 0 0 0 19.4 8.4a1.7 1.7 0 0 0 .6 1 1.7 1.7 0 0 0 1.1.4h.1v4.05h-.1A1.7 1.7 0 0 0 19.4 15Z"/></svg>
}

function LogoutIcon() {
  return <svg aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M10 5H5.5A1.5 1.5 0 0 0 4 6.5v11A1.5 1.5 0 0 0 5.5 19H10"/><path d="M14 8l4 4-4 4M8 12h10"/></svg>
}

type TreeEntryProps = {
  entry: TreeNode
  selectedPath?: string
  expandedFolders: Set<string>
  renamePath?: string
  renameValue: string
  onSelect: (entry: TreeNode, disposition?: 'current' | 'new') => void
  onToggleFolder: (path: string) => void
  onContextMenu: (entry: TreeNode, event: MouseEvent) => void
  onRenameValue: (value: string) => void
  onRenameCommit: () => void
  onRenameCancel: () => void
}

function TreeEntry(props: TreeEntryProps) {
  const { entry, selectedPath, expandedFolders, renamePath, renameValue, onSelect, onToggleFolder, onContextMenu, onRenameValue, onRenameCommit, onRenameCancel } = props
  const isFolder = entry.type === 'directory'
  const expanded = expandedFolders.has(entry.path)
  const isRenaming = renamePath === entry.path

  return (
    <li>
      <div
        onContextMenu={(event) => onContextMenu(entry, event)}
        className={`flex items-center rounded-md ${selectedPath === entry.path ? 'bg-amber-400/15 text-amber-200' : 'text-zinc-400 hover:bg-zinc-800 hover:text-white'}`}
      >
        {isFolder ? <button type="button" aria-label={`${expanded ? 'Collapse' : 'Expand'} ${entry.name}`} aria-expanded={expanded} className="h-8 w-7 shrink-0 text-xs text-zinc-500" onClick={() => onToggleFolder(entry.path)}>{expanded ? '▾' : '▸'}</button> : <span className="w-7 shrink-0" />}
        <span aria-hidden="true" className="mr-2">{isFolder ? '📁' : '◇'}</span>
        {isRenaming
          ? <InlineRename value={renameValue} onChange={onRenameValue} onCommit={onRenameCommit} onCancel={onRenameCancel} />
          : <button type="button" className="min-w-0 flex-1 truncate py-1.5 pr-2 text-left text-sm" onClick={(event) => onSelect(entry, !isFolder && (event.ctrlKey || event.metaKey) ? 'new' : 'current')} onAuxClick={(event) => { if (!isFolder && event.button === 1) { event.preventDefault(); onSelect(entry, 'new') } }} onDoubleClick={() => { if (isFolder) onToggleFolder(entry.path) }}>{isFolder ? entry.name : entry.name.replace(/\.md$/i, '')}</button>}
        {entry.protected && <span role="img" aria-label={entry.protectionInherited ? 'Protected by parent folder' : 'Protected'} title={entry.protectionInherited ? 'Protection inherited from a protected folder' : 'Protected'} className="mr-1 shrink-0 text-xs text-amber-300">🔒</span>}
      </div>
      {isFolder && expanded && entry.children && <ul className="ml-4 border-l border-zinc-800 pl-2">{entry.children.map((child) => <TreeEntry key={child.path} {...props} entry={child} />)}</ul>}
    </li>
  )
}

function InlineRename({ value, onChange, onCommit, onCancel }: { value: string; onChange: (value: string) => void; onCommit: () => void; onCancel: () => void }) {
  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Escape') { event.preventDefault(); onCancel() }
  }
  return <form className="min-w-0 flex-1 pr-1" onSubmit={(event: FormEvent) => { event.preventDefault(); onCommit() }}><input autoFocus value={value} onChange={(event) => onChange(event.target.value)} onKeyDown={handleKeyDown} className="w-full rounded border border-amber-500 bg-zinc-950 px-1.5 py-1 text-sm text-zinc-100 outline-none" aria-label="New name" /></form>
}

function TreeAction({ children, label, disabled, onClick }: { children: ReactNode; label?: string; disabled: boolean; onClick: () => void }) { return <button type="button" title={label} aria-label={label} disabled={disabled} onClick={onClick} className="rounded-md border border-zinc-700 px-2 py-2 text-xs font-medium text-zinc-300 hover:bg-zinc-800 hover:text-white disabled:opacity-40">{children}</button> }

function TextInputDialog({ title, label, initialValue, confirmLabel, onCancel, onConfirm }: { title:string; label:string; initialValue:string; confirmLabel:string; onCancel:()=>void; onConfirm:(value:string)=>void }) {
  const [value,setValue]=useState(initialValue)
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4" onMouseDown={(event)=>{if(event.target===event.currentTarget)onCancel()}}><form role="dialog" aria-modal="true" aria-labelledby="text-input-dialog-title" onSubmit={(event)=>{event.preventDefault();onConfirm(value)}} className="w-full max-w-md rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h2 id="text-input-dialog-title" className="text-lg font-semibold">{title}</h2><label className="mt-4 block text-sm text-zinc-300">{label}<input autoFocus required value={value} onChange={(event)=>setValue(event.target.value)} className="mt-2 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-zinc-100 outline-none focus:border-amber-500"/></label><div className="mt-5 flex justify-end gap-2"><button type="button" onClick={onCancel} className="min-h-10 rounded-md border border-zinc-700 px-4 text-sm text-zinc-300 hover:bg-zinc-800">Cancel</button><button className="min-h-10 rounded-md bg-amber-500 px-4 text-sm font-medium text-zinc-950 hover:bg-amber-400">{confirmLabel}</button></div></form></div>
}

function ConfirmationDialog({ title, message, confirmLabel, danger=false, busy=false, onCancel, onConfirm }: { title:string; message:string; confirmLabel:string; danger?:boolean; busy?:boolean; onCancel:()=>void; onConfirm:()=>void }) {
  return <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/75 p-4" onMouseDown={(event)=>{if(event.target===event.currentTarget&&!busy)onCancel()}}><section role="alertdialog" aria-modal="true" aria-labelledby="confirmation-dialog-title" aria-describedby="confirmation-dialog-message" className="w-full max-w-md rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h2 id="confirmation-dialog-title" className="text-lg font-semibold">{title}</h2><p id="confirmation-dialog-message" className="mt-3 text-sm leading-6 text-zinc-400">{message}</p><div className="mt-5 flex justify-end gap-2"><button type="button" disabled={busy} onClick={onCancel} className="min-h-10 rounded-md border border-zinc-700 px-4 text-sm text-zinc-300 hover:bg-zinc-800 disabled:opacity-40">Cancel</button><button type="button" disabled={busy} onClick={onConfirm} className={`min-h-10 rounded-md px-4 text-sm font-medium disabled:opacity-40 ${danger?'border border-red-800 bg-red-950/40 text-red-200 hover:bg-red-950/70':'bg-amber-500 text-zinc-950 hover:bg-amber-400'}`}>{busy?'Working…':confirmLabel}</button></div></section></div>
}

function SearchResults({ query, results, loading, error, onSelect }: { query: string; results: SearchResult[]; loading: boolean; error?: string; onSelect: (result: SearchResult) => void }) {
  if (loading) return <SidebarMessage>Searching…</SidebarMessage>
  if (error) return <div className="rounded-lg border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">Search failed: {error}</div>
  if (results.length === 0) return <SidebarMessage>No results for “{query}”.</SidebarMessage>
  return <nav aria-label="Search results"><p className="mb-2 px-2 text-xs text-zinc-500">{results.length === 100 ? 'First 100 results' : `${results.length} ${results.length === 1 ? 'result' : 'results'}`}</p><ul className="space-y-1">{results.map((result, index) => <li key={`${result.type}:${result.path}:${result.line ?? 0}:${index}`}><button type="button" onClick={() => onSelect(result)} className="w-full rounded-md px-2 py-2 text-left hover:bg-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500"><span className="flex items-center gap-2 text-sm text-zinc-200"><span aria-hidden="true" className="text-zinc-500">{result.type === 'directory' ? '▸' : result.type === 'file' ? '◇' : '≡'}</span><span className="truncate">{result.path.replace(/\.md$/i, '')}</span>{result.line && <span className="ml-auto shrink-0 text-xs text-zinc-500">L{result.line}</span>}</span>{result.excerpt && <span className="mt-1 block truncate pl-5 text-xs text-zinc-500">{result.excerpt}</span>}</button></li>)}</ul></nav>
}

function ActionMenu({ entry, onNewNote, onNewFolder, onOpenNewTab, onRename, onMove, onDelete }: { entry?: TreeNode; onNewNote: (entry?: TreeNode) => void; onNewFolder: (entry?: TreeNode) => void; onOpenNewTab: (entry: TreeNode) => void; onRename: (entry: TreeNode) => void; onMove: (entry: TreeNode) => void; onDelete: (entry: TreeNode) => void }) {
  return <div role="menu" className="w-44 overflow-hidden rounded-lg border border-zinc-700 bg-zinc-900 p-1 shadow-2xl"><MenuButton onClick={() => onNewNote(entry)}>New Note</MenuButton><MenuButton onClick={() => onNewFolder(entry)}>New Folder</MenuButton>{entry && <div className="my-1 border-t border-zinc-800" />}{entry?.type === 'file' && <MenuButton onClick={() => onOpenNewTab(entry)}>Open in new tab</MenuButton>}{entry && <><MenuButton onClick={() => onRename(entry)}>Rename</MenuButton><MenuButton onClick={() => onMove(entry)}>Move…</MenuButton><MenuButton danger onClick={() => onDelete(entry)}>Move to Trash</MenuButton></>}</div>
}

function NoteTabs({ tabs, activePath, onActivate, onClose }: { tabs: NoteTab[]; activePath?: string; onActivate: (path: string) => void; onClose: (path: string) => void }) {
  return <nav aria-label="Open notes" className="flex h-11 min-w-0 items-end gap-0.5 overflow-x-auto border-b border-zinc-800 px-2 sm:px-6"><div role="tablist" className="flex min-w-max items-end gap-0.5">{tabs.map((tab) => <div key={tab.path} role="presentation" className={`group flex h-10 max-w-56 items-center rounded-t-md border border-b-0 ${tab.path === activePath ? 'border-zinc-700 bg-zinc-900 text-zinc-100' : 'border-transparent text-zinc-500 hover:bg-zinc-900/60 hover:text-zinc-300'}`}><button type="button" role="tab" aria-selected={tab.path === activePath} title={tab.path} onClick={() => onActivate(tab.path)} className="min-w-0 flex-1 truncate py-2.5 pl-3 text-left text-xs">{baseName(tab.path).replace(/\.md$/i, '')}</button><button type="button" aria-label={`Close ${baseName(tab.path).replace(/\.md$/i, '')}`} title="Close tab" onClick={() => onClose(tab.path)} className="mx-0.5 flex min-h-9 min-w-9 items-center justify-center rounded text-base text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200">×</button></div>)}</div></nav>
}

function MenuButton({ children, danger, onClick }: { children: ReactNode; danger?: boolean; onClick: () => void }) {
  return <button type="button" role="menuitem" onClick={onClick} className={`block w-full rounded px-3 py-2 text-left text-sm ${danger ? 'text-red-300 hover:bg-red-950' : 'text-zinc-200 hover:bg-zinc-800'}`}>{children}</button>
}

function MoveLinkPreviewDialog({ preview, onCancel, onConfirm }: { preview:MoveLinkPreview; onCancel:()=>void; onConfirm:()=>void }) {
  const grouped = new Map<string,LinkRewrite[]>()
  for (const rewrite of preview.rewrites) grouped.set(rewrite.notePath, [...(grouped.get(rewrite.notePath) ?? []), rewrite])
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-3 sm:p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) onCancel() }}><section role="dialog" aria-modal="true" aria-labelledby="move-links-title" className="flex max-h-[92vh] w-full max-w-3xl flex-col rounded-xl border border-zinc-700 bg-zinc-900 shadow-2xl"><header className="border-b border-zinc-800 px-4 py-3 sm:px-5"><h2 id="move-links-title" className="text-lg font-semibold">Update note links?</h2><p className="mt-1 text-sm text-zinc-400">Moving <span className="text-zinc-200">{preview.source}</span> to <span className="text-zinc-200">{preview.target}</span> requires {preview.rewrites.length} portable Markdown link {preview.rewrites.length === 1 ? 'update' : 'updates'}.</p></header><div className="min-h-0 overflow-y-auto p-4 sm:p-5"><p className="mb-4 text-sm leading-6 text-zinc-400">Review the exact changes below. VariaNotes will stop if any note changed after this preview, and will never guess about unsupported or ambiguous Markdown.</p><div className="space-y-3">{[...grouped.entries()].map(([notePath,rewrites]) => <section key={notePath} className="rounded-lg border border-zinc-800 bg-zinc-950 p-3"><h3 className="text-sm font-medium text-zinc-200">{notePath}{rewrites[0].nextNotePath !== notePath && <span className="text-zinc-500"> → {rewrites[0].nextNotePath}</span>}</h3><ul className="mt-2 space-y-2">{rewrites.map((rewrite,index) => <li key={`${rewrite.line}:${index}`} className="text-xs"><span className="text-zinc-500">Line {rewrite.line}</span><div className="mt-1 break-all font-mono text-red-300">− {rewrite.before}</div><div className="break-all font-mono text-emerald-300">+ {rewrite.after}</div></li>)}</ul></section>)}</div></div><footer className="flex flex-wrap justify-end gap-2 border-t border-zinc-800 p-4"><button type="button" onClick={onCancel} className="min-h-10 rounded-md border border-zinc-700 px-4 text-sm text-zinc-300 hover:bg-zinc-800">Cancel move</button><button type="button" onClick={onConfirm} className="min-h-10 rounded-md bg-amber-500 px-4 text-sm font-medium text-zinc-950 hover:bg-amber-400">Move and update links</button></footer></section></div>
}

function MoveProtectionDialog({ entry, target, busy, onCancel, onChoose }: { entry:TreeNode; target:string; busy:boolean; onCancel:()=>void; onChoose:(choice:'keep'|'decrypt')=>void }) {
  const noun = entry.type === 'directory' ? 'This folder and its protected descendants' : 'This note and its protected attachments'
  return <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/80 p-4"><section role="dialog" aria-modal="true" aria-labelledby="move-protection-title" className="w-full max-w-lg rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h2 id="move-protection-title" className="text-lg font-semibold">Choose protection after moving</h2><p className="mt-2 text-sm leading-6 text-zinc-300">{noun} will move from <span className="font-medium text-zinc-100">{entry.path}</span> to <span className="font-medium text-zinc-100">{target}</span>. Choose whether the moved content stays protected or is decrypted. For a folder, the choice applies to its whole subtree.</p><p className="mt-2 text-xs leading-5 text-zinc-500">A shared attachment remains protected while any protected note still references it.</p><div className="mt-5 flex flex-wrap justify-end gap-2"><button type="button" disabled={busy} onClick={onCancel} className="min-h-10 rounded-md border border-zinc-700 px-3 text-sm text-zinc-300">Cancel</button><button type="button" disabled={busy} onClick={() => onChoose('decrypt')} className="min-h-10 rounded-md border border-zinc-700 px-3 text-sm text-zinc-200">Decrypt and move</button><button type="button" disabled={busy} onClick={() => onChoose('keep')} className="min-h-10 rounded-md bg-amber-500 px-3 text-sm font-semibold text-zinc-950">Keep protection</button></div></section></div>
}

function VaultUnlockDialog({ passwordMode, busy, error, onCancel, onUnlock }: { passwordMode:'login'|'separate'; busy:boolean; error?:string; onCancel:()=>void; onUnlock:(password:string)=>void }) {
  const [password, setPassword] = useState('')
  return <div className="fixed inset-0 z-[70] flex items-center justify-center bg-black/80 p-4"><form role="dialog" aria-modal="true" aria-labelledby="vault-unlock-title" onSubmit={(event) => { event.preventDefault(); onUnlock(password) }} className="w-full max-w-md rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h2 id="vault-unlock-title" className="text-lg font-semibold">Unlock protected vault</h2><p className="mt-2 text-sm leading-6 text-zinc-400">{passwordMode === 'login' ? 'Enter your current sign-in password to unlock this vault.' : 'Enter the separate encryption password for this vault. Your sign-in password is not used for encryption.'}</p><label className="mt-4 block text-sm text-zinc-300">{passwordMode === 'login' ? 'Sign-in password' : 'Encryption password'}<input autoFocus type="password" required maxLength={1024} autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} className="mt-1.5 min-h-11 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 text-sm outline-none focus:border-amber-500" /></label>{error && <p role="alert" className="mt-3 rounded border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{error}</p>}<div className="mt-5 flex justify-end gap-2"><button type="button" disabled={busy} onClick={onCancel} className="min-h-10 rounded-md border border-zinc-700 px-3 text-sm text-zinc-300">Cancel</button><button type="submit" disabled={busy || !password} className="min-h-10 rounded-md bg-amber-500 px-4 text-sm font-semibold text-zinc-950 disabled:opacity-50">{busy ? 'Unlocking…' : 'Unlock'}</button></div></form></div>
}

function FolderPicker({ entries, notebookName, moving, destination, onDestination, onCancel, onConfirm }: { entries: TreeNode[]; notebookName: string; moving: TreeNode; destination: string; onDestination: (path: string) => void; onCancel: () => void; onConfirm: () => void }) {
  const folders = flattenFolders(entries).filter(({ node }) => moving.type !== 'directory' || (node.path !== moving.path && !node.path.startsWith(`${moving.path}/`)))
  const unchanged = parentPath(moving.path) === destination
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) onCancel() }}><div role="dialog" aria-modal="true" aria-labelledby="move-title" className="flex max-h-[80vh] w-full max-w-md flex-col rounded-xl border border-zinc-700 bg-zinc-900 shadow-2xl"><header className="border-b border-zinc-800 px-5 py-4"><h2 id="move-title" className="font-semibold">Move “{moving.name}”</h2><p className="mt-1 text-xs text-zinc-400">Choose a destination folder.</p></header><div className="overflow-y-auto p-3"><FolderChoice label={notebookName} depth={0} selected={destination === ''} onClick={() => onDestination('')} />{folders.map(({ node, depth }) => <FolderChoice key={node.path} label={node.name} depth={depth} selected={destination === node.path} onClick={() => onDestination(node.path)} />)}</div><footer className="flex justify-end gap-2 border-t border-zinc-800 p-4"><button type="button" onClick={onCancel} className="rounded-md border border-zinc-700 px-4 py-2 text-sm text-zinc-300 hover:bg-zinc-800">Cancel</button><button type="button" disabled={unchanged} onClick={onConfirm} className="rounded-md bg-amber-500 px-4 py-2 text-sm font-medium text-zinc-950 hover:bg-amber-400 disabled:opacity-40">Move here</button></footer></div></div>
}

function flattenFolders(entries: TreeNode[], depth = 0): { node: TreeNode; depth: number }[] {
  return entries.flatMap((entry) => entry.type === 'directory' ? [{ node: entry, depth }, ...flattenFolders(entry.children ?? [], depth + 1)] : [])
}

function FolderChoice({ label, depth, selected, onClick }: { label: string; depth: number; selected: boolean; onClick: () => void }) {
  return <button type="button" onClick={onClick} style={{ paddingLeft: `${0.75 + depth * 1.25}rem` }} className={`flex w-full items-center gap-2 rounded-md py-2 pr-3 text-left text-sm ${selected ? 'bg-amber-400/15 text-amber-200' : 'text-zinc-300 hover:bg-zinc-800'}`}><span aria-hidden="true">📁</span><span className="truncate">{label}</span></button>
}

export function TrashDialog({ onChanged, onClose }: { onChanged:()=>Promise<void>; onClose:()=>void }) {
  const [items, setItems] = useState<TrashItem[]>()
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [notice, setNotice] = useState<string>()
  const [deleteConfirmation, setDeleteConfirmation] = useState<TrashItem[]>()
  const [restoreConfirmation, setRestoreConfirmation] = useState<TrashItem[]>()

  const load = useCallback(async () => {
    setError(undefined)
    try {
      const response = await apiFetch('/api/repository/trash')
      const loaded = (await responseJSON<{items:TrashItem[]}>(response)).items
      setItems(loaded)
      setSelectedIDs((current) => new Set([...current].filter((id) => loaded.some((item) => item.id === id))))
    } catch (caught) {
      setError(messageFrom(caught))
    }
  }, [])

  useEffect(() => {
    queueMicrotask(() => void load())
    const closeOnEscape = (event:globalThis.KeyboardEvent) => { if (event.key === 'Escape') onClose() }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [load, onClose])

  async function run(itemsToProcess:TrashItem[], operation:'restore'|'delete', protectionChoice?:'keep'|'decrypt') {
    if (itemsToProcess.length === 0) return
    setBusy(true)
    setError(undefined)
    setNotice(undefined)
    const failures:string[] = []
    let completed = 0
    try {
      for (const item of itemsToProcess) {
        try {
          const suffix = operation === 'restore' ? '/restore' : ''
          const method = operation === 'restore' ? 'POST' : 'DELETE'
          const body = operation === 'restore' && item.protectionChoiceRequired && protectionChoice
            ? JSON.stringify({ protectionChoice })
            : undefined
          await responseJSON<TrashItem>(await apiFetch(`/api/repository/trash/${encodeURIComponent(item.id)}${suffix}`, { method, body, headers: body ? { 'Content-Type': 'application/json' } : undefined }))
          completed += 1
        } catch (caught) {
          failures.push(`${item.originalPath}: ${messageFrom(caught)}`)
        }
      }
      setSelectedIDs(new Set())
      if (completed > 0) {
        const successful = itemsToProcess.filter((item) => !failures.some((failure) => failure.startsWith(`${item.originalPath}:`)))
        setNotice(completed === 1 ? `${operation === 'restore' ? 'Restored' : 'Permanently deleted'} “${successful[0].originalPath}”.` : `${operation === 'restore' ? 'Restored' : 'Permanently deleted'} ${completed} items.`)
      }
      if (failures.length > 0) setError(`${failures.length} ${failures.length === 1 ? 'item could' : 'items could'} not be ${operation === 'restore' ? 'restored' : 'deleted'}: ${failures.join('; ')}`)
      await Promise.all([load(), operation === 'restore' ? onChanged() : Promise.resolve()])
    } finally {
      setBusy(false)
    }
  }

  function toggleSelection(id:string) {
    setSelectedIDs((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function requestRestore(itemsToRestore:TrashItem[]) {
    if (itemsToRestore.some((item) => item.protectionChoiceRequired)) setRestoreConfirmation(itemsToRestore)
    else void run(itemsToRestore, 'restore')
  }

  const selectedItems = items?.filter((item) => selectedIDs.has(item.id)) ?? []
  const allSelected = Boolean(items?.length && selectedIDs.size === items.length)

  return <><div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-3 sm:p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}><section role="dialog" aria-modal="true" aria-labelledby="trash-title" className="flex max-h-[90vh] w-full max-w-2xl flex-col rounded-xl border border-zinc-700 bg-zinc-900 shadow-2xl"><header className="flex items-center justify-between gap-3 border-b border-zinc-800 px-5 py-4"><div><h2 id="trash-title" className="text-lg font-semibold">Trash</h2><p className="mt-1 text-xs leading-5 text-zinc-500">Deleted notes and folders stay recoverable here. Protected content remains encrypted until restored or permanently deleted.</p></div><button type="button" autoFocus onClick={onClose} aria-label="Close Trash" className="min-h-10 min-w-10 rounded text-xl text-zinc-500 hover:bg-zinc-800">×</button></header>{items && items.length > 0 && <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800 px-4 py-2"><label className="flex min-h-10 items-center gap-2 text-xs text-zinc-300"><input type="checkbox" aria-label="Select all Trash items" checked={allSelected} onChange={() => setSelectedIDs(allSelected ? new Set() : new Set(items.map((item) => item.id)))} className="h-4 w-4 accent-amber-500"/>Select all</label><span className="text-xs text-zinc-500">{selectedIDs.size} selected</span><div className="ml-auto flex flex-wrap gap-2"><button type="button" disabled={busy || selectedItems.length === 0} onClick={() => requestRestore(selectedItems)} className="min-h-9 rounded-md border border-zinc-700 px-3 text-xs text-zinc-200 hover:bg-zinc-800 disabled:opacity-40">Restore selected</button><button type="button" disabled={busy || selectedItems.length === 0} onClick={() => setDeleteConfirmation(selectedItems)} className="min-h-9 rounded-md border border-red-900 px-3 text-xs text-red-300 hover:bg-red-950/40 disabled:opacity-40">Delete selected</button><button type="button" disabled={busy} onClick={() => setDeleteConfirmation(items)} className="min-h-9 rounded-md border border-red-900 px-3 text-xs text-red-300 hover:bg-red-950/40 disabled:opacity-40">Empty Trash</button></div></div>}<div className="overflow-y-auto p-4 sm:p-5">{error && <ErrorMessage>{error}</ErrorMessage>}{notice && <p role="status" className="mb-4 rounded-md border border-emerald-900/70 bg-emerald-950/20 p-3 text-sm text-emerald-300">{notice}</p>}{items === undefined && !error && <p className="text-sm text-zinc-500">Loading Trash…</p>}{items?.length === 0 && <div className="rounded-md border border-zinc-800 p-5 text-center"><p className="text-sm text-zinc-300">Trash is empty.</p><p className="mt-1 text-xs text-zinc-500">Items you delete from the vault tree will appear here.</p></div>}<ul className="space-y-2">{items?.map((item) => <li key={item.id} className="rounded-md border border-zinc-800 p-3"><label className="flex cursor-pointer items-start gap-3"><input type="checkbox" aria-label={`Select ${item.originalPath}`} checked={selectedIDs.has(item.id)} onChange={() => toggleSelection(item.id)} className="mt-1 h-4 w-4 shrink-0 accent-amber-500"/><span className="min-w-0 flex-1"><span className="break-all text-sm text-zinc-200"><span aria-hidden="true" className="mr-2">{item.type === 'directory' ? '📁' : '◇'}</span>{item.originalPath}</span><span className="mt-1 block text-xs text-zinc-500">Deleted {new Date(item.deletedAt).toLocaleString()} · {formatBytes(item.size)}{item.protected && <span className="ml-2 rounded border border-amber-900 px-1.5 py-0.5 text-amber-300">Protected</span>}</span></span></label><div className="mt-2 flex justify-end gap-2"><button type="button" disabled={busy} onClick={() => requestRestore([item])} className="min-h-9 rounded-md px-3 text-xs text-amber-300 hover:bg-zinc-800 disabled:opacity-40">Restore</button><button type="button" disabled={busy} onClick={() => setDeleteConfirmation([item])} className="min-h-9 rounded-md px-3 text-xs text-red-300 hover:bg-red-950/40 disabled:opacity-40">Permanently delete</button></div></li>)}</ul></div><footer className="flex justify-end border-t border-zinc-800 p-4"><button type="button" onClick={onClose} className="min-h-10 rounded-md border border-zinc-700 px-4 text-sm text-zinc-200 hover:bg-zinc-800">Done</button></footer></section></div>{deleteConfirmation && <ConfirmationDialog title="Permanently delete?" message={"Permanently delete "+deleteConfirmation.length+" "+(deleteConfirmation.length === 1 ? "item" : "items")+"? This cannot be undone in VariaNotes."} confirmLabel="Permanently delete" danger busy={busy} onCancel={() => setDeleteConfirmation(undefined)} onConfirm={() => { const pending=deleteConfirmation; setDeleteConfirmation(undefined); void run(pending,'delete') }} />}{restoreConfirmation && <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/80 p-4"><section role="dialog" aria-modal="true" aria-labelledby="restore-protected-title" className="w-full max-w-lg rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h2 id="restore-protected-title" className="text-lg font-semibold">Restore protected content?</h2><p className="mt-2 text-sm leading-6 text-zinc-400">Choose whether the selected protected content stays encrypted after it returns to its original location.</p><div className="mt-5 flex flex-wrap justify-end gap-2"><button type="button" disabled={busy} onClick={() => setRestoreConfirmation(undefined)} className="min-h-10 rounded-md px-3 text-sm text-zinc-300">Cancel</button><button type="button" disabled={busy} onClick={() => { const pending=restoreConfirmation; setRestoreConfirmation(undefined); void run(pending,'restore','decrypt') }} className="min-h-10 rounded-md border border-zinc-700 px-3 text-sm text-zinc-200">Decrypt and restore</button><button type="button" disabled={busy} onClick={() => { const pending=restoreConfirmation; setRestoreConfirmation(undefined); void run(pending,'restore','keep') }} className="min-h-10 rounded-md bg-amber-500 px-3 text-sm font-medium text-zinc-950">Keep protection</button></div></section></div>}</>
}

function SaveConflictDialog({ pending, busy, error, onClose, onResolve }: {
  pending: { server: FileResponse; yourContent: string }
  busy: boolean
  error?: string
  onClose: () => void
  onResolve: (action: 'keep_mine' | 'use_server' | 'edit_combined', content?: string) => void
}) {
  const [combinedDraft, setCombinedDraft] = useState(() => ({
    serverVersion: pending.server.version,
    yourContent: pending.yourContent,
    value: pending.yourContent,
  }))
  const combinedContent = combinedDraft.serverVersion === pending.server.version && combinedDraft.yourContent === pending.yourContent
    ? combinedDraft.value
    : pending.yourContent
  return <div className="fixed inset-0 z-[70] flex items-center justify-center bg-black/80 p-3 sm:p-5">
    <section role="dialog" aria-modal="true" aria-labelledby="save-conflict-title" className="flex max-h-[92vh] w-full max-w-5xl flex-col rounded-xl border border-zinc-700 bg-zinc-900 shadow-2xl">
      <header className="border-b border-zinc-800 px-5 py-4">
        <h2 id="save-conflict-title" className="text-lg font-semibold">This note changed on the server</h2>
        <p className="mt-1 break-all text-xs text-zinc-400">{pending.server.path}</p>
        <p className="mt-2 text-sm leading-6 text-zinc-400">Your browser draft and the current server version are both preserved. Choose which content to save, or edit a combined version.</p>
      </header>
      <div className="grid min-h-0 gap-3 overflow-y-auto p-4 lg:grid-cols-2">
        <section className="min-w-0 rounded border border-zinc-800 bg-zinc-950/50 p-3"><h3 className="text-sm font-medium text-zinc-200">Your browser draft</h3><pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words text-xs leading-5 text-zinc-400">{pending.yourContent}</pre></section>
        <section className="min-w-0 rounded border border-zinc-800 bg-zinc-950/50 p-3"><h3 className="text-sm font-medium text-zinc-200">Current server version</h3><pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words text-xs leading-5 text-zinc-400">{pending.server.content}</pre></section>
        <label className="min-w-0 text-sm font-medium text-zinc-200 lg:col-span-2">Combined version<textarea value={combinedContent} onChange={(event) => setCombinedDraft({ serverVersion: pending.server.version, yourContent: pending.yourContent, value: event.target.value })} rows={12} className="mt-2 w-full rounded border border-zinc-700 bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-300" /></label>
      </div>
      <footer className="border-t border-zinc-800 p-4">
        {error && <p role="alert" className="mb-3 text-sm text-red-300">{error}</p>}
        <div className="flex flex-wrap justify-end gap-2">
          <button type="button" disabled={busy} onClick={onClose} className="min-h-10 rounded border border-zinc-700 px-3 text-sm text-zinc-300">Decide later</button>
          <button type="button" disabled={busy} onClick={() => onResolve('use_server')} className="min-h-10 rounded border border-zinc-700 px-3 text-sm text-zinc-200">Use server version</button>
          <button type="button" disabled={busy} onClick={() => onResolve('keep_mine')} className="min-h-10 rounded border border-amber-700 px-3 text-sm text-amber-200">Save my version</button>
          <button type="button" disabled={busy} onClick={() => onResolve('edit_combined', combinedContent)} className="min-h-10 rounded bg-amber-500 px-3 text-sm font-medium text-zinc-950 disabled:opacity-40">Save combined version</button>
        </div>
      </footer>
    </section>
  </div>
}

export function DocumentStatusBar({ status, stats = emptyDocumentStats }: { status: SaveStatus; stats?: DocumentStats }) {
  const labels: Record<SaveStatus, string> = { saved: 'Saved on this server', unsaved: 'Changes not saved yet', saving: 'Saving on this server…', error: 'Save could not finish', conflict: 'Your decision is required' }
  const colors: Record<SaveStatus, string> = { saved: 'text-emerald-400', unsaved: 'text-amber-300', saving: 'text-zinc-400', error: 'text-red-400', conflict: 'text-red-400' }
  return <footer aria-label="Document status" className="sticky bottom-0 z-10 flex h-7 shrink-0 items-center gap-2.5 border-t border-zinc-800 bg-zinc-950/90 px-5 text-[11px] leading-none text-zinc-500 backdrop-blur sm:px-8"><span role="status" className={`truncate font-medium ${colors[status]}`}>{labels[status]}</span><span aria-hidden="true" className="text-zinc-700">•</span><span className="hidden sm:inline" aria-label={`${stats.words} words`}>{stats.words} words</span><span className="hidden md:inline" aria-label={`${stats.characters} characters`}>{stats.characters} characters</span><span className="hidden md:inline" aria-label={`${stats.lines} lines`}>{stats.lines} lines</span></footer>
}

function SidebarMessage({ children }: { children: ReactNode }) { return <p className="px-2 py-3 text-sm text-zinc-500">{children}</p> }
function ErrorMessage({ children }: { children: ReactNode }) { return <p className="mb-5 rounded-lg border border-red-900/70 bg-red-950/30 p-4 text-sm text-red-200">{children}</p> }
function EmptyState({ notebookConfigured, onAddNotebook }: { notebookConfigured: boolean; onAddNotebook: () => void }) { return <div className="flex min-h-80 flex-col items-center justify-center text-center"><div className="rounded-2xl border border-zinc-800 bg-zinc-900 p-4 text-2xl" aria-hidden="true">◇</div><h2 className="mt-5 text-xl font-semibold">{notebookConfigured ? 'Your Markdown stays yours' : 'Create your first vault'}</h2><p className="mt-2 max-w-md text-sm leading-6 text-zinc-400">{notebookConfigured ? 'Choose a note from the vault tree to edit it with Milkdown. Changes are autosaved after a short pause.' : 'Create a vault or adopt an existing folder to browse and edit ordinary Markdown notes.'}</p>{!notebookConfigured && <button type="button" onClick={onAddNotebook} className="mt-5 min-h-11 rounded-md bg-amber-500 px-4 text-sm font-semibold text-zinc-950 hover:bg-amber-400">Add Vault</button>}</div> }
