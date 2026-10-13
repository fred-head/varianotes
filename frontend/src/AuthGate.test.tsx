// @vitest-environment jsdom

import { cleanup, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthGate } from './AuthGate'

afterEach(()=>{cleanup();vi.restoreAllMocks();vi.unstubAllGlobals();localStorage.clear();sessionStorage.clear()})
beforeEach(()=>{Object.defineProperty(window,'matchMedia',{configurable:true,value:vi.fn().mockReturnValue({matches:false,addEventListener:vi.fn(),removeEventListener:vi.fn()})})})

describe('browser authentication lifecycle',()=>{
  it('shows first-run setup as a responsive application screen',async()=>{
    vi.spyOn(globalThis,'fetch').mockImplementation(async(input)=>{
      const url=String(input)
      if(url==='/api/auth/status')return Response.json({mode:'local',setupRequired:true,authenticated:false})
      if(url==='/api/health')return Response.json({status:'ok',version:'dev'})
      return Response.json({})
    })
    const view=render(<AuthGate/>)
    expect(await view.findByRole('heading',{name:'Set up your owner password'})).toBeTruthy()
    expect(view.getByText('VariaNotes')).toBeTruthy()
    expect(view.getByLabelText('One-time setup token')).toBeTruthy()
    expect(view.getAllByLabelText(/password/i)).toHaveLength(2)
  })

  it('logs in without storing credentials in browser storage',async()=>{
    let authenticated=false
    vi.spyOn(globalThis,'fetch').mockImplementation(async(input,init)=>{
      const url=String(input)
      if(url==='/api/auth/status')return Response.json({mode:'local',setupRequired:false,authenticated,csrfToken:authenticated?'csrf':''})
      if(url==='/api/auth/login'){authenticated=true;return Response.json({authenticated:true,csrfToken:'csrf'})}
      if(url==='/api/health')return Response.json({status:'ok',version:'dev'})
      if(url==='/api/repository/tree')return Response.json({entries:[]})
      if(url==='/api/notebook')return Response.json({name:'Notebook',configured:false})
      if(url==='/api/notebooks')return Response.json({activeId:'',notebooks:[]})
      return Response.json({}, {status:init?.method==='POST'?200:404})
    })
    const view=render(<AuthGate/>)
    fireEvent.change(await view.findByLabelText('Password'),{target:{value:'a sufficiently long password'}})
    fireEvent.click(view.getByLabelText('Remember this device'))
    fireEvent.click(view.getByRole('button',{name:'Sign in'}))
    await waitFor(()=>expect(view.getByText('No vault yet')).toBeTruthy())
    expect(localStorage.getItem('varianotes.password')).toBeNull()
    expect(sessionStorage.getItem('varianotes.password')).toBeNull()
  })

  it('completes password-first MFA without storing either factor',async()=>{
	let authenticated=false
	let mfaPending=false
	vi.spyOn(globalThis,'fetch').mockImplementation(async(input,init)=>{
	  const url=String(input)
	  if(url==='/api/auth/status')return Response.json({mode:'local',setupRequired:false,authenticated,mfaRequired:mfaPending,csrfToken:authenticated?'csrf':''})
	  if(url==='/api/auth/login'){mfaPending=true;return Response.json({authenticated:false,mfaRequired:true},{status:202})}
	  if(url==='/api/auth/login/mfa'){authenticated=true;mfaPending=false;return Response.json({authenticated:true,csrfToken:'csrf'})}
	  if(url==='/api/health')return Response.json({status:'ok',version:'dev'})
	  if(url==='/api/repository/tree')return Response.json({entries:[]})
	  if(url==='/api/notebook')return Response.json({name:'Notebook',configured:false})
	  if(url==='/api/notebooks')return Response.json({activeId:'',notebooks:[]})
	  return Response.json({}, {status:init?.method==='POST'?200:404})
	})
	const view=render(<AuthGate/>)
	fireEvent.change(await view.findByLabelText('Password'),{target:{value:'a sufficiently long password'}})
	fireEvent.click(view.getByRole('button',{name:'Sign in'}))
	await view.findByLabelText('Authentication code')
	window.dispatchEvent(new Event('focus'))
	await waitFor(()=>expect(view.getByLabelText('Authentication code')).toBeTruthy())
	fireEvent.change(await view.findByLabelText('Authentication code'),{target:{value:'123456'}})
	fireEvent.click(view.getByRole('button',{name:'Verify'}))
	await waitFor(()=>expect(view.getByText('No vault yet')).toBeTruthy())
	expect([...Array(localStorage.length)].map((_,index)=>localStorage.getItem(localStorage.key(index)!)).join(' ')).not.toContain('a sufficiently long password')
	expect([...Array(sessionStorage.length)].map((_,index)=>sessionStorage.getItem(sessionStorage.key(index)!)).join(' ')).not.toContain('123456')
	expect(vi.mocked(globalThis.fetch).mock.calls.some(([url])=>String(url)==='/api/auth/login/mfa')).toBe(true)
  })

  it('uses the sign-in password once to unlock a protected vault without storing it', async () => {
    class EventSourceStub {
      constructor() {}
      addEventListener() {}
      removeEventListener() {}
      close() {}
    }
    vi.stubGlobal('EventSource', EventSourceStub)
    let authenticated = false
    const submittedVaultPasswords: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input)
      if (url === '/api/auth/status') return Response.json({ mode: 'local', setupRequired: false, authenticated, csrfToken: authenticated ? 'csrf' : '' })
      if (url === '/api/auth/login') {
        authenticated = true
        return Response.json({ authenticated: true, csrfToken: 'csrf' })
      }
      if (url === '/api/health') return Response.json({ status: 'ok', version: 'dev' })
      if (url === '/api/notebook') return Response.json({ name: 'Protected', configured: true })
      if (url === '/api/notebooks') return Response.json({ activeId: 'protected-vault', notebooks: [{ id: 'protected-vault', name: 'Protected' }] })
      if (url === '/api/encryption/status') return Response.json({
        vaultId: 'protected-vault', protected: true, locked: true, generation: 1, recoveryCodeConfigured: true,
        unlockPasswordMode: 'login', activationEnabled: false, autoLockMinutes: 15, mfaEnabled: false,
      })
      if (url === '/api/encryption/unlock') {
        submittedVaultPasswords.push((JSON.parse(String(init?.body)) as { password: string }).password)
        return Response.json({ vaultId: 'protected-vault', unlocked: true, generation: 2 })
      }
      if (url === '/api/repository/tree') return Response.json({ entries: [] })
      return Response.json({ error: `unexpected request: ${url}` }, { status: 500 })
    })

    const view = render(<AuthGate />)
    const password = 'login password used for unlock'
    fireEvent.change(await view.findByLabelText('Password'), { target: { value: password } })
    fireEvent.click(view.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(submittedVaultPasswords).toEqual([password]))
    expect(view.getByRole('button', { name: 'Lock vault' })).toBeTruthy()
    const storedValues = [localStorage, sessionStorage].flatMap((storage) => Array.from({ length: storage.length }, (_, index) => storage.getItem(storage.key(index)!) ?? '')).join(' ')
    expect(storedValues).not.toContain(password)
  })

  it('confirms a replacement recovery code without persisting the code or password', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input)
      if (url === '/api/auth/status') return Response.json({ mode: 'local', setupRequired: false, authenticated: false })
      if (url === '/api/health') return Response.json({ status: 'ok', version: 'dev' })
      if (url === '/api/auth/recovery/prepare') return Response.json({ challengeId: 'challenge-1', recoveryCode: 'VNRC1-REPLACEMENT-CODE' })
      if (url === '/api/auth/recovery/confirm') return Response.json({ confirmed: true })
      return Response.json({ error: `unexpected request: ${url}`, method: init?.method }, { status: 500 })
    })

    const view = render(<AuthGate />)
    fireEvent.click(await view.findByRole('button', { name: 'Forgot password? Use a vault recovery code' }))
    fireEvent.change(view.getByLabelText('Vault recovery code'), { target: { value: 'VNRC1-OLD-RECOVERY-CODE' } })
    fireEvent.change(view.getByLabelText('New password'), { target: { value: 'a replacement password with enough length' } })
    fireEvent.change(view.getByLabelText('Confirm new password'), { target: { value: 'a replacement password with enough length' } })
    fireEvent.click(view.getByRole('button', { name: 'Prepare recovery' }))

    expect(await view.findByText('VNRC1-REPLACEMENT-CODE')).toBeTruthy()
    fireEvent.change(view.getByLabelText('Re-enter the replacement code'), { target: { value: 'VNRC1-REPLACEMENT-CODE' } })
    fireEvent.click(view.getByLabelText(/I stored this code separately/))
    fireEvent.click(view.getByRole('button', { name: 'Confirm recovery' }))

    await waitFor(() => expect(view.getByRole('heading', { name: 'Welcome back' })).toBeTruthy())
    const storedValues = [localStorage, sessionStorage].flatMap((storage) => Array.from({ length: storage.length }, (_, index) => storage.getItem(storage.key(index)!) ?? '')).join(' ')
    expect(storedValues).not.toContain('VNRC1-')
    expect(storedValues).not.toContain('replacement password')
    const confirmRequest = vi.mocked(globalThis.fetch).mock.calls.find(([url]) => String(url) === '/api/auth/recovery/confirm')
    expect(confirmRequest).toBeTruthy()
    expect(String(confirmRequest?.[0])).not.toContain('VNRC1-')
  })

  it('returns a mobile standalone window to login when its session expires',async()=>{
    Object.defineProperty(window,'innerWidth',{configurable:true,value:390})
    Object.defineProperty(window,'matchMedia',{configurable:true,value:vi.fn().mockImplementation((query:string)=>({matches:query.includes('standalone'),addEventListener:vi.fn(),removeEventListener:vi.fn()}))})
    vi.spyOn(globalThis,'fetch').mockImplementation(async(input)=>{
      const url=String(input)
      if(url==='/api/auth/status')return Response.json({mode:'local',setupRequired:false,authenticated:true,csrfToken:'csrf'})
      if(url==='/api/health')return Response.json({status:'ok',version:'dev'})
      if(url==='/api/repository/tree')return Response.json({entries:[]})
      if(url==='/api/notebook')return Response.json({name:'Notebook',configured:false})
      if(url==='/api/notebooks')return Response.json({activeId:'',notebooks:[]})
      return Response.json({})
    })
    const view=render(<AuthGate/>)
    expect(await view.findByText('No vault yet')).toBeTruthy()
    window.dispatchEvent(new CustomEvent('varianotes:auth-required'))
    expect(await view.findByRole('heading',{name:'Welcome back'})).toBeTruthy()
    expect(view.getByLabelText('Password')).toBeTruthy()
  })
})
