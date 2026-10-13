import { useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { imageInlineComponent, inlineImageConfig } from '@milkdown/kit/component/image-inline'
import { commandsCtx, defaultValueCtx, Editor, editorViewCtx, editorViewOptionsCtx, parserCtx, rootCtx, schemaCtx } from '@milkdown/kit/core'
import { history, redoCommand, undoCommand } from '@milkdown/kit/plugin/history'
import { listener, listenerCtx } from '@milkdown/kit/plugin/listener'
import { upload, uploadConfig, type Uploader } from '@milkdown/kit/plugin/upload'
import { createCodeBlockCommand, insertHrCommand, liftListItemCommand, linkSchema, listItemSchema, toggleEmphasisCommand, toggleInlineCodeCommand, toggleLinkCommand, toggleStrongCommand, turnIntoTextCommand, updateLinkCommand, wrapInBlockquoteCommand, wrapInBulletListCommand, wrapInHeadingCommand, wrapInOrderedListCommand } from '@milkdown/kit/preset/commonmark'
import { addColAfterCommand, addColBeforeCommand, addRowAfterCommand, addRowBeforeCommand, gfm, insertTableCommand, toggleStrikethroughCommand } from '@milkdown/kit/preset/gfm'
import { commonmark } from '@milkdown/kit/preset/commonmark'
import { Slice, type Node, type ResolvedPos } from '@milkdown/kit/prose/model'
import { NodeSelection, TextSelection } from '@milkdown/kit/prose/state'
import type { Command, EditorState } from '@milkdown/kit/prose/state'
import type { EditorView } from '@milkdown/kit/prose/view'
import { exitCode, lift, newlineInCode } from '@milkdown/kit/prose/commands'
import { deleteColumn, deleteRow, deleteTable } from '@milkdown/kit/prose/tables'
import { $view } from '@milkdown/kit/utils'
import { Milkdown, MilkdownProvider, useEditor } from '@milkdown/react'
import { isEditorEditable } from '../../app/autoLock'
import { documentStats, type DocumentStats } from '../../app/documentStats'
import { apiFetch } from '../../api'
import { codeBlockView, codeHighlightPlugin } from './codeBlocks'

type MarkdownEditorProps = {
  documentKey: string
  notePath: string
  markdown: string
  readOnly: boolean
  onChange: (markdown: string) => void
  notePaths?: string[]
  onOpenNoteLink?: (path: string, disposition: 'current' | 'new') => void
  stickyToolbar?: boolean
  onStats?: (stats: DocumentStats) => void
}

export function MarkdownEditor(props: MarkdownEditorProps) {
  return (
    <MilkdownProvider>
      <MilkdownEditor {...props} />
    </MilkdownProvider>
  )
}

type UploadState = 'idle' | 'uploading' | 'error'
type SelectedImage = { position: number; alt: string; src: string }
type ViewedImage = { src: string; alt: string }
type ActiveImageView = ViewedImage & { contextKey: string }
type ImagePresentationSize = 'small' | 'medium' | 'large' | 'full'
type ImagePresentationState = { contextKey: string; values: Record<string, ImagePresentationSize> }
type ToolbarState = { block: string; strong: boolean; emphasis: boolean; strike: boolean; code: boolean; link: boolean; bullet: boolean; ordered: boolean; task: boolean; quote: boolean; table: boolean }
type TableSize = { rows: number; columns: number }
type SlashState = { from: number; to: number; query: string; left: number; top: number }
type NoteLinkTriggerState = { from:number; to:number; query:string; left:number; top:number; documentKey:string }
type NoteLinkTriggerIntent = { from:number; blockContext:string }
type NoteLinkOpening = { from:number; nextPosition:number; blockContext:string }
type SlashCommandID = 'paragraph' | 'heading-1' | 'heading-2' | 'heading-3' | 'heading-4' | 'heading-5' | 'heading-6' | 'bullet-list' | 'numbered-list' | 'task-list' | 'blockquote' | 'code-block' | 'inline-code' | 'link' | 'image' | 'table' | 'horizontal-rule'
type SlashCommand = { id: SlashCommandID; label: string; description: string; keywords: string }
type LinkDraft = { from: number; to: number; selectedText: string; existingHref?: string }
type SelectedLink = { href: string; targetPath?: string; exists: boolean }
type MarkdownPasteDraft = { text: string; error?: string; clipboardHint?: string }
type OutlineHeading = { position: number; level: number; text: string }
const emptyToolbarState: ToolbarState = { block: 'paragraph', strong: false, emphasis: false, strike: false, code: false, link: false, bullet: false, ordered: false, task: false, quote: false, table: false }
const imagePresentationSizes: ImagePresentationSize[] = ['small', 'medium', 'large', 'full']
const emptyImagePresentations: Record<string, ImagePresentationSize> = {}
const externalImagePlaceholderPrefix = 'data:image/gif;base64,R0lGODlhAQABAAD/ACwAAAAAAQABAAACADs=#varianotes-external='

function isExternalImageSource(source: string): boolean {
  return source.startsWith('//') || /^https?:/i.test(source)
}

function externalImagePlaceholderURL(source: string): string {
  return `${externalImagePlaceholderPrefix}${encodeURIComponent(source)}`
}

function externalSourceFromPlaceholderURL(source: string | null): string | undefined {
  if (!source?.startsWith(externalImagePlaceholderPrefix)) return undefined
  try { return decodeURIComponent(source.slice(externalImagePlaceholderPrefix.length)) } catch { return undefined }
}

function updateExternalImagePlaceholder(wrapper: HTMLElement, image: HTMLImageElement, source: string): void {
  if (wrapper.dataset.varianotesExternalImageSource !== source) {
    wrapper.dataset.varianotesExternalImageSource = source
    wrapper.dataset.varianotesExternalImageLoaded = 'false'
    delete wrapper.dataset.varianotesExternalImageFailed
    wrapper.querySelector('.varianotes-external-image-placeholder')?.remove()
  }

  if (image.dataset.varianotesExternalImageErrorHandler !== 'true') {
    image.dataset.varianotesExternalImageErrorHandler = 'true'
    image.addEventListener('error', () => {
      const currentWrapper = image.closest<HTMLElement>('.milkdown-image-inline')
      const currentSource = currentWrapper?.dataset.varianotesExternalImageSource
      if (!currentWrapper || !currentSource || currentWrapper.dataset.varianotesExternalImageLoaded !== 'true') return
      currentWrapper.dataset.varianotesExternalImageFailed = 'true'
      updateExternalImagePlaceholder(currentWrapper, image, currentSource)
    })
  }

  const failed = wrapper.dataset.varianotesExternalImageFailed === 'true'
  if (wrapper.dataset.varianotesExternalImageLoaded === 'true' && !failed) {
    wrapper.querySelector('.varianotes-external-image-placeholder')?.remove()
    image.style.display = ''
    if (image.getAttribute('src') !== source) image.setAttribute('src', source)
    return
  }

  image.style.display = 'none'
  let placeholder = wrapper.querySelector<HTMLSpanElement>('.varianotes-external-image-placeholder')
  if (!placeholder) {
    placeholder = document.createElement('span')
    placeholder.className = 'varianotes-external-image-placeholder'
    placeholder.setAttribute('role', 'group')
    placeholder.setAttribute('aria-label', 'External image')

    const details = document.createElement('span')
    details.className = 'varianotes-external-image-details'
    const label = document.createElement('span')
    label.className = 'varianotes-external-image-label'
    const note = document.createElement('span')
    note.className = 'varianotes-external-image-note'
    note.textContent = 'Loading it sends a request to that site.'
    details.append(label, note)

    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'varianotes-external-image-load'
    button.addEventListener('mousedown', (event) => event.preventDefault())
    button.addEventListener('click', (event) => {
      event.preventDefault()
      event.stopPropagation()
      wrapper.dataset.varianotesExternalImageLoaded = 'true'
      delete wrapper.dataset.varianotesExternalImageFailed
      placeholder?.remove()
      image.style.display = ''
      image.referrerPolicy = 'no-referrer'
      image.setAttribute('src', wrapper.dataset.varianotesExternalImageSource ?? source)
    })
    placeholder.append(details, button)
    wrapper.append(placeholder)
  }

  const host = (() => {
    try { return new URL(source, globalThis.location?.href ?? 'https://localhost').hostname || 'external host' } catch { return 'external host' }
  })()
  const label = placeholder.querySelector<HTMLElement>('.varianotes-external-image-label')
  const labelText = failed ? `Could not load image from ${host}.` : `External image from ${host}`
  if (label && label.textContent !== labelText) label.textContent = labelText
  const button = placeholder.querySelector<HTMLButtonElement>('button')
  if (button) {
    const buttonText = failed ? 'Try again' : 'Load image'
    const accessibleLabel = `${failed ? 'Retry loading' : 'Load'} external image from ${host}`
    if (button.textContent !== buttonText) button.textContent = buttonText
    if (button.getAttribute('aria-label') !== accessibleLabel) button.setAttribute('aria-label', accessibleLabel)
  }
}

function clearExternalImagePlaceholder(wrapper: HTMLElement, image: HTMLImageElement): void {
  delete wrapper.dataset.varianotesExternalImageSource
  delete wrapper.dataset.varianotesExternalImageLoaded
  delete wrapper.dataset.varianotesExternalImageFailed
  wrapper.querySelector('.varianotes-external-image-placeholder')?.remove()
  image.style.display = ''
}

function clearProtectedAttachmentPlaceholder(wrapper: HTMLElement, image: HTMLImageElement): void {
  delete wrapper.dataset.varianotesProtectedAttachmentSource
  wrapper.querySelector('.varianotes-protected-attachment-placeholder')?.remove()
  image.style.display = ''
}

function watchProtectedAttachmentErrors(image: HTMLImageElement): void {
  if (image.dataset.varianotesProtectedAttachmentErrorHandler === 'true') return
  image.dataset.varianotesProtectedAttachmentErrorHandler = 'true'
  image.addEventListener('error', () => {
    const source = image.getAttribute('src')
    if (!source) return
    let assetURL: URL
    try { assetURL = new URL(source, globalThis.location?.href ?? 'http://localhost') } catch { return }
    if (assetURL.pathname !== '/api/repository/asset') return
    void fetch(source, { method: 'HEAD', credentials: 'same-origin' }).then((response) => {
      if (response.status !== 423) return
      const wrapper = image.closest<HTMLElement>('.milkdown-image-inline')
      if (!wrapper) return
      wrapper.dataset.varianotesProtectedAttachmentSource = source
      image.style.display = 'none'
      let placeholder = wrapper.querySelector<HTMLSpanElement>('.varianotes-protected-attachment-placeholder')
      if (!placeholder) {
        placeholder = document.createElement('span')
        placeholder.className = 'varianotes-protected-attachment-placeholder'
        placeholder.setAttribute('role', 'img')
        placeholder.setAttribute('aria-label', 'Geschützter Anhang')
        placeholder.textContent = 'Geschützter Anhang'
        wrapper.append(placeholder)
      }
    }).catch(() => undefined)
  })
}

function documentOutline(document: Node): OutlineHeading[] {
  const headings: OutlineHeading[] = []
  document.descendants((node, position) => {
    if (node.type.name === 'heading') {
      headings.push({ position, level: Number(node.attrs.level) || 1, text: node.textContent.trim() })
      return false
    }
    return true
  })
  return headings
}

function sameOutline(left: OutlineHeading[], right: OutlineHeading[]): boolean {
  return left.length === right.length && left.every((heading, index) => {
    const candidate = right[index]
    return heading.position === candidate.position && heading.level === candidate.level && heading.text === candidate.text
  })
}

function currentOutlinePosition(view: EditorView, headings: OutlineHeading[]): number | undefined {
  const stickyToolbar = view.dom.closest('.varianotes-editor-layout')?.querySelector<HTMLElement>('.varianotes-editor-toolbars[data-sticky="true"]')
  const toolbarBottom = stickyToolbar?.getBoundingClientRect().bottom
  const threshold = toolbarBottom !== undefined && toolbarBottom > 0 ? toolbarBottom + 8 : 128
  let preceding: number | undefined
  let following: number | undefined
  for (const heading of headings) {
    const dom = view.nodeDOM(heading.position)
    if (!(dom instanceof HTMLElement)) continue
    if (dom.getBoundingClientRect().top <= threshold) preceding = heading.position
    else if (following === undefined) following = heading.position
  }
  return preceding ?? following
}

function isNarrowViewport(): boolean {
  return typeof window !== 'undefined' && (window.matchMedia?.('(max-width: 767px)').matches ?? window.innerWidth <= 767)
}

function requestFrame(callback: FrameRequestCallback): number {
  return typeof window.requestAnimationFrame === 'function'
    ? window.requestAnimationFrame(callback)
    : window.setTimeout(() => callback(Date.now()), 16)
}

function cancelFrame(handle: number): void {
  if (typeof window.cancelAnimationFrame === 'function') window.cancelAnimationFrame(handle)
  else window.clearTimeout(handle)
}

const slashCommands: SlashCommand[] = [
  { id: 'paragraph', label: 'Paragraph', description: 'Normal text block', keywords: 'text paragraph absatz' },
  { id: 'heading-1', label: 'Heading 1', description: 'Large section heading', keywords: 'h1 title heading überschrift' },
  { id: 'heading-2', label: 'Heading 2', description: 'Medium section heading', keywords: 'h2 heading überschrift' },
  { id: 'heading-3', label: 'Heading 3', description: 'Small section heading', keywords: 'h3 heading überschrift' },
  { id: 'heading-4', label: 'Heading 4', description: 'Fourth-level heading', keywords: 'h4 heading überschrift' },
  { id: 'heading-5', label: 'Heading 5', description: 'Fifth-level heading', keywords: 'h5 heading überschrift' },
  { id: 'heading-6', label: 'Heading 6', description: 'Sixth-level heading', keywords: 'h6 heading überschrift' },
  { id: 'bullet-list', label: 'Bullet list', description: 'Unordered list', keywords: 'list bullet aufzählung' },
  { id: 'numbered-list', label: 'Numbered list', description: 'Ordered list', keywords: 'list numbered ordered nummeriert' },
  { id: 'task-list', label: 'Task list', description: 'Checklist with checkboxes', keywords: 'task todo checklist aufgabe' },
  { id: 'blockquote', label: 'Blockquote', description: 'Quoted paragraph', keywords: 'quote citation zitat' },
  { id: 'code-block', label: 'Code block', description: 'Multiline code', keywords: 'code block preformatted' },
  { id: 'inline-code', label: 'Inline code', description: 'Code inside a paragraph', keywords: 'code inline' },
  { id: 'link', label: 'Link', description: 'Insert a Markdown link', keywords: 'url href link' },
  { id: 'image', label: 'Image', description: 'Upload and insert an image', keywords: 'image picture photo bild' },
  { id: 'table', label: 'Table', description: 'Choose rows and columns', keywords: 'table grid tabelle' },
  { id: 'horizontal-rule', label: 'Horizontal rule', description: 'Section divider', keywords: 'rule divider separator trennlinie' },
]

function filteredSlashCommands(query: string): SlashCommand[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return slashCommands
  return slashCommands.filter((command) => `${command.label} ${command.keywords}`.toLowerCase().includes(normalized))
}

function filteredNotePaths(query:string, notePaths:string[], currentPath:string):string[] {
  const normalized = query.trim().toLowerCase()
  return notePaths.filter((path) => path !== currentPath && (!normalized || path.toLowerCase().includes(normalized))).slice(0,100)
}

function noteLinkBlockContext(position: ResolvedPos): string {
  const context: string[] = []
  for (let depth = 0; depth <= position.depth; depth += 1) {
    const node = position.node(depth)
    context.push(`${node.type.name}:${JSON.stringify(node.attrs)}`)
  }
  return context.join('/')
}

function imageAssetPath(source: string, notePath: string): string | undefined {
  if (!source) return undefined
  if (!/^(?:https?:|data:|blob:|\/)/i.test(source)) return source
  try {
    const parsed = new URL(source, globalThis.location?.origin ?? 'http://localhost')
    if (parsed.pathname !== '/api/repository/asset' || parsed.searchParams.get('note') !== notePath) return undefined
    return parsed.searchParams.get('path') ?? undefined
  } catch {
    return undefined
  }
}

function toolbarStateFromEditor(state: EditorState): ToolbarState {
  const { $from, from, to, empty } = state.selection
  const markActive = (name: string) => {
    const mark = state.schema.marks[name]
    if (!mark) return false
    return empty ? Boolean(mark.isInSet(state.storedMarks ?? $from.marks())) : state.doc.rangeHasMark(from, to, mark)
  }
  let block = 'paragraph'
  let bullet = false
  let ordered = false
  let task = false
  let quote = false
  let table = false
  for (let depth = $from.depth; depth >= 0; depth -= 1) {
    const node = $from.node(depth)
    if (node.type.name === 'heading') block = `heading-${node.attrs.level}`
    if (node.type.name === 'code_block') block = 'code-block'
    if (node.type.name === 'bullet_list') bullet = true
    if (node.type.name === 'ordered_list') ordered = true
    if (node.type.name === 'list_item' && node.attrs.checked !== null) task = true
    if (node.type.name === 'blockquote') quote = true
    if (node.type.name === 'table') table = true
  }
  return { block, strong: markActive('strong'), emphasis: markActive('emphasis'), strike: markActive('strike_through'), code: markActive('inlineCode'), link: markActive('link'), bullet, ordered, task, quote, table }
}

function resolveInternalNotePath(notePath: string, href: string): string | undefined {
  if (!href || href.startsWith('#') || href.startsWith('/') || href.includes('\\') || /^[a-z][a-z0-9+.-]*:/i.test(href)) return undefined
  const withoutSuffix = href.split(/[?#]/, 1)[0]
  let decoded: string
  try { decoded = decodeURIComponent(withoutSuffix) } catch { return undefined }
  if (!decoded.toLowerCase().endsWith('.md')) return undefined
  const stack = notePath.split('/').slice(0, -1)
  for (const part of decoded.split('/')) {
    if (!part || part === '.') continue
    if (part === '..') {
      if (!stack.length) return undefined
      stack.pop()
    } else stack.push(part)
  }
  return stack.join('/')
}

function portableRelativeNoteHref(notePath: string, targetPath: string): string {
  const source = notePath.split('/').slice(0, -1)
  const target = targetPath.split('/')
  let common = 0
  while (common < source.length && common < target.length && source[common] === target[common]) common += 1
  const parts = [...Array.from({ length: source.length - common }, () => '..'), ...target.slice(common).map((part) => encodeURIComponent(part))]
  return parts.join('/') || encodeURIComponent(target[target.length - 1])
}

function markdownConversionError(document: Node): string | undefined {
  let error: string | undefined
  document.descendants((node) => {
    if (error) return false
    if (node.type.name === 'html') {
      error = 'Raw HTML is not inserted as rendered content. This keeps pasted notes safe and portable.'
      return false
    }
    if (node.type.name === 'image') {
      const source = String(node.attrs.src ?? '').trim()
      if (!source || (source.startsWith('/') && !source.startsWith('//')) || (/^[a-z][a-z0-9+.-]*:/i.test(source) && !isExternalImageSource(source))) {
        error = 'Only relative notebook paths and external HTTP(S) image URLs are supported in pasted Markdown.'
        return false
      }
    }
    return true
  })
  return error
}

function insertParsedMarkdown(view: EditorView, parse: (markdown: string) => Node, markdown: string): string | undefined {
  if (!markdown.trim()) return 'Paste or enter some Markdown first.'
  try {
    const document = parse(markdown)
    const validationError = markdownConversionError(document)
    if (validationError) return validationError
    view.dispatch(view.state.tr.replaceSelection(Slice.maxOpen(document.content)).scrollIntoView())
    view.focus()
    return undefined
  } catch {
    return 'This Markdown could not be converted. The original text is still available and has not been lost.'
  }
}

const interactiveListItemView = $view(listItemSchema.node, () => (initialNode, editorView, getPos) => {
  const dom = document.createElement('li')
  dom.className = 'varianotes-list-item'

  const checkbox = document.createElement('button')
  checkbox.type = 'button'
  checkbox.setAttribute('role', 'checkbox')
  checkbox.setAttribute('contenteditable', 'false')
  checkbox.className = 'varianotes-task-checkbox'

  const contentDOM = document.createElement('div')
  contentDOM.className = 'varianotes-list-item-content'
  dom.append(checkbox, contentDOM)

  let currentNode = initialNode
  const render = (node: Node) => {
    const isTask = typeof node.attrs.checked === 'boolean'
    const checked = node.attrs.checked === true
    checkbox.hidden = !isTask
    checkbox.disabled = !editorView.editable
    checkbox.setAttribute('aria-checked', String(checked))
    checkbox.setAttribute('aria-disabled', String(!editorView.editable))
    checkbox.setAttribute('aria-label', node.textContent.trim() ? `Task: ${node.textContent.trim().slice(0, 120)}` : 'Task item')
    checkbox.title = checked ? 'Mark task incomplete' : 'Mark task complete'
    checkbox.textContent = checked ? '☑' : '☐'
    checkbox.classList.toggle('is-checked', checked)
    if (isTask) {
      dom.setAttribute('data-item-type', 'task')
      dom.setAttribute('data-checked', String(checked))
    } else {
      dom.removeAttribute('data-item-type')
      dom.removeAttribute('data-checked')
    }
  }

  const toggle = (event: MouseEvent) => {
    event.preventDefault()
    event.stopPropagation()
    if (!editorView.editable) return
    const position = getPos()
    if (typeof position !== 'number') return
    const node = editorView.state.doc.nodeAt(position)
    if (!node || node.type !== currentNode.type || typeof node.attrs.checked !== 'boolean') return
    editorView.dispatch(editorView.state.tr.setNodeAttribute(position, 'checked', !node.attrs.checked).scrollIntoView())
  }
  checkbox.addEventListener('click', toggle)
  render(initialNode)

  return {
    dom,
    contentDOM,
    update: (node) => {
      if (node.type !== initialNode.type) return false
      currentNode = node
      render(node)
      return true
    },
    stopEvent: (event) => event.target instanceof HTMLElement && checkbox.contains(event.target),
    ignoreMutation: (mutation) => mutation.type !== 'selection' && !contentDOM.contains(mutation.target),
    selectNode: () => dom.classList.add('ProseMirror-selectednode'),
    deselectNode: () => dom.classList.remove('ProseMirror-selectednode'),
    destroy: () => checkbox.removeEventListener('click', toggle),
  }
})

function MilkdownEditor({ documentKey, notePath, markdown, readOnly, onChange, notePaths = [], onOpenNoteLink, stickyToolbar = false, onStats }: MarkdownEditorProps) {
  const input = useRef<HTMLInputElement>(null)
  const insertMenuButton = useRef<HTMLButtonElement>(null)
  const insertMenuPanel = useRef<HTMLDivElement>(null)
  const replacementInput = useRef<HTMLInputElement>(null)
  const editorContainer = useRef<HTMLDivElement>(null)
  const outlineToggle = useRef<HTMLButtonElement>(null)
  const outlineDock = useRef<HTMLDivElement>(null)
  const outlineUpdateTimer = useRef<number | null>(null)
  const outlineLastUpdate = useRef(0)
  const viewerReturnFocus = useRef<HTMLElement | null>(null)
  const [uploadState, setUploadState] = useState<UploadState>('idle')
  const [uploadError, setUploadError] = useState<string>()
  const [selectedImage, setSelectedImage] = useState<SelectedImage>()
  const [viewedImage, setViewedImage] = useState<ActiveImageView>()
  const [imagePresentations, setImagePresentations] = useState<ImagePresentationState>()
  const [presentationError, setPresentationError] = useState<{ contextKey: string; message: string }>()
  const [editingAlt, setEditingAlt] = useState<string>()
  const [toolbarState, setToolbarState] = useState<ToolbarState>(emptyToolbarState)
  const [insertMenuOpen, setInsertMenuOpen] = useState(false)
  const [outlineOpen, setOutlineOpen] = useState(false)
  const outlineOpenRef = useRef(outlineOpen)
  const [outlineHeadings, setOutlineHeadings] = useState<OutlineHeading[]>([])
  const [activeOutlinePosition, setActiveOutlinePosition] = useState<number>()
  const [narrowViewport, setNarrowViewport] = useState(isNarrowViewport)
  const [tablePickerOpen, setTablePickerOpen] = useState(false)
  const [tableSize, setTableSize] = useState<TableSize>({ rows: 3, columns: 3 })
  const [slashState, setSlashState] = useState<SlashState>()
  const [slashIndex, setSlashIndex] = useState(0)
  const [linkPicker, setLinkPicker] = useState<LinkDraft>()
  const [selectedLink, setSelectedLink] = useState<SelectedLink>()
  const [linkError, setLinkError] = useState<string>()
  const [markdownPaste, setMarkdownPaste] = useState<MarkdownPasteDraft>()
  const [pasteNotice, setPasteNotice] = useState<string>()
  const [noteLinkTrigger, setNoteLinkTrigger] = useState<NoteLinkTriggerState>()
  const [noteLinkTriggerIndex, setNoteLinkTriggerIndex] = useState(0)
  const slashStateRef = useRef<SlashState | undefined>(undefined)
  const slashIndexRef = useRef(0)
  const notePathsRef = useRef(notePaths)
  const openNoteLinkRef = useRef(onOpenNoteLink)
  const noteLinkTriggerRef = useRef<NoteLinkTriggerState | undefined>(undefined)
  const noteLinkTriggerIntentRef = useRef<NoteLinkTriggerIntent | undefined>(undefined)
  const noteLinkOpeningRef = useRef<NoteLinkOpening | undefined>(undefined)
  const noteLinkOpeningKeyDownRef = useRef(false)
  const noteLinkTriggerIndexRef = useRef(0)
  // Compatibility layer for VariaNotes's empty-cursor inline-code workflow.
  // Milkdown 7.22 intentionally makes the mark non-inclusive, so ProseMirror
  // otherwise drops it after the first typed character. Keep this explicit
  // mode until Milkdown offers an equivalent empty-selection command itself.
  const inlineCodeTypingRef = useRef(false)
  const presentationRequestRef = useRef('')
  notePathsRef.current = notePaths
  openNoteLinkRef.current = onOpenNoteLink
  const imageContextKey = `${documentKey}\u0000${notePath}`
  const activeViewedImage = viewedImage?.contextKey === imageContextKey ? viewedImage : undefined
  const activeImagePresentations = imagePresentations?.contextKey === imageContextKey ? imagePresentations.values : emptyImagePresentations
  outlineOpenRef.current = outlineOpen

  useEffect(() => {
    const updateViewport = () => setNarrowViewport(isNarrowViewport())
    const media = window.matchMedia?.('(max-width: 767px)')
    media?.addEventListener?.('change', updateViewport)
    window.addEventListener('resize', updateViewport)
    return () => {
      media?.removeEventListener?.('change', updateViewport)
      window.removeEventListener('resize', updateViewport)
    }
  }, [])

  useEffect(() => () => {
    if (outlineUpdateTimer.current !== null) window.clearTimeout(outlineUpdateTimer.current)
  }, [])

  useEffect(() => {
    if (!outlineOpen) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        setOutlineOpen(false)
        requestFrame(() => outlineToggle.current?.focus())
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [outlineOpen])

  useEffect(() => {
    const dock = outlineDock.current
    const container = editorContainer.current
    if (!dock || !container) return
    const toolbar = container.closest('.varianotes-editor-layout')?.querySelector<HTMLElement>('.varianotes-editor-toolbars')
    const scroller = container.closest('.varianotes-note-scroll') as HTMLElement | null
    const updateDockPosition = () => {
      const editorTop = container.getBoundingClientRect().top
      const toolbarTop = toolbar?.getBoundingClientRect().top
      const panelTop = toolbarTop ?? editorTop
      const scrollbarWidth = scroller ? Math.max(0, scroller.offsetWidth - scroller.clientWidth) : 0
      dock.style.top = `${Math.max(0, Math.round(panelTop))}px`
      dock.style.right = `${Math.round(scrollbarWidth)}px`
      dock.dataset.positioned = 'true'
    }
    const resizeObserver = new ResizeObserver(updateDockPosition)
    resizeObserver.observe(container)
    if (toolbar) resizeObserver.observe(toolbar)
    if (scroller) resizeObserver.observe(scroller)
    window.addEventListener('scroll', updateDockPosition, true)
    window.addEventListener('resize', updateDockPosition)
    updateDockPosition()
    return () => {
      resizeObserver.disconnect()
      window.removeEventListener('scroll', updateDockPosition, true)
      window.removeEventListener('resize', updateDockPosition)
    }
  }, [outlineOpen])

  useEffect(() => {
    if (!pasteNotice) return
    const timeout = globalThis.setTimeout(() => setPasteNotice(undefined), 10_000)
    return () => globalThis.clearTimeout(timeout)
  }, [pasteNotice])

  useEffect(() => {
    if (!insertMenuOpen) return
    const closeOnOutsideInteraction = (event: PointerEvent) => {
      const target = event.target
      if (!(target instanceof Element)) return
      if (insertMenuButton.current?.contains(target) || insertMenuPanel.current?.contains(target)) return
      setInsertMenuOpen(false)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      setInsertMenuOpen(false)
      requestFrame(() => insertMenuButton.current?.focus())
    }
    document.addEventListener('pointerdown', closeOnOutsideInteraction)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('pointerdown', closeOnOutsideInteraction)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [insertMenuOpen])

  useEffect(() => {
    const reset = globalThis.setTimeout(() => {
      setInsertMenuOpen(false)
      noteLinkTriggerIntentRef.current = undefined
      noteLinkOpeningRef.current = undefined
      noteLinkOpeningKeyDownRef.current = false
      noteLinkTriggerRef.current = undefined
      noteLinkTriggerIndexRef.current = 0
      setNoteLinkTrigger(undefined)
      setNoteLinkTriggerIndex(0)
    }, 0)
    return () => globalThis.clearTimeout(reset)
  }, [documentKey, readOnly])

  useEffect(() => {
    const closeOnOutsideInteraction = (event: PointerEvent) => {
      if (!noteLinkTriggerRef.current && !noteLinkOpeningRef.current) return
      const target = event.target
      if (!(target instanceof Element)) return
      if (editorContainer.current?.contains(target) || target.closest('[data-varianotes-note-link-menu]')) return
      noteLinkTriggerIntentRef.current = undefined
      noteLinkOpeningRef.current = undefined
      noteLinkOpeningKeyDownRef.current = false
      noteLinkTriggerRef.current = undefined
      noteLinkTriggerIndexRef.current = 0
      setNoteLinkTrigger(undefined)
      setNoteLinkTriggerIndex(0)
    }
    document.addEventListener('pointerdown', closeOnOutsideInteraction)
    return () => document.removeEventListener('pointerdown', closeOnOutsideInteraction)
  }, [])

  const loadImagePresentations = useCallback(async () => {
    if (presentationRequestRef.current === imageContextKey) return
    const requestContext = imageContextKey
    presentationRequestRef.current = requestContext
    try {
      const response = await apiFetch(`/api/repository/image-presentations?note=${encodeURIComponent(notePath)}`)
      const result = await response.json() as { presentations?: Record<string, ImagePresentationSize>; error?: string }
      if (!response.ok) throw new Error(result.error ?? `Image presentation settings failed (${response.status})`)
      if (presentationRequestRef.current === requestContext) {
        setImagePresentations({ contextKey: requestContext, values: result.presentations ?? {} })
        setPresentationError(undefined)
      }
    } catch (error) {
      if (presentationRequestRef.current === requestContext) {
        setPresentationError({ contextKey: requestContext, message: error instanceof Error ? error.message : 'Image presentation settings are unavailable' })
      }
    }
  }, [imageContextKey, notePath])

  useEffect(() => {
    const container = editorContainer.current
    if (!container) return
    const decorateImages = () => {
      const images = container.querySelectorAll<HTMLImageElement>('.varianotes-editor .milkdown-image-inline img')
      if (Array.from(images).some((image) => imageAssetPath(image.src, notePath))) void loadImagePresentations()
      for (const wrapper of container.querySelectorAll<HTMLElement>('.varianotes-editor .milkdown-image-inline')) {
        if (wrapper.querySelector('img') || !wrapper.dataset.varianotesExternalImageSource) continue
        delete wrapper.dataset.varianotesExternalImageSource
        delete wrapper.dataset.varianotesExternalImageLoaded
        delete wrapper.dataset.varianotesExternalImageFailed
        wrapper.querySelector('.varianotes-external-image-placeholder')?.remove()
      }
      for (const image of images) {
        const wrapper = image.closest<HTMLElement>('.milkdown-image-inline')
        const currentSource = image.getAttribute('src')
        if (wrapper?.dataset.varianotesProtectedAttachmentSource && wrapper.dataset.varianotesProtectedAttachmentSource !== currentSource) {
          clearProtectedAttachmentPlaceholder(wrapper, image)
        }
        watchProtectedAttachmentErrors(image)
        const placeholderSource = externalSourceFromPlaceholderURL(currentSource)
        const storedExternalSource = wrapper?.dataset.varianotesExternalImageSource
        const externalSource = placeholderSource ?? (currentSource && isExternalImageSource(currentSource) ? currentSource : undefined) ?? (currentSource === storedExternalSource ? storedExternalSource : undefined)
        if (wrapper && externalSource) updateExternalImagePlaceholder(wrapper, image, externalSource)
        else if (wrapper && storedExternalSource) clearExternalImagePlaceholder(wrapper, image)
        if (wrapper?.dataset.varianotesExternalImageSource && (wrapper.dataset.varianotesExternalImageLoaded !== 'true' || wrapper.dataset.varianotesExternalImageFailed === 'true')) continue

        const assetPath = imageAssetPath(image.src, notePath)
        if (wrapper) wrapper.dataset.presentationSize = assetPath ? activeImagePresentations[assetPath] ?? 'full' : 'full'
        if (readOnly) {
          image.tabIndex = 0
          image.setAttribute('role', 'button')
          image.setAttribute('aria-label', image.alt ? `View image: ${image.alt}` : 'View note image')
          image.dataset.varianotesViewerTrigger = 'true'
        } else if (image.dataset.varianotesViewerTrigger) {
          image.removeAttribute('tabindex')
          image.removeAttribute('role')
          image.removeAttribute('aria-label')
          delete image.dataset.varianotesViewerTrigger
        }
      }
    }
    decorateImages()
    const observer = new MutationObserver(decorateImages)
    observer.observe(container, { attributes: true, attributeFilter: ['src'], childList: true, subtree: true })
    return () => observer.disconnect()
  }, [documentKey, readOnly, activeImagePresentations, loadImagePresentations, notePath])


  async function uploadImage(file: File): Promise<string> {
    if (readOnly) throw new Error('Switch to Edit before inserting an image')
    setUploadState('uploading')
    setUploadError(undefined)
    try {
      const form = new FormData()
      form.append('file', file)
      const response = await apiFetch(`/api/repository/assets?note=${encodeURIComponent(notePath)}`, { method: 'POST', body: form })
      const result = await response.json() as { path?: string; error?: string }
      if (!response.ok || !result.path) throw new Error(result.error ?? `Image upload failed (${response.status})`)
      setUploadState('idle')
      return result.path
    } catch (error) {
      setUploadState('error')
      setUploadError(error instanceof Error ? error.message : 'Image upload failed')
      throw error
    }
  }

  const uploader: Uploader = async (files, schema) => {
    if (readOnly) return []
    const images = Array.from(files).filter((file) => file.type.startsWith('image/'))
    const nodes = await Promise.all(images.map(async (file) => schema.nodes.image.createAndFill({ src: await uploadImage(file), alt: '' })))
    return nodes.filter((node): node is Node => node !== null)
  }

  function displayURL(source: string): string {
    if (/^(?:https?:|data:|blob:|\/)/i.test(source)) return source
    return `/api/repository/asset?note=${encodeURIComponent(notePath)}&path=${encodeURIComponent(source)}`
  }

  function inlineImageURL(source: string): string {
    return isExternalImageSource(source) ? externalImagePlaceholderURL(source) : displayURL(source)
  }

  async function saveImagePresentation(image: string, size: ImagePresentationSize, previousImage = '') {
    const previousValues = activeImagePresentations
    setImagePresentations({ contextKey: imageContextKey, values: { ...previousValues, [image]: size } })
    setPresentationError(undefined)
    try {
      const response = await apiFetch('/api/repository/image-presentation', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ note: notePath, image, size, previousImage }) })
      const result = await response.json() as { error?: string }
      if (!response.ok) throw new Error(result.error ?? `Image presentation setting failed (${response.status})`)
      if (previousImage && previousImage !== image) {
        setImagePresentations((current) => {
          const values = { ...(current?.contextKey === imageContextKey ? current.values : previousValues) }
          delete values[previousImage]
          values[image] = size
          return { contextKey: imageContextKey, values }
        })
      }
    } catch (error) {
      setImagePresentations({ contextKey: imageContextKey, values: previousValues })
      setPresentationError({ contextKey: imageContextKey, message: error instanceof Error ? error.message : 'Image presentation setting could not be saved' })
    }
  }

  async function removeImagePresentation(image: string) {
    const previousValues = activeImagePresentations
    const values = { ...previousValues }
    delete values[image]
    setImagePresentations({ contextKey: imageContextKey, values })
    try {
      const response = await apiFetch(`/api/repository/image-presentation?note=${encodeURIComponent(notePath)}&image=${encodeURIComponent(image)}`, { method: 'DELETE' })
      if (!response.ok) {
        const result = await response.json() as { error?: string }
        throw new Error(result.error ?? `Image presentation setting cleanup failed (${response.status})`)
      }
    } catch (error) {
      setPresentationError({ contextKey: imageContextKey, message: error instanceof Error ? error.message : 'Image presentation setting could not be removed' })
    }
  }

  function openImageViewer(image: ViewedImage, trigger?: HTMLElement | null) {
    viewerReturnFocus.current = trigger ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null)
    setViewedImage({ ...image, contextKey: imageContextKey })
  }

  function closeImageViewer() {
    setViewedImage(undefined)
    const returnTarget = viewerReturnFocus.current
    viewerReturnFocus.current = null
    globalThis.setTimeout(() => returnTarget?.isConnected && returnTarget.focus(), 0)
  }

  function updateSelectedLink(state: EditorState) {
    const mark = state.schema.marks.link?.isInSet(state.storedMarks ?? state.selection.$from.marks())
    if (!mark) {
      setSelectedLink(undefined)
      return
    }
    const href = String(mark.attrs.href ?? '')
    const targetPath = resolveInternalNotePath(notePath, href)
    setSelectedLink({ href, targetPath, exists: Boolean(targetPath && notePathsRef.current.includes(targetPath)) })
  }

  const recordRawNoteLinkOpening = useCallback((view: EditorView) => {
    if (!view.state.selection.empty) {
      noteLinkOpeningRef.current = undefined
      return
    }
    const { $from } = view.state.selection
    if (!$from.parent.isTextblock || $from.parent.type.name === 'code_block') {
      noteLinkOpeningRef.current = undefined
      return
    }
    const blockContext = noteLinkBlockContext($from)
    const pending = noteLinkOpeningRef.current
    if (pending && pending.nextPosition === $from.pos && pending.blockContext === blockContext) {
      noteLinkTriggerIntentRef.current = { from: pending.from, blockContext }
      noteLinkOpeningRef.current = undefined
      return
    }
    noteLinkOpeningRef.current = { from: $from.pos, nextPosition: $from.pos + 1, blockContext }
  }, [])

  const { get } = useEditor(
    (root) =>
      Editor.make()
        .config((ctx) => {
          const queueOutlineRefresh = (view: EditorView) => {
            if (!outlineOpenRef.current || outlineUpdateTimer.current !== null) return
            const delay = Math.max(0, 120 - (Date.now() - outlineLastUpdate.current))
            outlineUpdateTimer.current = window.setTimeout(() => {
              outlineUpdateTimer.current = null
              if (!outlineOpenRef.current || view.isDestroyed) return
              outlineLastUpdate.current = Date.now()
              const headings = documentOutline(view.state.doc)
              setOutlineHeadings((previous) => sameOutline(previous, headings) ? previous : headings)
              setActiveOutlinePosition(currentOutlinePosition(view, headings))
            }, delay)
          }
          ctx.set(rootCtx, root)
          ctx.set(defaultValueCtx, markdown)
          ctx.update(editorViewOptionsCtx, (previous) => ({
            ...previous,
            editable: () => isEditorEditable(readOnly),
            attributes: {
              ...previous.attributes,
              class: 'varianotes-editor',
              'aria-label': 'Markdown editor',
              tabindex: '0',
            },
            handleTextInput: (view, from, to, text, defaultHandler) => {
              if (inlineCodeTypingRef.current && view.state.selection.empty) {
                const inlineCode = view.state.schema.marks.inlineCode
                if (inlineCode) {
                  view.dispatch(view.state.tr.insertText(text, from, to).addStoredMark(inlineCode.create()))
                  return true
                }
              }
              return previous.handleTextInput?.(view, from, to, text, defaultHandler) ?? false
            },
            handlePaste: (view, event, slice) => {
              const clipboard = event.clipboardData
              const types = Array.from(clipboard?.types ?? [])
              if (readOnly || !clipboard || clipboard.files.length > 0 || view.state.selection.$from.parent.type.spec.code || !types.includes('text/markdown')) {
                return previous.handlePaste?.(view, event, slice) ?? false
              }
              const source = clipboard.getData('text/markdown')
              if (!source) return previous.handlePaste?.(view, event, slice) ?? false
              event.preventDefault()
              const error = insertParsedMarkdown(view, ctx.get(parserCtx), source)
              if (error) {
                view.dispatch(view.state.tr.insertText(source).scrollIntoView())
                view.focus()
                setPasteNotice(`${error} The original clipboard text was pasted unchanged.`)
              } else {
                setPasteNotice(undefined)
              }
              return true
            },
            handleKeyDown: (view, event) => {
              const { $from } = view.state.selection
              if (event.key === 'Enter' && view.state.selection.empty && $from.parent.type.name === 'code_block' && $from.parentOffset === $from.parent.content.size && $from.parent.textContent.endsWith('\n\n')) {
                event.preventDefault()
                view.dispatch(view.state.tr.delete($from.pos - 2, $from.pos))
                exitCode(view.state, view.dispatch)
                view.focus()
                return true
              }
              if (event.key === 'Enter' && $from.parent.type.name === 'code_block') {
                event.preventDefault()
                newlineInCode(view.state, view.dispatch)
                return true
              }
              const active = slashStateRef.current
              const activeNoteLink = noteLinkTriggerRef.current
              if (activeNoteLink && event.key === 'Escape') {
                event.preventDefault()
                closeNoteLinkTrigger()
                return true
              }
              if (activeNoteLink && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
                event.preventDefault()
                const options = filteredNotePaths(activeNoteLink.query, notePathsRef.current, notePath)
                if (!options.length) return true
                const direction = event.key === 'ArrowDown' ? 1 : -1
                const next = (noteLinkTriggerIndexRef.current + direction + options.length) % options.length
                noteLinkTriggerIndexRef.current = next
                setNoteLinkTriggerIndex(next)
                return true
              }
              if (activeNoteLink && event.key === 'Enter') {
                const options = filteredNotePaths(activeNoteLink.query, notePathsRef.current, notePath)
                if (options.length) {
                  event.preventDefault()
                  insertTriggeredNoteLink(options[Math.min(noteLinkTriggerIndexRef.current, options.length - 1)])
                  return true
                }
              }
              if (activeNoteLink && ['ArrowLeft', 'ArrowRight', 'Home', 'End', 'PageUp', 'PageDown'].includes(event.key)) {
                closeNoteLinkTrigger()
              }
              if (active && event.key === 'Escape') {
                event.preventDefault()
                closeSlashMenu()
                return true
              }
              const inlineCode = view.state.schema.marks.inlineCode
              const inlineCodeActive = inlineCode?.isInSet(view.state.storedMarks ?? $from.marks())
              if (event.key === 'Escape' && (inlineCodeTypingRef.current || inlineCodeActive)) {
                event.preventDefault()
                inlineCodeTypingRef.current = false
                view.dispatch(view.state.tr.removeStoredMark(inlineCode))
                setToolbarState(toolbarStateFromEditor(view.state))
                return true
              }
              if (!active) return previous.handleKeyDown?.(view, event) ?? false
              const options = filteredSlashCommands(active.query)
              if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
                event.preventDefault()
                if (!options.length) return true
                const direction = event.key === 'ArrowDown' ? 1 : -1
                const next = (slashIndexRef.current + direction + options.length) % options.length
                slashIndexRef.current = next
                setSlashIndex(next)
                return true
              }
              if (event.key === 'Enter' && options.length) {
                event.preventDefault()
                executeSlashCommand(options[Math.min(slashIndexRef.current, options.length - 1)].id)
                return true
              }
              return previous.handleKeyDown?.(view, event) ?? false
            },
            handleDOMEvents: {
              ...previous.handleDOMEvents,
              blur: (view, event) => {
                const handled = previous.handleDOMEvents?.blur?.(view, event) ?? false
                globalThis.setTimeout(() => {
                  const activeElement = document.activeElement
                  if (activeElement instanceof Element && activeElement.closest('[data-varianotes-note-link-menu]')) return
                  closeNoteLinkTrigger()
                }, 0)
                return handled
              },
              click: (view, event) => {
                const anchor = (event.target as HTMLElement | null)?.closest<HTMLAnchorElement>('a[href]')
                if (!anchor) return previous.handleDOMEvents?.click?.(view, event) ?? false
                const href = anchor.getAttribute('href') ?? ''
                const targetPath = resolveInternalNotePath(notePath, href)
                if (!targetPath) return previous.handleDOMEvents?.click?.(view, event) ?? false
                event.preventDefault()
                if (!notePathsRef.current.includes(targetPath)) {
                  setLinkError(`Linked note not found: ${targetPath}`)
                  return true
                }
                setLinkError(undefined)
                openNoteLinkRef.current?.(targetPath, event.ctrlKey || event.metaKey ? 'new' : 'current')
                return true
              },
            },
          }))
          ctx.get(listenerCtx).markdownUpdated((_ctx, nextMarkdown, previousMarkdown) => {
            const view = _ctx.get(editorViewCtx)
            if (view?.state) queueOutlineRefresh(view)
            if (nextMarkdown !== previousMarkdown) onChange(nextMarkdown)
          })
          ctx.get(listenerCtx).selectionUpdated((_ctx, selection) => {
            const view = _ctx.get(editorViewCtx)
            if (view?.state) {
              setToolbarState(toolbarStateFromEditor(view.state))
              updateSelectedLink(view.state)
              updateSlashMenu(view)
              updateNoteLinkTrigger(view)
            }
            if (selection instanceof NodeSelection && selection.node.type.name === 'image') {
              setSelectedImage({ position: selection.from, alt: selection.node.attrs.alt ?? '', src: selection.node.attrs.src ?? '' })
            } else {
              setSelectedImage(undefined)
            }
          })
          ctx.get(listenerCtx).updated((ctx) => {
            const view = ctx.get(editorViewCtx)
            if (view?.state) {
              onStats?.(documentStats(view.state.doc))
              queueOutlineRefresh(view)
              setToolbarState(toolbarStateFromEditor(view.state))
              updateSelectedLink(view.state)
              updateSlashMenu(view)
              updateNoteLinkTrigger(view)
            }
          })
          ctx.get(listenerCtx).mounted((ctx) => {
            const view = ctx.get(editorViewCtx)
            if (view?.state) {
              onStats?.(documentStats(view.state.doc))
              queueOutlineRefresh(view)
              setToolbarState(toolbarStateFromEditor(view.state))
              updateSelectedLink(view.state)
              updateNoteLinkTrigger(view)
            }
          })
          ctx.update(uploadConfig.key, (previous) => ({ ...previous, uploader, enableHtmlFileUploader: true }))
          ctx.update(inlineImageConfig.key, (previous) => ({ ...previous, onUpload: uploadImage, proxyDomURL: inlineImageURL }))
        })
        .use(commonmark)
        .use(gfm)
        .use(codeHighlightPlugin)
        .use(codeBlockView)
        .use(history)
        .use(listener)
        .use(upload)
        .use(interactiveListItemView)
        .use(imageInlineComponent),
    [documentKey],
  )

  useEffect(() => {
    if (!outlineOpen || outlineHeadings.length === 0) return
    let frame: number | null = null
    const updateActiveHeading = () => {
      if (frame !== null) cancelFrame(frame)
      frame = requestFrame(() => {
        frame = null
        get()?.action((ctx) => {
          const view = ctx.get(editorViewCtx)
          setActiveOutlinePosition(currentOutlinePosition(view, outlineHeadings))
        })
      })
    }
    window.addEventListener('scroll', updateActiveHeading, true)
    updateActiveHeading()
    return () => {
      window.removeEventListener('scroll', updateActiveHeading, true)
      if (frame !== null) cancelFrame(frame)
    }
  }, [get, outlineHeadings, outlineOpen])

  useEffect(() => {
    if (!outlineOpen) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const headings = documentOutline(view.state.doc)
      setOutlineHeadings((previous) => sameOutline(previous, headings) ? previous : headings)
      setActiveOutlinePosition(currentOutlinePosition(view, headings))
    })
  }, [get, outlineOpen])

  function selectOutlineHeading(heading: OutlineHeading) {
    let editorElement: HTMLElement | undefined
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      editorElement = view.dom
      const position = Math.min(heading.position + 1, view.state.doc.content.size)
      const selection = TextSelection.near(view.state.doc.resolve(position), 1)
      view.dispatch(view.state.tr.setSelection(selection).scrollIntoView())
      const dom = view.nodeDOM(heading.position)
      if (dom instanceof HTMLElement) dom.scrollIntoView({ block: 'start', behavior: 'smooth' })
      view.focus()
      setActiveOutlinePosition(heading.position)
    })
    if (narrowViewport) {
      setOutlineOpen(false)
    }
    requestFrame(() => editorElement?.focus())
  }

  function closeOutline() {
    setOutlineOpen(false)
    requestFrame(() => outlineToggle.current?.focus())
  }

  useEffect(() => {
    const container = editorContainer.current
    if (!container || readOnly) return
    const recordOpening = () => {
      get()?.action((ctx) => {
        const view = ctx.get(editorViewCtx)
        recordRawNoteLinkOpening(view)
      })
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === '[') {
        noteLinkOpeningKeyDownRef.current = true
        recordOpening()
      } else if (['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End', 'PageUp', 'PageDown'].includes(event.key)) {
        noteLinkOpeningRef.current = undefined
        noteLinkOpeningKeyDownRef.current = false
      } else if (!event.ctrlKey && !event.metaKey && !event.altKey && event.key.length === 1) {
        noteLinkOpeningRef.current = undefined
      }
    }
    const handleBeforeInput = (event: InputEvent) => {
      if (event.data === '[') {
        if (!noteLinkOpeningKeyDownRef.current) recordOpening()
        noteLinkOpeningKeyDownRef.current = false
      } else if (event.data) {
        noteLinkOpeningKeyDownRef.current = false
        noteLinkOpeningRef.current = undefined
      }
    }
    container.addEventListener('keydown', handleKeyDown, true)
    container.addEventListener('beforeinput', handleBeforeInput, true)
    return () => {
      container.removeEventListener('keydown', handleKeyDown, true)
      container.removeEventListener('beforeinput', handleBeforeInput, true)
    }
  }, [get, readOnly, recordRawNoteLinkOpening])

  function callCommand<T>(command: { key: unknown }, payload?: T) {
    if (readOnly) return
    get()?.action((ctx) => {
      ctx.get(commandsCtx).call(command.key as never, payload as never)
      setToolbarState(toolbarStateFromEditor(ctx.get(editorViewCtx).state))
    })
  }

  async function openMarkdownPaste() {
    if (readOnly) return
    setMarkdownPaste({ text: '', clipboardHint: 'Paste Markdown into this field, then insert it.' })
    if (!navigator.clipboard?.readText) return
    try {
      const text = await navigator.clipboard.readText()
      setMarkdownPaste((current) => current && current.text === ''
        ? { ...current, text, clipboardHint: text ? 'Markdown was read from the clipboard.' : 'The clipboard contains no text. Paste Markdown into this field.' }
        : current)
    } catch {
      setMarkdownPaste((current) => current && current.text === ''
        ? { ...current, clipboardHint: 'Direct clipboard access is unavailable here. Paste Markdown into this field instead.' }
        : current)
    }
  }

  function applyMarkdownPaste(asPlainText = false) {
    if (readOnly || !markdownPaste) return
    let error = 'The editor is not ready yet. Your Markdown remains in this dialog.'
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      if (asPlainText) {
        view.dispatch(view.state.tr.insertText(markdownPaste.text).scrollIntoView())
        view.focus()
        error = ''
        return
      }
      error = insertParsedMarkdown(view, ctx.get(parserCtx), markdownPaste.text) ?? ''
    })
    if (error) {
      setMarkdownPaste((current) => current ? { ...current, error } : current)
      return
    }
    setMarkdownPaste(undefined)
    setPasteNotice(undefined)
  }

  function closeSlashMenu() {
    slashStateRef.current = undefined
    slashIndexRef.current = 0
    setSlashState(undefined)
    setSlashIndex(0)
  }

  function closeNoteLinkTrigger() {
    noteLinkTriggerIntentRef.current = undefined
    noteLinkOpeningRef.current = undefined
    noteLinkOpeningKeyDownRef.current = false
    noteLinkTriggerRef.current = undefined
    noteLinkTriggerIndexRef.current = 0
    setNoteLinkTrigger(undefined)
    setNoteLinkTriggerIndex(0)
  }

  function updateNoteLinkTrigger(view:EditorView) {
    if (readOnly || !view.state.selection.empty) {
      if (noteLinkTriggerRef.current || noteLinkTriggerIntentRef.current) closeNoteLinkTrigger()
      return
    }
    const { $from } = view.state.selection
    if (!$from.parent.isTextblock || $from.parent.type.name === 'code_block') {
      if (noteLinkTriggerRef.current || noteLinkTriggerIntentRef.current) closeNoteLinkTrigger()
      return
    }
    const beforeCursor = $from.parent.textBetween(0, $from.parentOffset, undefined, '\ufffc')
    const match = beforeCursor.match(/\[\[?([^\]\n]*)$/)
    const intent = noteLinkTriggerIntentRef.current
    if (!match) {
      if (noteLinkTriggerRef.current || intent) closeNoteLinkTrigger()
      return
    }
    const query = match[1]
    const from = $from.pos - match[0].length
    if (!intent || intent.from !== from || intent.blockContext !== noteLinkBlockContext($from)) {
      if (noteLinkTriggerRef.current || intent) closeNoteLinkTrigger()
      return
    }
    const coordinates = view.coordsAtPos($from.pos)
    const next = { from, to:$from.pos, query, left:Math.max(8,Math.min(coordinates.left,window.innerWidth-320)), top:Math.max(8,Math.min(coordinates.bottom+6,window.innerHeight-320)), documentKey }
    noteLinkTriggerRef.current = next
    noteLinkTriggerIndexRef.current = 0
    setNoteLinkTrigger(next)
    setNoteLinkTriggerIndex(0)
  }

  function insertTriggeredNoteLink(targetPath:string) {
    const active = noteLinkTriggerRef.current
    if (!active || readOnly) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const link = linkSchema.type(ctx)
      const label = targetPath.split('/').pop()?.replace(/\.md$/i,'') || 'Note'
      const href = portableRelativeNoteHref(notePath,targetPath)
      const end = active.from + label.length
      let transaction = view.state.tr.delete(active.from,active.to).insertText(label,active.from).addMark(active.from,end,link.create({ href })).removeStoredMark(link)
      transaction = transaction.setSelection(TextSelection.create(transaction.doc,end))
      view.dispatch(transaction)
      view.focus()
    })
    closeNoteLinkTrigger()
  }

  function updateSlashMenu(view: EditorView) {
    if (readOnly || !view.state.selection.empty) {
      if (slashStateRef.current) closeSlashMenu()
      return
    }
    const { $from } = view.state.selection
    if (!$from.parent.isTextblock || $from.parent.type.name === 'code_block') {
      if (slashStateRef.current) closeSlashMenu()
      return
    }
    const beforeCursor = $from.parent.textBetween(0, $from.parentOffset, undefined, '\ufffc')
    const match = beforeCursor.match(/(^|\s)\/([a-z0-9-]*)$/i)
    if (!match) {
      if (slashStateRef.current) closeSlashMenu()
      return
    }
    const query = match[2]
    const from = $from.pos - query.length - 1
    const coordinates = view.coordsAtPos($from.pos)
    const next = { from, to: $from.pos, query, left: Math.max(8, Math.min(coordinates.left, window.innerWidth - 288)), top: Math.max(8, Math.min(coordinates.bottom + 6, window.innerHeight - 320)) }
    slashStateRef.current = next
    slashIndexRef.current = 0
    setSlashState(next)
    setSlashIndex(0)
  }

  function executeSlashCommand(id: SlashCommandID) {
    const active = slashStateRef.current
    if (!active || readOnly) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      view.dispatch(view.state.tr.delete(active.from, active.to))
      view.focus()
    })
    closeSlashMenu()
    if (id === 'paragraph') setBlock('paragraph')
    else if (id.startsWith('heading-')) setBlock(id)
    else if (id === 'bullet-list') callCommand(wrapInBulletListCommand)
    else if (id === 'numbered-list') callCommand(wrapInOrderedListCommand)
    else if (id === 'task-list') toggleTaskList()
    else if (id === 'blockquote') callCommand(wrapInBlockquoteCommand)
    else if (id === 'code-block') callCommand(createCodeBlockCommand)
    else if (id === 'inline-code') toggleInlineCode()
    else if (id === 'link') editLink()
    else if (id === 'image') input.current?.click()
    else if (id === 'table') setTablePickerOpen(true)
    else if (id === 'horizontal-rule') callCommand(insertHrCommand)
  }

  function callProseCommand(command: Command) {
    if (readOnly) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      command(view.state, view.dispatch, view)
      view.focus()
      setToolbarState(toolbarStateFromEditor(view.state))
    })
  }

  function toggleInlineCode() {
    if (readOnly) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const mark = view.state.schema.marks.inlineCode
      if (!mark) return
      if (!view.state.selection.empty) {
        inlineCodeTypingRef.current = false
        ctx.get(commandsCtx).call(toggleInlineCodeCommand.key)
      } else {
        const active = inlineCodeTypingRef.current
          || Boolean(mark.isInSet(view.state.storedMarks ?? view.state.selection.$from.marks()))
        inlineCodeTypingRef.current = !active
        const transaction = active ? view.state.tr.removeStoredMark(mark) : view.state.tr.addStoredMark(mark.create())
        view.dispatch(transaction)
      }
      view.focus()
      setToolbarState(toolbarStateFromEditor(view.state))
    })
  }

  function insertTable(size: TableSize) {
    callCommand(insertTableCommand, { row: size.rows, col: size.columns })
    setTablePickerOpen(false)
  }

  function syncToolbarAfterPointer() {
    globalThis.setTimeout(() => {
      get()?.action((ctx) => {
        const view = ctx.get(editorViewCtx)
        if (view?.state) setToolbarState(toolbarStateFromEditor(view.state))
      })
    }, 0)
  }

  function selectImageFromPointer(target: HTMLElement) {
    const imageView = target.closest<HTMLElement>('.milkdown-image-inline')
    if (!imageView) return false
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const position = view.posAtDOM(imageView, 0)
      const node = view.state.doc.nodeAt(position)
      if (!node || node.type.name !== 'image') return
      view.dispatch(view.state.tr.setSelection(NodeSelection.create(view.state.doc, position)))
      if (!readOnly) view.focus()
    })
    return true
  }

  function openReadOnlyImage(target: HTMLElement) {
    if (!readOnly) return false
    const image = target.closest<HTMLImageElement>('.varianotes-editor .milkdown-image-inline img')
    if (!image) return false
    openImageViewer({ src: image.currentSrc || image.src, alt: image.alt }, image)
    return true
  }

  function setBlock(block: string) {
    if (block === 'paragraph') callCommand(turnIntoTextCommand)
    else if (block === 'code-block') callCommand(createCodeBlockCommand)
    else callCommand(wrapInHeadingCommand, Number(block.replace('heading-', '')))
  }

  function editLink() {
    if (readOnly) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const { from, to, empty, $from } = view.state.selection
      const link = linkSchema.type(ctx)
      const existing = link.isInSet(view.state.storedMarks ?? $from.marks())
      setLinkPicker({ from, to, selectedText: empty ? '' : view.state.doc.textBetween(from, to, ' '), existingHref: existing ? String(existing.attrs.href ?? '') : undefined })
    })
  }

  function applyLink(href: string, label?: string) {
    const draft = linkPicker
    if (!draft || readOnly) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const link = linkSchema.type(ctx)
      const normalizedHref = href.trim()
      if (draft.existingHref !== undefined) {
        if (normalizedHref) ctx.get(commandsCtx).call(updateLinkCommand.key, { href: normalizedHref })
        else ctx.get(commandsCtx).call(toggleLinkCommand.key)
      } else if (draft.from !== draft.to) {
        const transaction = normalizedHref
          ? view.state.tr.addMark(draft.from, draft.to, link.create({ href: normalizedHref }))
          : view.state.tr.removeMark(draft.from, draft.to, link)
        view.dispatch(transaction)
      } else if (normalizedHref) {
        const text = (label || 'Link').trim()
        const end = draft.from + text.length
        let transaction = view.state.tr.insertText(text, draft.from, draft.to).addMark(draft.from, end, link.create({ href: normalizedHref })).removeStoredMark(link)
        transaction = transaction.setSelection(TextSelection.create(transaction.doc, end))
        view.dispatch(transaction)
      }
      view.focus()
      setToolbarState(toolbarStateFromEditor(view.state))
      updateSelectedLink(view.state)
    })
    setLinkPicker(undefined)
  }

  function openSelectedInternalLink(disposition: 'current' | 'new') {
    if (!selectedLink?.targetPath) return
    if (!selectedLink.exists) {
      setLinkError(`Linked note not found: ${selectedLink.targetPath}`)
      return
    }
    setLinkError(undefined)
    openNoteLinkRef.current?.(selectedLink.targetPath, disposition)
  }

  function toggleTaskList() {
    if (readOnly) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      let state = view.state
      let itemDepth = -1
      for (let depth = state.selection.$from.depth; depth > 0; depth -= 1) {
        if (state.selection.$from.node(depth).type.name === 'list_item') { itemDepth = depth; break }
      }
      if (itemDepth < 0) {
        ctx.get(commandsCtx).call(wrapInBulletListCommand.key)
        state = view.state
        for (let depth = state.selection.$from.depth; depth > 0; depth -= 1) {
          if (state.selection.$from.node(depth).type.name === 'list_item') { itemDepth = depth; break }
        }
      }
      if (itemDepth < 0) return
      const item = state.selection.$from.node(itemDepth)
      const position = state.selection.$from.before(itemDepth)
      view.dispatch(state.tr.setNodeMarkup(position, undefined, { ...item.attrs, checked: item.attrs.checked === null ? false : null }))
    })
  }

  async function insertSelectedImages(files: FileList | null) {
    if (!files?.length) return
    try {
      const uploadedImages = await Promise.all(Array.from(files).filter((file) => file.type.startsWith('image/')).map(uploadImage))
      if (uploadedImages.length) {
        get()?.action((ctx) => {
          const view = ctx.get(editorViewCtx)
          const schema = ctx.get(schemaCtx)
          let transaction = view.state.tr
          for (const [index, path] of uploadedImages.entries()) {
            const node = schema.nodes.image.create({ src: path, alt: '' })
            transaction = transaction.replaceSelectionWith(node)
            if (index < uploadedImages.length - 1) transaction = transaction.insertText(' ')
          }
          view.dispatch(transaction.scrollIntoView())
        })
      }
    } catch {
      // uploadImage already exposes the actionable error in the editor UI.
    } finally {
      if (input.current) input.current.value = ''
    }
  }

  async function replaceSelectedImage(files: FileList | null) {
    const file = Array.from(files ?? []).find((candidate) => candidate.type.startsWith('image/'))
    if (!file || readOnly || !selectedImage) return
    const selectedPosition = selectedImage.position
    const previousImage = selectedImage.src
    const presentationSize = activeImagePresentations[previousImage] ?? 'full'
    try {
      const path = await uploadImage(file)
      get()?.action((ctx) => {
        const view = ctx.get(editorViewCtx)
        const node = view.state.doc.nodeAt(selectedPosition)
        if (!node || node.type.name !== 'image') {
          setUploadError('The selected image changed before replacement finished')
          return
        }
        view.dispatch(view.state.tr.setNodeMarkup(selectedPosition, undefined, { ...node.attrs, src: path }).scrollIntoView())
        setSelectedImage({ position: selectedPosition, alt: node.attrs.alt ?? '', src: path })
      })
      void saveImagePresentation(path, presentationSize, previousImage)
    } catch {
      // uploadImage already exposes the actionable error in the editor UI.
    } finally {
      if (replacementInput.current) replacementInput.current.value = ''
    }
  }

  function removeSelectedImage() {
    if (readOnly || !selectedImage) return
    const removedImage = selectedImage.src
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const node = view.state.doc.nodeAt(selectedImage.position)
      if (!node || node.type.name !== 'image') return
      view.dispatch(view.state.tr.delete(selectedImage.position, selectedImage.position + node.nodeSize))
      setSelectedImage(undefined)
    })
    void removeImagePresentation(removedImage)
  }

  function saveImageMetadata() {
    if (readOnly || !selectedImage || editingAlt === undefined) return
    get()?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const node = view.state.doc.nodeAt(selectedImage.position)
      if (!node || node.type.name !== 'image') return
      view.dispatch(view.state.tr.setNodeAttribute(selectedImage.position, 'alt', editingAlt).scrollIntoView())
      setSelectedImage({ position: selectedImage.position, alt: editingAlt, src: selectedImage.src })
      setEditingAlt(undefined)
    })
  }

  return (
    <div className="varianotes-editor-layout">
      <div className="varianotes-editor-main">
      {(!readOnly || selectedLink?.targetPath) && <div aria-label="Editor toolbars" data-sticky={stickyToolbar ? 'true' : 'false'} className="varianotes-editor-toolbars mb-3 space-y-1.5">
      {!readOnly && <div role="toolbar" aria-label="Editor formatting" className="flex max-w-full flex-wrap items-center gap-0.5 border-b border-zinc-800/80 pb-1.5">
        <ToolbarButton label="Undo" disabled={false} onClick={() => callCommand(undoCommand)}><EditorIcon name="undo" /></ToolbarButton>
        <ToolbarButton label="Redo" disabled={false} onClick={() => callCommand(redoCommand)}><EditorIcon name="redo" /></ToolbarButton>
        <ToolbarDivider />
        <select aria-label="Block type" value={toolbarState.block} onChange={(event) => setBlock(event.target.value)} className="h-8 max-w-32 shrink-0 rounded border border-zinc-700 bg-zinc-950 px-2 text-xs text-zinc-200">
          <option value="paragraph">Paragraph</option>
          {[1, 2, 3, 4, 5, 6].map((level) => <option key={level} value={`heading-${level}`}>Heading {level}</option>)}
          <option hidden value="code-block">Code block</option>
        </select>
        <ToolbarDivider />
        <ToolbarButton label="Bold" active={toolbarState.strong} disabled={false} onClick={() => callCommand(toggleStrongCommand)}><EditorIcon name="bold" /></ToolbarButton>
        <ToolbarButton label="Italic" active={toolbarState.emphasis} disabled={false} onClick={() => callCommand(toggleEmphasisCommand)}><EditorIcon name="italic" /></ToolbarButton>
        <ToolbarButton label="Strikethrough" active={toolbarState.strike} disabled={false} onClick={() => callCommand(toggleStrikethroughCommand)}><EditorIcon name="strikethrough" /></ToolbarButton>
        <ToolbarButton label="Inline code" active={toolbarState.code} disabled={false} onClick={toggleInlineCode}><EditorIcon name="code" /></ToolbarButton>
        <ToolbarDivider />
        <ToolbarButton label="Bullet list" active={toolbarState.bullet && !toolbarState.task} disabled={false} onClick={() => callCommand(toolbarState.bullet ? liftListItemCommand : wrapInBulletListCommand)}><EditorIcon name="bullet-list" /></ToolbarButton>
        <ToolbarButton label="Numbered list" active={toolbarState.ordered} disabled={false} onClick={() => callCommand(toolbarState.ordered ? liftListItemCommand : wrapInOrderedListCommand)}><EditorIcon name="numbered-list" /></ToolbarButton>
        <ToolbarButton label="Task list" active={toolbarState.task} disabled={false} onClick={toggleTaskList}><EditorIcon name="task-list" /></ToolbarButton>
        <ToolbarButton label="Blockquote" active={toolbarState.quote} disabled={false} onClick={() => toolbarState.quote ? callProseCommand(lift) : callCommand(wrapInBlockquoteCommand)}><EditorIcon name="quote" /></ToolbarButton>
        <ToolbarButton label="Code block" active={toolbarState.block === 'code-block'} disabled={false} onClick={() => callCommand(toolbarState.block === 'code-block' ? turnIntoTextCommand : createCodeBlockCommand)}><EditorIcon name="code-block" /></ToolbarButton>
        <ToolbarDivider />
        <ToolbarButton label="Link" active={toolbarState.link} disabled={false} onClick={editLink}><EditorIcon name="link" /></ToolbarButton>
        <div className="relative shrink-0">
          <button ref={insertMenuButton} type="button" aria-label="Insert" aria-controls="editor-insert-menu" aria-expanded={insertMenuOpen} onMouseDown={(event) => event.preventDefault()} onClick={() => setInsertMenuOpen((open) => !open)} className={`inline-flex h-9 items-center gap-1 rounded-md px-2 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500 ${insertMenuOpen ? 'bg-amber-400/15 text-amber-200 ring-1 ring-inset ring-amber-500/70' : 'border border-zinc-700 bg-zinc-950/40 text-zinc-300 hover:bg-zinc-800 hover:text-white'}`}><EditorIcon name="plus" /><span>Insert</span><EditorIcon name="chevron-down" /></button>
          <div ref={insertMenuPanel} id="editor-insert-menu" role="group" aria-label="Insert actions" hidden={!insertMenuOpen} className="absolute right-0 top-full z-30 mt-2 grid w-56 max-w-[calc(100vw-2rem)] gap-0.5 rounded-lg border border-zinc-700 bg-zinc-900 p-1.5 shadow-xl shadow-black/30">
            <button type="button" aria-label="Paste as Markdown" title="Paste as Markdown" onMouseDown={(event) => event.preventDefault()} onClick={() => { setInsertMenuOpen(false); void openMarkdownPaste() }} className="flex min-h-10 items-center gap-2.5 rounded-md px-2.5 text-left text-sm text-zinc-200 transition-colors hover:bg-zinc-800"><EditorIcon name="markdown" /><span>Paste as Markdown</span></button>
            <button type="button" aria-label="Insert image" title="Insert image" disabled={uploadState === 'uploading'} onMouseDown={(event) => event.preventDefault()} onClick={() => { setInsertMenuOpen(false); input.current?.click() }} className="flex min-h-10 items-center gap-2.5 rounded-md px-2.5 text-left text-sm text-zinc-200 transition-colors hover:bg-zinc-800 disabled:opacity-40"><EditorIcon name="image" /><span>{uploadState === 'uploading' ? 'Uploading image…' : 'Image'}</span></button>
            <button type="button" aria-label="Insert table" title="Insert table" onMouseDown={(event) => event.preventDefault()} onClick={() => { setInsertMenuOpen(false); setTablePickerOpen(true) }} className="flex min-h-10 items-center gap-2.5 rounded-md px-2.5 text-left text-sm text-zinc-200 transition-colors hover:bg-zinc-800"><EditorIcon name="table" /><span>Table</span></button>
            <span role="separator" className="my-1 h-px bg-zinc-700" />
            <button type="button" aria-label="Horizontal rule" title="Horizontal rule" onMouseDown={(event) => event.preventDefault()} onClick={() => { setInsertMenuOpen(false); callCommand(insertHrCommand) }} className="flex min-h-10 items-center gap-2.5 rounded-md px-2.5 text-left text-sm text-zinc-200 transition-colors hover:bg-zinc-800"><EditorIcon name="horizontal-rule" /><span>Horizontal rule</span></button>
          </div>
          <input ref={input} type="file" accept="image/png,image/jpeg,image/gif,image/webp" multiple className="sr-only" onChange={(event) => { void insertSelectedImages(event.target.files) }} />
        </div>
      </div>}

      {toolbarState.table && !readOnly && (
        <div role="toolbar" aria-label="Table editing" className="flex max-w-full flex-wrap items-center gap-1 border-b border-zinc-800/60 pb-1.5">
          <span className="shrink-0 px-1 text-xs font-medium text-zinc-400">Table</span>
          <ToolbarDivider />
          <ToolbarButton label="Add row above" disabled={false} onClick={() => callCommand(addRowBeforeCommand)}>↑ Row</ToolbarButton>
          <ToolbarButton label="Add row below" disabled={false} onClick={() => callCommand(addRowAfterCommand)}>↓ Row</ToolbarButton>
          <ToolbarButton label="Delete current row" disabled={false} onClick={() => callProseCommand(deleteRow)}>Delete row</ToolbarButton>
          <ToolbarDivider />
          <ToolbarButton label="Add column left" disabled={false} onClick={() => callCommand(addColBeforeCommand)}>← Column</ToolbarButton>
          <ToolbarButton label="Add column right" disabled={false} onClick={() => callCommand(addColAfterCommand)}>→ Column</ToolbarButton>
          <ToolbarButton label="Delete current column" disabled={false} onClick={() => callProseCommand(deleteColumn)}>Delete column</ToolbarButton>
          <ToolbarDivider />
          <ToolbarButton label="Delete table" disabled={false} onClick={() => callProseCommand(deleteTable)}>Delete table</ToolbarButton>
        </div>
      )}

      {selectedLink?.targetPath && (
        <div role="toolbar" aria-label="Internal note link" className="flex max-w-full flex-wrap items-center gap-1 border-b border-zinc-800/60 pb-1.5">
          <span className="shrink-0 px-1 text-xs font-medium text-zinc-400">Note link</span>
          <span className={`shrink-0 px-1 text-xs ${selectedLink.exists ? 'text-zinc-500' : 'text-red-300'}`}>{selectedLink.exists ? selectedLink.targetPath : `Missing: ${selectedLink.targetPath}`}</span>
          <ToolbarDivider />
          <ToolbarButton label="Open linked note" disabled={!selectedLink.exists} onClick={() => openSelectedInternalLink('current')}>Open</ToolbarButton>
          <ToolbarButton label="Open linked note in new tab" disabled={!selectedLink.exists} onClick={() => openSelectedInternalLink('new')}>Open in new tab</ToolbarButton>
          {!readOnly && <ToolbarButton label="Edit link" disabled={false} onClick={editLink}>Edit link</ToolbarButton>}
        </div>
      )}

      {selectedImage && !readOnly && (
        <div role="toolbar" aria-label="Image editing" className="flex max-w-full flex-wrap items-center gap-1 border-b border-zinc-800/60 pb-1.5">
          <span className="shrink-0 px-1 text-xs font-medium text-zinc-400">Image</span>
          <ToolbarDivider />
          {!isExternalImageSource(selectedImage.src) && <><span className="shrink-0 px-1 text-xs text-zinc-500">Image size</span>
            <div role="group" aria-label="Image presentation size" className="flex shrink-0 gap-1">{imagePresentationSizes.map((size) => <button key={size} type="button" aria-label={`${size[0].toUpperCase()+size.slice(1)} image size`} aria-pressed={(activeImagePresentations[selectedImage.src] ?? 'full') === size} onClick={() => void saveImagePresentation(selectedImage.src, size)} className={`min-h-10 rounded px-2 text-xs font-medium ${(activeImagePresentations[selectedImage.src] ?? 'full') === size ? 'border border-amber-500 bg-amber-400/15 text-amber-100' : 'border border-zinc-700 text-zinc-300 hover:bg-zinc-800'}`}>{size[0].toUpperCase()+size.slice(1)}</button>)}</div>
            <ToolbarDivider /></>}
          <button type="button" title="View image" aria-label="View image" onClick={(event) => openImageViewer({ src: displayURL(selectedImage.src), alt: selectedImage.alt }, event.currentTarget)} className="h-8 shrink-0 rounded border border-transparent px-2 text-xs font-medium text-zinc-300 hover:bg-zinc-800 hover:text-white">View image</button>
          <ToolbarButton label="Alt text" disabled={false} onClick={() => setEditingAlt(selectedImage.alt)}>Alt text</ToolbarButton>
          <ToolbarButton label="Replace image" disabled={uploadState === 'uploading'} onClick={() => replacementInput.current?.click()}>{uploadState === 'uploading' ? 'Replacing…' : 'Replace image'}</ToolbarButton>
          <ToolbarButton label="Remove image" disabled={false} onClick={removeSelectedImage}>Remove image</ToolbarButton>
          <input ref={replacementInput} type="file" accept="image/png,image/jpeg,image/gif,image/webp" className="sr-only" onChange={(event) => { void replaceSelectedImage(event.target.files) }} />
        </div>
      )}
      </div>
      }

      {readOnly && <p className="mb-3 text-xs text-zinc-500">Read only: select and copy without changing the note.</p>}
      {uploadError && <p className="mb-4 rounded-lg border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{uploadError}</p>}
      {presentationError?.contextKey === imageContextKey && <p role="alert" className="mb-4 rounded-lg border border-amber-900/70 bg-amber-950/30 p-3 text-sm text-amber-100">Image layout settings are unavailable. The note and image are unchanged. {presentationError.message}</p>}
      {linkError && <p role="alert" className="mb-4 rounded-lg border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{linkError}</p>}
      {pasteNotice && <div role="status" className="mb-4 flex items-start justify-between gap-3 rounded-lg border border-amber-900/70 bg-amber-950/30 p-3 text-sm text-amber-100"><span>{pasteNotice}</span><button type="button" onClick={() => setPasteNotice(undefined)} className="shrink-0 rounded px-2 py-1 text-xs text-amber-100 hover:bg-amber-900/50" aria-label="Dismiss paste message">Dismiss</button></div>}
      <div
        ref={editorContainer}
        onPointerDownCapture={(event) => {
          if (noteLinkTriggerRef.current || noteLinkOpeningRef.current) closeNoteLinkTrigger()
          const target = event.target as HTMLElement
          if (selectImageFromPointer(target)) return
          if (target.closest('table')) {
            setToolbarState((previous) => ({ ...previous, table: true }))
          }
        }}
        onClickCapture={(event) => { openReadOnlyImage(event.target as HTMLElement) }}
        onKeyDownCapture={(event) => {
          if (readOnly && (event.key === 'Enter' || event.key === ' ') && (event.target as HTMLElement).closest('.varianotes-editor .milkdown-image-inline img')) {
            event.preventDefault()
            openReadOnlyImage(event.target as HTMLElement)
          }
        }}
        onPointerUp={syncToolbarAfterPointer}
      >
        <Milkdown />
      </div>
      </div>

      {slashState && !readOnly && <SlashCommandMenu commands={filteredSlashCommands(slashState.query)} selectedIndex={slashIndex} left={slashState.left} top={slashState.top} onSelect={executeSlashCommand} />}
      {noteLinkTrigger?.documentKey === documentKey && !readOnly && <NoteLinkTriggerMenu paths={filteredNotePaths(noteLinkTrigger.query,notePaths,notePath)} selectedIndex={noteLinkTriggerIndex} left={noteLinkTrigger.left} top={noteLinkTrigger.top} onSelect={insertTriggeredNoteLink} />}

      {tablePickerOpen && !readOnly && <TablePicker size={tableSize} onPreview={setTableSize} onSelect={insertTable} onClose={() => setTablePickerOpen(false)} />}
      {linkPicker && !readOnly && <LinkPicker notePath={notePath} notePaths={notePaths} draft={linkPicker} onApply={applyLink} onClose={() => setLinkPicker(undefined)} />}
      {markdownPaste && !readOnly && <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) setMarkdownPaste(undefined) }}><form onSubmit={(event) => { event.preventDefault(); applyMarkdownPaste() }} role="dialog" aria-modal="true" aria-labelledby="markdown-paste-title" className="w-full max-w-2xl rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h2 id="markdown-paste-title" className="text-lg font-semibold text-zinc-100">Paste as Markdown</h2><p className="mt-1 text-sm text-zinc-400">Headings, lists, tasks, quotes, code, links, tables, and dividers become editable note content.</p><label className="mt-4 block text-sm text-zinc-300">Markdown<textarea autoFocus rows={12} value={markdownPaste.text} onChange={(event) => setMarkdownPaste({ text: event.target.value, clipboardHint: markdownPaste.clipboardHint })} className="mt-2 w-full resize-y rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-sm text-zinc-100 outline-none focus:border-amber-500" placeholder="# Heading&#10;&#10;- First item&#10;- Second item" /></label><p className="mt-2 text-xs text-zinc-500">{markdownPaste.clipboardHint} External images stay blocked until you load them. Raw HTML is not inserted.</p>{markdownPaste.error && <p role="alert" className="mt-3 rounded-md border border-red-900/70 bg-red-950/30 p-3 text-sm text-red-200">{markdownPaste.error}</p>}<div className="mt-5 flex flex-wrap justify-end gap-2"><button type="button" onClick={() => setMarkdownPaste(undefined)} className="min-h-10 rounded-md border border-zinc-700 px-4 py-2 text-sm text-zinc-300 hover:bg-zinc-800">Cancel</button><button type="button" onClick={() => applyMarkdownPaste(true)} disabled={!markdownPaste.text} className="min-h-10 rounded-md border border-zinc-700 px-4 py-2 text-sm text-zinc-200 hover:bg-zinc-800 disabled:cursor-not-allowed disabled:opacity-40">Insert as plain text</button><button type="submit" disabled={!markdownPaste.text} className="min-h-10 rounded-md bg-amber-500 px-4 py-2 text-sm font-medium text-zinc-950 hover:bg-amber-400 disabled:cursor-not-allowed disabled:opacity-40">Insert Markdown</button></div></form></div>}
      {editingAlt !== undefined && !readOnly && <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) setEditingAlt(undefined) }}><form onSubmit={(event) => { event.preventDefault(); saveImageMetadata() }} role="dialog" aria-modal="true" aria-labelledby="image-metadata-title" className="w-full max-w-md rounded-xl border border-zinc-700 bg-zinc-900 p-5 shadow-2xl"><h2 id="image-metadata-title" className="text-lg font-semibold text-zinc-100">Edit image</h2><label className="mt-4 block text-sm text-zinc-300">Alt text<input autoFocus value={editingAlt} onChange={(event) => setEditingAlt(event.target.value)} className="mt-2 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 outline-none focus:border-amber-500" placeholder="Leave empty for a decorative image" /></label><p className="mt-2 text-xs text-zinc-500">Describe meaningful content briefly, or leave this empty for a decorative image.</p><div className="mt-5 flex justify-end gap-2"><button type="button" onClick={() => setEditingAlt(undefined)} className="rounded-md border border-zinc-700 px-4 py-2 text-sm text-zinc-300 hover:bg-zinc-800">Cancel</button><button type="submit" className="rounded-md bg-amber-500 px-4 py-2 text-sm font-medium text-zinc-950 hover:bg-amber-400">Save</button></div></form></div>}
      <div ref={outlineDock} className="varianotes-outline-dock" data-positioned="false">
        <div className="varianotes-outline-rail">
          <button ref={outlineToggle} type="button" title="Document outline" aria-label="Outline / Table of contents" aria-pressed={outlineOpen} aria-expanded={outlineOpen} aria-controls={outlineOpen ? 'varianotes-document-outline' : undefined} onMouseDown={(event) => event.preventDefault()} onClick={() => setOutlineOpen((open) => !open)} className="varianotes-outline-toggle"><span aria-hidden="true">☷</span></button>
        </div>
        {outlineOpen && <aside id="varianotes-document-outline" aria-label="Document outline" aria-labelledby="varianotes-document-outline-title" className="varianotes-outline-panel">
          <header className="flex min-h-10 items-center justify-between gap-3 border-b border-zinc-800 px-2 py-2">
            <h2 id="varianotes-document-outline-title" className="text-sm font-semibold text-zinc-200">Outline</h2>
            <button type="button" aria-label="Close document outline" onClick={closeOutline} className="min-h-8 min-w-8 rounded px-2 text-lg leading-none text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200 focus-visible:outline-2 focus-visible:outline-amber-500">×</button>
          </header>
          <nav aria-label="Table of contents" className="varianotes-outline-nav">
            {outlineHeadings.length === 0
              ? <p className="px-2 py-3 text-sm text-zinc-500">This note has no headings yet.</p>
              : <ol className="space-y-0.5 py-2">{outlineHeadings.map((heading) => <li key={heading.position}>
                <button type="button" aria-label={`Heading ${heading.level}: ${heading.text || 'Untitled heading'}`} aria-current={activeOutlinePosition === heading.position ? 'location' : undefined} onClick={() => selectOutlineHeading(heading)} style={{ '--outline-level': Math.max(0, heading.level - 1) } as CSSProperties} className="varianotes-outline-entry">
                  <span className="min-w-0 flex-1 truncate">{heading.text || 'Untitled heading'}</span>
                </button>
              </li>)}</ol>}
          </nav>
        </aside>}
      </div>
      {activeViewedImage && <ImageViewer image={activeViewedImage} onClose={closeImageViewer} />}
    </div>
  )
}

function ImageViewer({ image, onClose }: { image: ViewedImage; onClose: () => void }) {
  const dialog = useRef<HTMLDivElement>(null)
  const closeButton = useRef<HTMLButtonElement>(null)
  const [sizeMode, setSizeMode] = useState<'fit' | 'actual'>('fit')
  const [loadFailed, setLoadFailed] = useState(false)
  const accessibleDescription = image.alt.trim() || 'Note image'

  useEffect(() => {
    closeButton.current?.focus()
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
        return
      }
      if (event.key !== 'Tab' || !dialog.current) return
      const focusable = Array.from(dialog.current.querySelectorAll<HTMLElement>('button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'))
      if (!focusable.length) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-[70] flex min-h-0 flex-col bg-black/90 p-2 sm:p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <div ref={dialog} role="dialog" aria-modal="true" aria-labelledby="image-viewer-title" aria-describedby="image-viewer-status" className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border border-zinc-700 bg-zinc-950 shadow-2xl">
        <header className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-zinc-800 px-3 py-2 sm:px-4">
          <div className="min-w-0"><h2 id="image-viewer-title" className="truncate text-sm font-semibold text-zinc-100">{image.alt.trim() || 'Note image'}</h2><p id="image-viewer-status" className="text-xs text-zinc-500">{loadFailed ? 'Image could not be loaded' : sizeMode === 'fit' ? 'Fit to screen' : 'Actual size (100%)'}</p></div>
          <div role="toolbar" aria-label="Image viewing controls" className="flex flex-wrap items-center justify-end gap-1">
            <button type="button" aria-pressed={sizeMode === 'fit'} onClick={() => setSizeMode('fit')} className={`min-h-11 rounded-md px-3 text-xs font-medium ${sizeMode === 'fit' ? 'border border-amber-500 bg-amber-400/15 text-amber-100' : 'border border-zinc-700 text-zinc-300 hover:bg-zinc-800'}`}>Fit to screen</button>
            <button type="button" aria-pressed={sizeMode === 'actual'} onClick={() => setSizeMode('actual')} className={`min-h-11 rounded-md px-3 text-xs font-medium ${sizeMode === 'actual' ? 'border border-amber-500 bg-amber-400/15 text-amber-100' : 'border border-zinc-700 text-zinc-300 hover:bg-zinc-800'}`}>Actual size</button>
            <button ref={closeButton} type="button" aria-label="Close image viewer" onClick={onClose} className="min-h-11 min-w-11 rounded-md border border-zinc-700 px-3 text-xl leading-none text-zinc-300 hover:bg-zinc-800">×</button>
          </div>
        </header>
        <div data-size-mode={sizeMode} className={`min-h-0 flex-1 overflow-auto p-3 sm:p-6 ${sizeMode === 'fit' ? 'flex items-center justify-center' : 'block'}`}>
          {loadFailed ? <div role="alert" className="m-auto max-w-md rounded-lg border border-red-900/70 bg-red-950/30 p-4 text-center text-sm text-red-100"><p className="font-medium">This image could not be loaded.</p><p className="mt-2 text-xs leading-5 text-red-200/80">The asset may have been moved or removed outside VariaNotes. Your note and its Markdown were not changed.</p></div> : <img src={image.src} alt={accessibleDescription} draggable={false} onError={() => setLoadFailed(true)} className={sizeMode === 'fit' ? 'h-auto max-h-full w-auto max-w-full rounded-md object-contain' : 'h-auto max-h-none w-auto max-w-none rounded-md'} />}
        </div>
      </div>
    </div>
  )
}

function SlashCommandMenu({ commands, selectedIndex, left, top, onSelect }: { commands: SlashCommand[]; selectedIndex: number; left: number; top: number; onSelect: (id: SlashCommandID) => void }) {
  const selected = commands[Math.min(selectedIndex, Math.max(0, commands.length - 1))]
  return <div role="listbox" aria-label="Slash commands" className="fixed z-50 max-h-72 w-72 overflow-y-auto rounded-xl border border-zinc-700 bg-zinc-900 p-1.5 shadow-2xl" style={{ left, top }}><span className="sr-only" aria-live="polite">{selected ? `${selected.label}: ${selected.description}` : 'No matching commands'}</span>{commands.length ? commands.map((command, index) => <button key={command.id} type="button" role="option" aria-selected={index === selectedIndex} onMouseDown={(event) => event.preventDefault()} onClick={() => onSelect(command.id)} className={`block min-h-12 w-full rounded-lg px-3 py-2 text-left ${index === selectedIndex ? 'bg-amber-400/15 text-amber-100' : 'text-zinc-200 hover:bg-zinc-800'}`}><span className="block text-sm font-medium">{command.label}</span><span className="block text-xs text-zinc-500">{command.description}</span></button>) : <p className="px-3 py-4 text-sm text-zinc-500">No matching commands</p>}</div>
}

function NoteLinkTriggerMenu({ paths, selectedIndex, left, top, onSelect }: { paths:string[]; selectedIndex:number; left:number; top:number; onSelect:(path:string)=>void }) {
  return <div data-varianotes-note-link-menu role="listbox" aria-label="Internal note suggestions" className="fixed z-50 max-h-72 w-80 overflow-y-auto rounded-xl border border-zinc-700 bg-zinc-900 p-1.5 shadow-2xl" style={{ left,top }}>{paths.length ? paths.map((path,index) => <button key={path} type="button" role="option" aria-selected={index===selectedIndex} onMouseDown={(event)=>event.preventDefault()} onClick={()=>onSelect(path)} className={`block min-h-12 w-full rounded-lg px-3 py-2 text-left ${index===selectedIndex?'bg-amber-400/15 text-amber-100':'text-zinc-200 hover:bg-zinc-800'}`}><span className="block truncate text-sm font-medium">{path.split('/').pop()?.replace(/\.md$/i,'')}</span><span className="block truncate text-xs text-zinc-500">{path}</span></button>) : <p className="px-3 py-4 text-sm text-zinc-500">No matching notes</p>}</div>
}

function LinkPicker({ notePath, notePaths, draft, onApply, onClose }: { notePath:string; notePaths:string[]; draft:LinkDraft; onApply:(href:string,label?:string)=>void; onClose:()=>void }) {
  const [query, setQuery] = useState('')
  const [externalURL, setExternalURL] = useState(draft.existingHref ?? '')
  const [label, setLabel] = useState(draft.selectedText)
  const normalized = query.trim().toLowerCase()
  const matches = notePaths.filter((path) => path !== notePath && (!normalized || path.toLowerCase().includes(normalized))).slice(0, 100)
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-3 sm:p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}><section role="dialog" aria-modal="true" aria-labelledby="link-picker-title" className="flex max-h-[92vh] w-full max-w-xl flex-col rounded-xl border border-zinc-700 bg-zinc-900 shadow-2xl"><header className="flex items-center justify-between border-b border-zinc-800 px-4 py-3"><div><h2 id="link-picker-title" className="text-lg font-semibold text-zinc-100">Insert link</h2><p className="mt-1 text-xs text-zinc-500">Choose a note or enter an external URL. Internal links remain ordinary relative Markdown links.</p></div><button type="button" aria-label="Close link picker" onClick={onClose} className="min-h-10 min-w-10 rounded text-xl text-zinc-500 hover:bg-zinc-800">×</button></header><div className="min-h-0 overflow-y-auto p-4">{draft.from === draft.to && draft.existingHref === undefined && <label className="mb-4 block text-sm text-zinc-300">Link text<input value={label} onChange={(event) => setLabel(event.target.value)} placeholder="Descriptive link text" className="mt-2 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 outline-none focus:border-amber-500" /></label>}<label className="block text-sm text-zinc-300">Find a note<input autoFocus value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search notebook paths…" className="mt-2 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 outline-none focus:border-amber-500" /></label><div role="listbox" aria-label="Notebook notes" className="mt-3 max-h-64 overflow-y-auto rounded-md border border-zinc-800 bg-zinc-950 p-1">{matches.map((target) => <button key={target} type="button" role="option" aria-selected={false} onClick={() => onApply(portableRelativeNoteHref(notePath,target), label || draft.selectedText || target.split('/').pop()?.replace(/\.md$/i,'') || 'Note')} className="block min-h-11 w-full rounded px-3 py-2 text-left text-sm text-zinc-200 hover:bg-zinc-800"><span className="block truncate">{target.split('/').pop()?.replace(/\.md$/i,'')}</span><span className="block truncate text-xs text-zinc-500">{target}</span></button>)}{matches.length === 0 && <p className="px-3 py-4 text-sm text-zinc-500">No matching notes.</p>}</div><form className="mt-5 border-t border-zinc-800 pt-4" onSubmit={(event) => { event.preventDefault(); if (externalURL.trim()) onApply(externalURL.trim(), label || draft.selectedText || 'Link') }}><label className="block text-sm text-zinc-300">External URL or custom Markdown destination<input value={externalURL} onChange={(event) => setExternalURL(event.target.value)} placeholder="https://example.com" className="mt-2 w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 outline-none focus:border-amber-500" /></label><div className="mt-4 flex flex-wrap justify-end gap-2">{draft.existingHref !== undefined && <button type="button" onClick={() => onApply('')} className="min-h-10 rounded-md border border-red-900 px-4 text-sm text-red-300 hover:bg-red-950/30">Remove link</button>}<button type="button" onClick={onClose} className="min-h-10 rounded-md border border-zinc-700 px-4 text-sm text-zinc-300 hover:bg-zinc-800">Cancel</button><button type="submit" disabled={!externalURL.trim()} className="min-h-10 rounded-md bg-amber-500 px-4 text-sm font-medium text-zinc-950 hover:bg-amber-400 disabled:opacity-40">Apply URL</button></div></form></div></section></div>
}

function TablePicker({ size, onPreview, onSelect, onClose }: { size: TableSize; onPreview: (size: TableSize) => void; onSelect: (size: TableSize) => void; onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/55 p-4 pt-24" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-labelledby="table-picker-title" className="w-fit max-w-full rounded-xl border border-zinc-700 bg-zinc-900 p-4 shadow-2xl">
        <div className="flex items-center justify-between gap-6">
          <h2 id="table-picker-title" className="text-sm font-semibold text-zinc-100">Insert table</h2>
          <button type="button" aria-label="Close table picker" onClick={onClose} className="rounded px-2 py-1 text-zinc-400 hover:bg-zinc-800 hover:text-white">×</button>
        </div>
        <div className="mt-3 grid grid-cols-10 gap-1" role="grid" aria-label="Table dimensions">
          {Array.from({ length: 100 }, (_, index) => {
            const rows = Math.floor(index / 10) + 1
            const columns = (index % 10) + 1
            const active = rows <= size.rows && columns <= size.columns
            return <button key={`${rows}-${columns}`} type="button" role="gridcell" aria-label={`Insert ${columns} columns by ${rows} rows`} onPointerEnter={() => onPreview({ rows, columns })} onFocus={() => onPreview({ rows, columns })} onClick={() => onSelect({ rows, columns })} className={`h-6 w-6 rounded-sm border sm:h-7 sm:w-7 ${active ? 'border-amber-400 bg-amber-400/25' : 'border-zinc-600 bg-zinc-950 hover:border-zinc-400'}`} />
          })}
        </div>
        <p aria-live="polite" className="mt-3 text-center text-xs text-zinc-300">{size.columns} × {size.rows} <span className="text-zinc-500">(1 header + {Math.max(0, size.rows - 1)} body {size.rows === 2 ? 'row' : 'rows'})</span></p>
      </div>
    </div>
  )
}

type EditorIconName = 'undo' | 'redo' | 'bold' | 'italic' | 'strikethrough' | 'code' | 'code-block' | 'bullet-list' | 'numbered-list' | 'task-list' | 'quote' | 'link' | 'plus' | 'chevron-down' | 'markdown' | 'image' | 'table' | 'horizontal-rule'

function EditorIcon({ name }: { name: EditorIconName }) {
  let paths: ReactNode
  switch (name) {
    case 'undo': paths = <><path d="M3 7v6h6" /><path d="M21 17a9 9 0 0 0-15-6.7L3 13" /></>; break
    case 'redo': paths = <><path d="M21 7v6h-6" /><path d="M3 17a9 9 0 0 1 15-6.7L21 13" /></>; break
    case 'bold': paths = <><path d="M6 4h7a4 4 0 0 1 0 8H6z" /><path d="M6 12h8a4 4 0 0 1 0 8H6z" /></>; break
    case 'italic': paths = <><line x1="19" y1="4" x2="10" y2="4" /><line x1="14" y1="20" x2="5" y2="20" /><line x1="15" y1="4" x2="9" y2="20" /></>; break
    case 'strikethrough': paths = <><path d="M16 5c-1-.8-2.4-1.2-4-1.2-3 0-5 1.4-5 3.7 0 1.7 1.3 2.6 3 3.5" /><path d="M4 12h16" /><path d="M8 16c.7 1.3 2.2 2.2 4.5 2.2 3 0 5-1.3 5-3.5 0-1.4-1-2.4-2.7-3.1" /></>; break
    case 'code': paths = <><polyline points="8 8 4 12 8 16" /><polyline points="16 8 20 12 16 16" /><line x1="14" y1="4" x2="10" y2="20" /></>; break
    case 'code-block': paths = <><rect x="3" y="4" width="18" height="16" rx="2" /><polyline points="10 9 7 12 10 15" /><polyline points="14 9 17 12 14 15" /></>; break
    case 'bullet-list': paths = <><circle cx="4" cy="6" r="1" /><circle cx="4" cy="12" r="1" /><circle cx="4" cy="18" r="1" /><line x1="9" y1="6" x2="21" y2="6" /><line x1="9" y1="12" x2="21" y2="12" /><line x1="9" y1="18" x2="21" y2="18" /></>; break
    case 'numbered-list': paths = <><path d="M3 5h2v4M3 9h2" /><path d="M3 13c0-1.3 2.5-1.3 2.5 0 0 .7-.6 1.2-2.5 3h2.7" /><line x1="10" y1="6" x2="21" y2="6" /><line x1="10" y1="12" x2="21" y2="12" /><line x1="10" y1="18" x2="21" y2="18" /></>; break
    case 'task-list': paths = <><path d="m3 6 1.5 1.5L7 5" /><line x1="10" y1="6" x2="21" y2="6" /><rect x="3" y="11" width="4" height="4" rx="1" /><line x1="10" y1="13" x2="21" y2="13" /><rect x="3" y="18" width="4" height="3" rx="1" /><line x1="10" y1="19.5" x2="21" y2="19.5" /></>; break
    case 'quote': paths = <><path d="M4 7h6v5H5l-1 5" /><path d="M14 7h6v5h-5l-1 5" /></>; break
    case 'link': paths = <><path d="M10 13a5 5 0 0 0 7.1 0l2-2A5 5 0 0 0 12 3.9l-1.2 1.2" /><path d="M14 11a5 5 0 0 0-7.1 0l-2 2A5 5 0 0 0 12 20.1l1.2-1.2" /></>; break
    case 'plus': paths = <><line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" /></>; break
    case 'chevron-down': paths = <polyline points="6 9 12 15 18 9" />; break
    case 'markdown': paths = <><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline points="14 2 14 8 20 8" /><path d="M8 13v4l2-2 2 2v-4M15 13h3M16.5 13v4" /></>; break
    case 'image': paths = <><rect x="3" y="3" width="18" height="18" rx="2" /><circle cx="8.5" cy="8.5" r="1.5" /><path d="m21 15-5-5L5 21" /></>; break
    case 'table': paths = <><rect x="3" y="4" width="18" height="16" rx="2" /><line x1="3" y1="10" x2="21" y2="10" /><line x1="9" y1="4" x2="9" y2="20" /><line x1="15" y1="4" x2="15" y2="20" /></>; break
    case 'horizontal-rule': paths = <line x1="4" y1="12" x2="20" y2="12" />; break
  }
  return <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4 shrink-0">{paths}</svg>
}

function ToolbarButton({ label, active = false, disabled, onClick, children }: { label: string; active?: boolean; disabled: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" title={label} aria-label={label} aria-pressed={active} disabled={disabled} onMouseDown={(event) => event.preventDefault()} onClick={onClick} className={`inline-flex h-8 min-w-8 shrink-0 items-center justify-center gap-1.5 rounded-md px-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-500 disabled:cursor-default disabled:opacity-35 ${active ? 'border border-amber-500 bg-amber-400/15 text-amber-200 shadow-inner' : 'border border-transparent text-zinc-300 hover:bg-zinc-800 hover:text-white'}`}>{children}<span className="sr-only">{active ? ' active' : ''}</span></button>
}

function ToolbarDivider() { return <span aria-hidden="true" className="mx-1 h-5 w-px shrink-0 bg-zinc-700/80" /> }
