import bash from 'highlight.js/lib/languages/bash'
import css from 'highlight.js/lib/languages/css'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import go from 'highlight.js/lib/languages/go'
import highlight from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import markdown from 'highlight.js/lib/languages/markdown'
import powershell from 'highlight.js/lib/languages/powershell'
import python from 'highlight.js/lib/languages/python'
import sql from 'highlight.js/lib/languages/sql'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'
import { $prose, $view } from '@milkdown/kit/utils'
import { codeBlockSchema } from '@milkdown/kit/preset/commonmark'
import type { Node as ProseMirrorNode } from '@milkdown/kit/prose/model'
import { Plugin, PluginKey } from '@milkdown/kit/prose/state'
import { Decoration, DecorationSet, type ViewMutationRecord } from '@milkdown/kit/prose/view'

export const codeBlockLanguages = [
  { label: 'Plain text', value: '' },
  { label: 'Shell / Bash', value: 'bash' },
  { label: 'PowerShell', value: 'powershell' },
  { label: 'Python', value: 'python' },
  { label: 'JavaScript', value: 'javascript' },
  { label: 'TypeScript', value: 'typescript' },
  { label: 'JSON', value: 'json' },
  { label: 'YAML', value: 'yaml' },
  { label: 'Markdown', value: 'markdown' },
  { label: 'HTML', value: 'html' },
  { label: 'CSS', value: 'css' },
  { label: 'SQL', value: 'sql' },
  { label: 'Go', value: 'go' },
  { label: 'Dockerfile', value: 'dockerfile' },
] as const

highlight.registerLanguage('bash', bash)
highlight.registerLanguage('css', css)
highlight.registerLanguage('dockerfile', dockerfile)
highlight.registerLanguage('go', go)
highlight.registerLanguage('javascript', javascript)
highlight.registerLanguage('json', json)
highlight.registerLanguage('markdown', markdown)
highlight.registerLanguage('powershell', powershell)
highlight.registerLanguage('python', python)
highlight.registerLanguage('sql', sql)
highlight.registerLanguage('typescript', typescript)
highlight.registerLanguage('xml', xml)
highlight.registerLanguage('yaml', yaml)

const highlightedLanguageAliases: Record<string, string> = {
  bash: 'bash',
  shell: 'bash',
  sh: 'bash',
  powershell: 'powershell',
  ps1: 'powershell',
  python: 'python',
  py: 'python',
  javascript: 'javascript',
  js: 'javascript',
  typescript: 'typescript',
  ts: 'typescript',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  markdown: 'markdown',
  md: 'markdown',
  html: 'xml',
  xml: 'xml',
  css: 'css',
  sql: 'sql',
  go: 'go',
  dockerfile: 'dockerfile',
}

function normalizedLanguage(language: unknown): string {
  return typeof language === 'string' ? language.toLowerCase() : ''
}

export function codeBlockLanguageLabel(language: unknown): string {
  const value = typeof language === 'string' ? language : ''
  const normalized = normalizedLanguage(value)
  if (!normalized) return ''
  const canonical = highlightedLanguageAliases[normalized]
  const languageOption = canonical === 'xml' ? 'html' : canonical
  const selected = codeBlockLanguages.find((option) => option.value === normalized)
    ?? codeBlockLanguages.find((option) => option.value === languageOption)
  return selected?.label ?? value
}

function addBlockHighlights(node: ProseMirrorNode, position: number): Decoration[] {
  const language = highlightedLanguageAliases[normalizedLanguage(node.attrs.language)]
  if (!language || !highlight.getLanguage(language) || node.textContent.length > 100_000) return []

  try {
    const result = highlight.highlight(node.textContent, { language, ignoreIllegals: true })
    const template = document.createElement('template')
    template.innerHTML = result.value
    if (template.content.textContent !== node.textContent) return []

    const ranges: Decoration[] = []
    let offset = 0
    const visit = (current: globalThis.Node, inherited: string[]) => {
      if (current.nodeType === globalThis.Node.TEXT_NODE) {
        const text = current.textContent ?? ''
        const from = offset
        offset += text.length
        const classes = [...new Set(inherited)]
        if (text.length && classes.length) {
          ranges.push(Decoration.inline(position + 1 + from, position + 1 + offset, { class: classes.join(' ') }))
        }
        return
      }

      const tokenClasses = current instanceof HTMLElement
        ? Array.from(current.classList).filter((name) => /^hljs-[a-z0-9_-]+$/.test(name))
        : []
      for (const child of Array.from(current.childNodes)) visit(child, [...inherited, ...tokenClasses])
    }
    visit(template.content, [])
    return ranges
  } catch {
    // A malformed or unsupported code sample stays readable as plain text.
    return []
  }
}

function codeBlockDecorations(doc: ProseMirrorNode): DecorationSet {
  const decorations: Decoration[] = []
  doc.descendants((node, position) => {
    if (node.type.name === 'code_block') decorations.push(...addBlockHighlights(node, position))
  })
  return DecorationSet.create(doc, decorations)
}

const codeHighlightKey = new PluginKey<DecorationSet>('varianotesCodeHighlight')

export const codeHighlightPlugin = $prose(() => new Plugin<DecorationSet>({
  key: codeHighlightKey,
  state: {
    init: (_config, state) => codeBlockDecorations(state.doc),
    apply: (transaction, previous, _oldState, nextState) => transaction.docChanged
      ? codeBlockDecorations(nextState.doc)
      : previous.map(transaction.mapping, transaction.doc),
  },
  props: {
    decorations: (state) => codeHighlightKey.getState(state),
  },
}))

export const codeBlockView = $view(codeBlockSchema.node, () => (initialNode, editorView, getPos) => {
  const dom = document.createElement('div')
  dom.className = 'varianotes-code-block'

  const language = document.createElement('span')
  language.className = 'varianotes-code-block-language'
  language.setAttribute('data-editable', String(editorView.editable))

  const languageSelect = document.createElement('select')
  languageSelect.className = 'varianotes-code-block-language-select'
  languageSelect.setAttribute('aria-label', 'Code block language')
  languageSelect.setAttribute('contenteditable', 'false')
  languageSelect.append(new Option('Language', ''))
  for (const option of codeBlockLanguages) {
    if (option.value) languageSelect.append(new Option(option.label, option.value))
  }

  const languageLabel = document.createElement('span')
  languageLabel.className = 'varianotes-code-block-language-label'
  language.append(languageSelect, languageLabel)

  const copyButton = document.createElement('button')
  copyButton.type = 'button'
  copyButton.className = 'varianotes-code-block-copy'
  copyButton.setAttribute('aria-label', 'Copy code block')
  copyButton.title = 'Copy code block'
  copyButton.setAttribute('contenteditable', 'false')

  const renderCopyIcon = (copied: boolean) => {
    const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
    icon.setAttribute('viewBox', '0 0 20 20')
    icon.setAttribute('aria-hidden', 'true')
    icon.setAttribute('focusable', 'false')
    icon.dataset.icon = copied ? 'check' : 'copy'
    if (copied) {
      const check = document.createElementNS('http://www.w3.org/2000/svg', 'path')
      check.setAttribute('d', 'm4 10.5 4 4 8-9')
      icon.append(check)
    } else {
      const back = document.createElementNS('http://www.w3.org/2000/svg', 'rect')
      back.setAttribute('x', '7')
      back.setAttribute('y', '7')
      back.setAttribute('width', '10')
      back.setAttribute('height', '10')
      back.setAttribute('rx', '2')
      const front = document.createElementNS('http://www.w3.org/2000/svg', 'path')
      front.setAttribute('d', 'M13 7V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2')
      icon.append(back, front)
    }
    copyButton.replaceChildren(icon)
  }
  renderCopyIcon(false)

  const status = document.createElement('span')
  status.className = 'varianotes-code-block-copy-status'
  status.setAttribute('role', 'status')
  status.setAttribute('aria-live', 'polite')
  status.setAttribute('aria-atomic', 'true')

  const pre = document.createElement('pre')
  pre.className = 'varianotes-code-block-pre'
  const contentDOM = document.createElement('code')
  contentDOM.className = 'varianotes-code-block-content'
  pre.append(contentDOM)
  dom.append(language, copyButton, status, pre)

  let currentNode = initialNode
  const render = (node: ProseMirrorNode) => {
    const value = typeof node.attrs.language === 'string' ? node.attrs.language : ''
    const editable = editorView.editable
    language.dataset.language = value
    language.dataset.editable = String(editable)
    languageSelect.disabled = !editable
    languageSelect.hidden = !editable
    languageLabel.hidden = editable || !value
    languageLabel.textContent = codeBlockLanguageLabel(value)
    language.hidden = !editable && !value

    for (const customOption of languageSelect.querySelectorAll('option[data-custom-language="true"]')) customOption.remove()
    if (value && !codeBlockLanguages.some((option) => option.value === value)) {
      const customOption = new Option(codeBlockLanguageLabel(value), value)
      customOption.dataset.customLanguage = 'true'
      languageSelect.append(customOption)
    }
    languageSelect.value = value
  }

  let copyResetTimer: number | undefined
  let statusTimer: number | undefined
  let destroyed = false
  const announce = (message: string) => {
    status.textContent = ''
    if (statusTimer !== undefined) window.clearTimeout(statusTimer)
    statusTimer = window.setTimeout(() => {
      if (!destroyed) status.textContent = message
      statusTimer = undefined
    }, 0)
  }
  const resetCopyIcon = () => {
    if (copyResetTimer !== undefined) window.clearTimeout(copyResetTimer)
    copyResetTimer = undefined
    renderCopyIcon(false)
  }
  const copy = async () => {
    try {
      const clipboard = navigator.clipboard
      if (!clipboard?.writeText) throw new Error('Clipboard access is unavailable')
      await clipboard.writeText(currentNode.textContent)
      renderCopyIcon(true)
      if (copyResetTimer !== undefined) window.clearTimeout(copyResetTimer)
      copyResetTimer = window.setTimeout(resetCopyIcon, 1400)
      announce('Code copied to clipboard.')
    } catch {
      resetCopyIcon()
      announce('Copy failed. Select and copy the code manually.')
    }
  }
  const setLanguage = () => {
    if (!editorView.editable) return
    const position = getPos()
    if (typeof position !== 'number') return
    const node = editorView.state.doc.nodeAt(position)
    if (!node || node.type !== currentNode.type) return
    editorView.dispatch(editorView.state.tr.setNodeAttribute(position, 'language', languageSelect.value))
  }
  const preserveEditorSelection = (event: MouseEvent) => event.preventDefault()
  copyButton.addEventListener('click', copy)
  copyButton.addEventListener('mousedown', preserveEditorSelection)
  languageSelect.addEventListener('change', setLanguage)
  render(initialNode)

  return {
    dom,
    contentDOM,
    update: (node: ProseMirrorNode) => {
      if (node.type !== initialNode.type) return false
      currentNode = node
      render(node)
      return true
    },
    stopEvent: (event: Event) => event.target instanceof globalThis.Node
      && (copyButton.contains(event.target) || languageSelect.contains(event.target)),
    ignoreMutation: (mutation: ViewMutationRecord) => mutation.type !== 'selection' && !contentDOM.contains(mutation.target),
    selectNode: () => dom.classList.add('ProseMirror-selectednode'),
    deselectNode: () => dom.classList.remove('ProseMirror-selectednode'),
    destroy: () => {
      destroyed = true
      if (copyResetTimer !== undefined) window.clearTimeout(copyResetTimer)
      if (statusTimer !== undefined) window.clearTimeout(statusTimer)
      copyButton.removeEventListener('click', copy)
      copyButton.removeEventListener('mousedown', preserveEditorSelection)
      languageSelect.removeEventListener('change', setLanguage)
    },
  }
})
