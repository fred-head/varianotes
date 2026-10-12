// @vitest-environment jsdom

import { cleanup, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SettingsDialog } from './App'
import './styles.css'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('vault onboarding and settings', () => {
  it('creates a filesystem vault by name', async () => {
    const onNotebookAdded = vi.fn()
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(Response.json({ id: 'vault-id', name: 'Personal' }, { status: 201 }))
    const view = render(<SettingsDialog mode="onboarding" autoLockMinutes={0} onAutoLockMinutes={vi.fn()} onNotebookAdded={onNotebookAdded} onClose={vi.fn()} />)

    fireEvent.change(view.getByLabelText('Vault name'), { target: { value: 'Personal' } })
    fireEvent.click(view.getByRole('button', { name: 'Create vault' }))

    await waitFor(() => expect(onNotebookAdded).toHaveBeenCalledOnce())
    expect(fetchMock).toHaveBeenCalledWith('/api/notebooks/local', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'Personal' }),
    }))
  })

  it('adopts an existing folder and explains retained legacy Git history', () => {
    const view = render(<SettingsDialog mode="onboarding" autoLockMinutes={0} onAutoLockMinutes={vi.fn()} onClose={vi.fn()} />)
    fireEvent.click(view.getByRole('button', { name: 'Adopt an existing folder' }))

    expect(view.getByLabelText('Vault name')).toBeTruthy()
    expect(view.getByLabelText('Folder name under the configured vault directory')).toBeTruthy()
    expect(view.getByText(/Old commits inside that folder may still contain deleted notes or secrets/)).toBeTruthy()
  })

  it('states the online-only and operator-owned backup model', () => {
    const view = render(<SettingsDialog mode="settings" autoLockMinutes={0} onAutoLockMinutes={vi.fn()} onClose={vi.fn()} />)
    expect(view.getByText(/operator is responsible for backups and restore/)).toBeTruthy()
    expect(view.getByText(/Note content is online-only/)).toBeTruthy()
    expect(view.getByText(/portable copy, not automatic backup/)).toBeTruthy()
  })

  it('scans, selects, confirms, and deletes only selected unused assets', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(Response.json({ assets: [{ path: 'Note.assets/one.png', size: 2048 }, { path: 'Note.assets/two.jpg', size: 1024 }] }))
      .mockResolvedValueOnce(Response.json({ deleted: ['Note.assets/one.png'], failures: [] }))
    const view = render(<SettingsDialog mode="settings" autoLockMinutes={0} onAutoLockMinutes={vi.fn()} onClose={vi.fn()} />)

    fireEvent.click(view.getByRole('button', { name: 'Scan vault' }))
    await view.findByText('2 unused assets found.')
    fireEvent.click(view.getAllByRole('checkbox')[1])
    fireEvent.click(view.getByRole('button', { name: /Remove 1 selected/ }))
    fireEvent.click(view.getByRole('button', { name: 'Remove assets' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const request = fetchMock.mock.calls[1][1]
    expect(JSON.parse(String(request?.body))).toEqual({ paths: ['Note.assets/one.png'] })
  })
})
