// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiFetch, listenForAuthEvents, notifyAuthChanged, setCSRFToken } from './api'

afterEach(()=>{vi.restoreAllMocks();setCSRFToken();localStorage.clear();sessionStorage.clear()})

describe('authenticated API client',()=>{
  it('adds the in-memory CSRF token only to state-changing requests',async()=>{
    const fetchMock=vi.spyOn(globalThis,'fetch').mockResolvedValue(Response.json({ok:true}))
    setCSRFToken('session-csrf')
    await apiFetch('/api/read')
    await apiFetch('/api/write',{method:'POST'})
    expect(fetchMock.mock.calls[0][1]?.cache).toBe('no-store')
    expect(fetchMock.mock.calls[1][1]?.cache).toBe('no-store')
    expect(new Headers(fetchMock.mock.calls[0][1]?.headers).get('X-CSRF-Token')).toBeNull()
    expect(new Headers(fetchMock.mock.calls[1][1]?.headers).get('X-CSRF-Token')).toBe('session-csrf')
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })

  it('announces session expiry for authenticated API requests',async()=>{
    vi.spyOn(globalThis,'fetch').mockResolvedValue(Response.json({code:'authentication_required'},{status:401}))
    const listener=vi.fn();window.addEventListener('varianotes:auth-required',listener)
    await apiFetch('/api/repository/tree')
    expect(listener).toHaveBeenCalledOnce()
    window.removeEventListener('varianotes:auth-required',listener)
  })

  it('does not expire the browser session for rejected vault credentials',async()=>{
    vi.spyOn(globalThis,'fetch').mockResolvedValue(Response.json({code:'invalid_credentials',error:'vault password is incorrect'},{status:401}))
    const listener=vi.fn();window.addEventListener('varianotes:auth-required',listener)
    await apiFetch('/api/encryption/unlock',{method:'POST'})
    expect(listener).not.toHaveBeenCalled()
    window.removeEventListener('varianotes:auth-required',listener)
  })

  it('broadcasts authentication changes to other tabs and PWA windows',()=>{
    const channels: MockBroadcastChannel[]=[]
    class MockBroadcastChannel {
      onmessage: (()=>void)|null=null
      constructor(readonly name:string){channels.push(this)}
      postMessage(){for(const channel of channels)if(channel!==this&&channel.name===this.name)channel.onmessage?.()}
      close(){}
    }
    vi.stubGlobal('BroadcastChannel',MockBroadcastChannel)
    const otherContext=vi.fn()
    const stop=listenForAuthEvents(()=>undefined,otherContext)
    notifyAuthChanged()
    expect(otherContext).toHaveBeenCalledOnce()
    expect(channels.every((channel)=>channel.name==='varianotes-auth')).toBe(true)
    stop()
    vi.unstubAllGlobals()
  })
})
