import { describe, expect, it, vi } from 'vitest'
import { AutoLockController, isEditorEditable, loadAutoLockPreference, saveAutoLockPreference } from './autoLock'

function fakeStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
  }
}

describe('editor mode', () => {
  it('prevents editing in Read only and restores it in Edit', () => {
    expect(isEditorEditable(true)).toBe(false)
    expect(isEditorEditable(false)).toBe(true)
  })
})

describe('auto-lock preference', () => {
  it('defaults invalid or missing preferences to Off and persists valid values', () => {
    const storage = fakeStorage()
    expect(loadAutoLockPreference(storage)).toBe(0)
    saveAutoLockPreference(storage, 15)
    expect(loadAutoLockPreference(storage)).toBe(15)
    storage.setItem('varianotes.auto-lock-minutes', '2')
    expect(loadAutoLockPreference(storage)).toBe(0)
  })
})

describe('AutoLockController', () => {
  it('locks after the configured inactivity period', () => {
    vi.useFakeTimers()
    const expire = vi.fn()
    const controller = new AutoLockController(expire)
    controller.update(1, true)
    vi.advanceTimersByTime(60_000)
    expect(expire).toHaveBeenCalledOnce()
    controller.dispose()
    vi.useRealTimers()
  })

  it('resets only when document activity is reported', () => {
    vi.useFakeTimers()
    const expire = vi.fn()
    const controller = new AutoLockController(expire)
    controller.update(1, true)
    vi.advanceTimersByTime(50_000)
    controller.activity()
    vi.advanceTimersByTime(50_000)
    expect(expire).not.toHaveBeenCalled()
    vi.advanceTimersByTime(10_000)
    expect(expire).toHaveBeenCalledOnce()
    controller.dispose()
    vi.useRealTimers()
  })

  it('does not lock when disabled or inactive', () => {
    vi.useFakeTimers()
    const expire = vi.fn()
    const controller = new AutoLockController(expire)
    controller.update(1, true)
    controller.update(0, false)
    vi.runAllTimers()
    expect(expire).not.toHaveBeenCalled()
    controller.dispose()
    vi.useRealTimers()
  })
})
