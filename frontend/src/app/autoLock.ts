export type AutoLockMinutes = 0 | 1 | 5 | 15 | 30

export const autoLockOptions: ReadonlyArray<{ value: AutoLockMinutes; label: string }> = [
  { value: 0, label: 'Off' },
  { value: 1, label: '1 minute' },
  { value: 5, label: '5 minutes' },
  { value: 15, label: '15 minutes' },
  { value: 30, label: '30 minutes' },
]

export function parseAutoLockMinutes(value: string | null): AutoLockMinutes {
  const parsed = Number(value)
  return autoLockOptions.some((option) => option.value === parsed) ? parsed as AutoLockMinutes : 0
}

type PreferenceStorage = Pick<Storage, 'getItem' | 'setItem'>

export function loadAutoLockPreference(storage: PreferenceStorage): AutoLockMinutes {
  return parseAutoLockMinutes(storage.getItem('varianotes.auto-lock-minutes'))
}

export function saveAutoLockPreference(storage: PreferenceStorage, value: AutoLockMinutes) {
  storage.setItem('varianotes.auto-lock-minutes', String(value))
}

export function isEditorEditable(readOnly: boolean) {
  return !readOnly
}

type TimerHandle = ReturnType<typeof setTimeout>
type Schedule = (callback: () => void, delay: number) => TimerHandle
type Cancel = (handle: TimerHandle) => void

const browserSchedule: Schedule = (callback, delay) => globalThis.setTimeout(callback, delay)
const browserCancel: Cancel = (handle) => globalThis.clearTimeout(handle)

export class AutoLockController {
  private handle?: TimerHandle
  private delay = 0
  private active = false

  constructor(
    private readonly onExpire: () => void,
    private readonly schedule: Schedule = browserSchedule,
    private readonly cancel: Cancel = browserCancel,
  ) {}

  update(minutes: AutoLockMinutes, active: boolean) {
    this.delay = minutes * 60_000
    this.active = active && this.delay > 0
    this.restart()
  }

  activity() {
    if (this.active) this.restart()
  }

  dispose() {
    if (this.handle !== undefined) this.cancel(this.handle)
    this.handle = undefined
    this.active = false
  }

  private restart() {
    if (this.handle !== undefined) this.cancel(this.handle)
    this.handle = undefined
    if (!this.active) return
    this.handle = this.schedule(() => {
      this.handle = undefined
      this.onExpire()
    }, this.delay)
  }
}
