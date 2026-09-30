import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { usePersistentState } from './usePersistentState'

const allowed = ['5m', '15m', '1h'] as const

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('usePersistentState', () => {
  it('survives a reload', () => {
    const first = renderHook(() => usePersistentState('test.range', '1h', allowed))
    act(() => first.result.current[1]('5m'))
    first.unmount()
    const second = renderHook(() => usePersistentState('test.range', '1h', allowed))
    expect(second.result.current[0]).toBe('5m')
  })

  it('ignores a stored value that is not allowed', () => {
    localStorage.setItem('myserver.test.range', '99y')
    const { result } = renderHook(() => usePersistentState('test.range', '1h', allowed))
    expect(result.current[0]).toBe('1h')
  })

  it('keeps working when storage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    const { result } = renderHook(() => usePersistentState('test.range', '1h', allowed))
    expect(result.current[0]).toBe('1h')
    act(() => result.current[1]('15m'))
    expect(result.current[0]).toBe('15m')
  })
})
