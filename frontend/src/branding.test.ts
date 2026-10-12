import { describe, expect, it } from 'vitest'
import { appIconPath, pwaManifestId, productDescription, productName } from '../branding'
import htmlDocument from '../index.html?raw'
import pwaConfig from '../vite.config.ts?raw'

describe('VariaNotes browser metadata', () => {
  it('uses the product name and description in browser metadata', () => {
    expect(productName).toBe('VariaNotes')
    expect(productDescription).toBe('Self-hosted Markdown notes with full control over your data')
    expect(appIconPath).toBe('/varianotes.svg')
    expect(htmlDocument).toContain('<title>VariaNotes</title>')
    expect(htmlDocument).toContain('name="apple-mobile-web-app-title" content="VariaNotes"')
    expect(htmlDocument).toContain(`content="${productDescription}"`)
  })

  it('keeps the installed PWA identity and network-only API behavior', () => {
    expect(pwaManifestId).toBe('/')
    expect(appIconPath).toBe('/varianotes.svg')
    expect(pwaConfig).toContain('id: pwaManifestId')
    expect(pwaConfig).toContain('name: productName')
    expect(pwaConfig).toContain('short_name: productName')
    expect(pwaConfig).toContain('description: productDescription')
    expect(pwaConfig).toContain('url.pathname.startsWith(\'/api/\')')
  })
})
