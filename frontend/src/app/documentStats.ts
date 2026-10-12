import type { Node } from '@milkdown/kit/prose/model'

export type DocumentStats = {
  words: number
  characters: number
  lines: number
}

export const emptyDocumentStats: DocumentStats = { words: 0, characters: 0, lines: 0 }

type Segment = { segment: string; isWordLike?: boolean }
type Segmenter = { segment: (value: string) => Iterable<Segment> }
type SegmenterConstructor = new (locale?: string | string[], options?: { granularity: 'word' | 'grapheme' }) => Segmenter

const SegmenterClass = (Intl as typeof Intl & { Segmenter?: SegmenterConstructor }).Segmenter
const wordSegmenter = SegmenterClass ? new SegmenterClass(undefined, { granularity: 'word' }) : undefined
const graphemeSegmenter = SegmenterClass ? new SegmenterClass(undefined, { granularity: 'grapheme' }) : undefined
const statsCache = new WeakMap<Node, DocumentStats>()

function countWords(text: string): number {
  if (wordSegmenter) {
    let words = 0
    for (const segment of wordSegmenter.segment(text)) {
      if (segment.isWordLike) words += 1
    }
    return words
  }

  return text.match(/[\p{L}\p{N}]+(?:['’_-][\p{L}\p{N}]+)*/gu)?.length ?? 0
}

function countCharacters(text: string): number {
  const visibleText = text.replace(/[\r\n]/g, '')
  if (graphemeSegmenter) {
    let characters = 0
    for (const segment of graphemeSegmenter.segment(visibleText)) {
      if (segment.segment) characters += 1
    }
    return characters
  }

  return Array.from(visibleText).length
}

function countHardBreaks(node: Node): number {
  let breaks = 0
  node.descendants((child) => {
    if (child.type.name === 'hard_break') breaks += 1
  })
  return breaks
}

/**
 * Counts the editor's parsed document, so Markdown syntax, link destinations,
 * image metadata, and empty inline break nodes never become visible text.
 * Every text block contributes one logical line; explicit hard breaks and
 * embedded code-block line endings add lines without adding characters.
 */
export function documentStats(document: Node): DocumentStats {
  const cached = statsCache.get(document)
  if (cached) return cached

  let words = 0
  let characters = 0
  let lines = 0

  function visit(node: Node, tableRowLineCountSuppressed = false) {
    if (node.type.name === 'table_row') {
      lines += 1
      node.forEach((child) => visit(child, true))
      return
    }

    if (node.isTextblock) {
      const text = node.textBetween(0, node.content.size, '')
      const wordText = node.textBetween(0, node.content.size, '', ' ')
      words += countWords(wordText)
      characters += countCharacters(text)
      if (!tableRowLineCountSuppressed) {
        lines += text.split(/\r\n|\r|\n/).length + countHardBreaks(node)
      }
      return
    }

    if (node.type.name !== 'doc' && node.isBlock && node.childCount === 0 && !tableRowLineCountSuppressed) {
      lines += 1
      return
    }

    node.forEach((child) => visit(child, tableRowLineCountSuppressed))
  }

  visit(document)
  const stats = { words, characters, lines }
  statsCache.set(document, stats)
  return stats
}
