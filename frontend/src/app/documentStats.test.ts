import { Schema, type Node } from '@milkdown/kit/prose/model'
import { describe, expect, it } from 'vitest'
import { documentStats } from './documentStats'

const schema = new Schema({
  nodes: {
    doc: { content: 'block*' },
    paragraph: { content: 'inline*', group: 'block' },
    heading: { content: 'inline*', group: 'block' },
    code_block: { content: 'text*', group: 'block', code: true },
    blockquote: { content: 'block+', group: 'block' },
    bullet_list: { content: 'list_item+', group: 'block' },
    list_item: { content: 'paragraph block*' },
    table: { content: 'table_row+', group: 'block' },
    table_row: { content: 'table_cell+' },
    table_cell: { content: 'block+' },
    image: { inline: true, group: 'inline', atom: true, attrs: { alt: { default: '' }, src: { default: '' }, title: { default: null } } },
    hard_break: { inline: true, group: 'inline', atom: true },
    horizontal_rule: { group: 'block', atom: true },
    text: { group: 'inline' },
  },
  marks: {
    strong: {},
    link: { attrs: { href: { default: '' }, title: { default: null } } },
  },
})

function paragraph(...children: Node[]): Node {
  return schema.node('paragraph', null, children)
}

function text(value: string, marks?: string[]): Node {
  return schema.text(value, marks?.map((mark) => schema.mark(mark)))
}

function document(...blocks: Node[]): Node {
  return schema.node('doc', null, blocks)
}

describe('documentStats', () => {
  it('counts readable text while ignoring image attributes, link destinations, and Markdown structure', () => {
    const note = document(
      schema.node('heading', { level: 1 }, [text('Hello '), text('world', ['strong'])]),
      paragraph(
        text('read '),
        text('this label', ['link']),
        schema.node('image', { alt: 'hidden generated image words', src: 'Note.assets/generated-image.png', title: 'hidden title' }),
        schema.node('hard_break'),
        text('next'),
      ),
      schema.node('blockquote', null, [paragraph(text('quoted text'))]),
      schema.node('bullet_list', null, [schema.node('list_item', null, [paragraph(text('item text'))])]),
      schema.node('code_block', null, [text('const café = "👩‍💻";\nreturn true')]),
      schema.node('horizontal_rule'),
    )

    expect(documentStats(note)).toEqual({ words: 14, characters: 78, lines: 8 })
  })

  it('lets empty paragraphs and hard breaks affect lines without adding words or characters', () => {
    const note = document(
      paragraph(),
      paragraph(text('A'), schema.node('hard_break'), text('B')),
      schema.node('code_block', null, [text('one\r\ntwo')]),
    )

    expect(documentStats(note)).toEqual({ words: 4, characters: 8, lines: 5 })
  })

  it('counts composed Unicode text and emoji as user-perceived characters', () => {
    const note = document(paragraph(text('e\u0301 👨‍👩‍👧‍👦')))

    expect(documentStats(note)).toEqual({ words: 1, characters: 3, lines: 1 })
  })

  it('returns zero statistics for a document with no blocks', () => {
    expect(documentStats(document())).toEqual({ words: 0, characters: 0, lines: 0 })
  })

  it('counts each table row as one logical line', () => {
    const note = document(schema.node('table', null, [
      schema.node('table_row', null, [
        schema.node('table_cell', null, [paragraph(text('first cell'))]),
        schema.node('table_cell', null, [paragraph(text('second cell'))]),
      ]),
    ]))

    expect(documentStats(note)).toEqual({ words: 4, characters: 21, lines: 1 })
  })
})
