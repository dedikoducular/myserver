import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { controlVisibility, deferred, fail, mockFetch, ok, restoreVisibility } from '@/test/helpers'
import { useAction, useQuery, type QueryOptions } from './useApi'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  restoreVisibility()
})

/** Runs `step`, then lets pending responses arrive. Used where setInterval is
 *  faked, because waitFor polls with setInterval itself. */
async function settle(step?: () => void): Promise<void> {
  await act(async () => {
    step?.()
    await new Promise((resolve) => setTimeout(resolve, 20))
  })
}

describe('useQuery', () => {
  it('yüklenirken loading true olur, yanıt gelince veriyi verir', async () => {
    const res = deferred<Response>()
    mockFetch({ 'GET /disks': () => res.promise })
    const { result } = renderHook(() => useQuery<string[]>('/disks'))
    expect(result.current).toMatchObject({ loading: true, data: undefined, error: null })
    await waitFor(() => expect(result.current.fetching).toBe(true))

    await act(async () => res.resolve(ok(['sda'])))
    expect(result.current).toMatchObject({ loading: false, fetching: false, data: ['sda'], error: null })
  })

  it('hata yanıtında sunucunun Türkçe mesajını verir', async () => {
    mockFetch({ 'GET /disks': fail(502, 'docker_down', 'Docker servisine ulaşılamıyor.') })
    const { result } = renderHook(() => useQuery('/disks'))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe('Docker servisine ulaşılamıyor.')
    expect(result.current.data).toBeUndefined()
  })

  it('sorgu parametrelerini gönderir ve değişince yeniden ister', async () => {
    const f = mockFetch({ 'GET /logs': (c) => ok({ level: c.query.get('level') }) })
    const { result, rerender } = renderHook((p: QueryOptions) => useQuery<{ level: string }>('/logs', p), {
      initialProps: { query: { level: 'ERROR', limit: 10 } },
    })
    await waitFor(() => expect(result.current.data).toEqual({ level: 'ERROR' }))
    expect(f.calls[0]!.query.get('limit')).toBe('10')

    // A new object with the same contents must not trigger a request.
    rerender({ query: { level: 'ERROR', limit: 10 } })
    rerender({ query: { level: 'WARN', limit: 10 } })
    await waitFor(() => expect(result.current.data).toEqual({ level: 'WARN' }))
    expect(f.calls).toHaveLength(2)
  })

  it('reload veriyi yeniler ve önceki hatayı temizler', async () => {
    let n = 0
    mockFetch({ 'GET /disks': () => (++n === 1 ? fail(500, 'x', 'Diskler okunamadı.') : ok(['sda', 'sdb'])) })
    const { result } = renderHook(() => useQuery<string[]>('/disks'))
    await waitFor(() => expect(result.current.error).toBe('Diskler okunamadı.'))

    await act(() => result.current.reload())
    expect(result.current).toMatchObject({ data: ['sda', 'sdb'], error: null, loading: false, fetching: false })
  })

  it('reload başarısız olursa eski veriyi korur ve hatayı bildirir', async () => {
    let n = 0
    mockFetch({ 'GET /disks': () => (++n === 1 ? ok(['sda']) : fail(503, 'x', 'Sunucu şu anda yanıt veremiyor.')) })
    const { result } = renderHook(() => useQuery<string[]>('/disks'))
    await waitFor(() => expect(result.current.data).toEqual(['sda']))
    await act(() => result.current.reload())
    expect(result.current.data).toEqual(['sda'])
    expect(result.current.error).toBe('Sunucu şu anda yanıt veremiyor.')
  })

  it('reload sırasında loading değil fetching true olur', async () => {
    const second = deferred<Response>()
    let n = 0
    mockFetch({ 'GET /disks': () => (++n === 1 ? ok(['sda']) : second.promise) })
    const { result } = renderHook(() => useQuery<string[]>('/disks'))
    await waitFor(() => expect(result.current.loading).toBe(false))
    let done!: Promise<void>
    act(() => {
      done = result.current.reload()
    })
    expect(result.current).toMatchObject({ loading: false, fetching: true })
    await act(async () => {
      second.resolve(ok(['sdb']))
      await done
    })
    expect(result.current).toMatchObject({ fetching: false, data: ['sdb'] })
  })

  it('yol değişince yeni yolu ister ve eski isteği iptal eder', async () => {
    const f = mockFetch({ 'GET /containers/a': ok({ id: 'a' }), 'GET /containers/b': ok({ id: 'b' }) })
    const { result, rerender } = renderHook((path: string) => useQuery<{ id: string }>(path), { initialProps: '/containers/a' })
    await waitFor(() => expect(result.current.data).toEqual({ id: 'a' }))
    rerender('/containers/b')
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.data).toEqual({ id: 'b' }))
    expect(f.calls.map((c) => c.path)).toEqual(['/containers/a', '/containers/b'])
  })

  it('yol değiştiğinde geç gelen eski yanıtı yok sayar', async () => {
    const a = deferred<Response>()
    const b = deferred<Response>()
    const f = mockFetch({ 'GET /containers/a': () => a.promise, 'GET /containers/b': () => b.promise })
    const { result, rerender } = renderHook((path: string) => useQuery<{ id: string }>(path), { initialProps: '/containers/a' })
    await waitFor(() => expect(f.calls).toHaveLength(1))
    rerender('/containers/b')
    await waitFor(() => expect(f.calls).toHaveLength(2))
    expect((f.calls[0]!.init.signal as AbortSignal).aborted).toBe(true)

    await act(async () => b.resolve(ok({ id: 'b' })))
    expect(result.current.data).toEqual({ id: 'b' })
    // The server answers the first request anyway, after the newer one.
    await act(async () => a.resolve(ok({ id: 'a' })))
    expect(result.current).toMatchObject({ data: { id: 'b' }, error: null, loading: false, fetching: false })
  })

  it('art arda iki yenilemede geç gelen eski yanıtı ve eski hatayı yok sayar', async () => {
    const slow = deferred<Response>()
    const slowError = deferred<Response>()
    const queue = [() => Promise.resolve(ok('ilk')), () => slow.promise, () => slowError.promise, () => Promise.resolve(ok('en yeni'))]
    mockFetch({ 'GET /x': () => queue.shift()!() })
    const { result } = renderHook(() => useQuery<string>('/x'))
    await waitFor(() => expect(result.current.data).toBe('ilk'))

    await act(async () => {
      void result.current.reload()
      void result.current.reload()
      await result.current.reload()
    })
    expect(result.current.data).toBe('en yeni')

    await act(async () => {
      slow.resolve(ok('eski'))
      slowError.resolve(fail(500, 'x', 'Eski hata.'))
    })
    expect(result.current).toMatchObject({ data: 'en yeni', error: null, fetching: false })
  })

  it('enabled false iken istek göndermez ve yükleniyor görünmez', async () => {
    const f = mockFetch({ 'GET /x': ok(1) })
    const { result, rerender } = renderHook((enabled: boolean) => useQuery<number>('/x', { enabled }), { initialProps: false })
    await waitFor(() => expect(result.current.loading).toBe(false))
    await act(() => result.current.reload())
    expect(f.calls).toHaveLength(0)
    expect(result.current.data).toBeUndefined()

    rerender(true)
    await waitFor(() => expect(result.current.data).toBe(1))
    expect(f.calls).toHaveLength(1)
  })

  it('yol null iken istek göndermez', async () => {
    const f = mockFetch({})
    const { result } = renderHook(() => useQuery(null))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(f.calls).toHaveLength(0)
  })

  it('bileşen kaldırılınca isteği iptal eder', async () => {
    const res = deferred<Response>()
    const f = mockFetch({ 'GET /x': () => res.promise })
    const { unmount } = renderHook(() => useQuery('/x'))
    await waitFor(() => expect(f.calls).toHaveLength(1))
    unmount()
    expect((f.calls[0]!.init.signal as AbortSignal).aborted).toBe(true)
  })

  it('iptal edilen istek hata olarak gösterilmez', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((_url: string, init: RequestInit) => {
        return new Promise((_resolve, reject) => {
          init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
        })
      }),
    )
    const { result, rerender } = renderHook((enabled: boolean) => useQuery('/x', { enabled }), { initialProps: true })
    rerender(false)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBeNull()
  })

  describe('refetchInterval', () => {
    it('belirtilen aralıkla yeniler, sekme gizliyken durur, görünür olunca sürer', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const setHidden = controlVisibility()
      let n = 0
      const f = mockFetch({ 'GET /health': () => ok(++n) })
      const { result, unmount } = renderHook(() => useQuery<number>('/health', { refetchInterval: 60_000 }))
      await settle()
      expect(result.current.data).toBe(1)

      await settle(() => vi.advanceTimersByTime(59_999))
      expect(f.calls).toHaveLength(1)
      await settle(() => vi.advanceTimersByTime(1))
      expect(result.current.data).toBe(2)

      act(() => setHidden(true))
      await settle(() => vi.advanceTimersByTime(5 * 60_000))
      expect(f.calls).toHaveLength(2)

      act(() => setHidden(false))
      await settle(() => vi.advanceTimersByTime(60_000))
      expect(result.current.data).toBe(3)

      unmount()
      await settle(() => vi.advanceTimersByTime(5 * 60_000))
      expect(f.calls).toHaveLength(3)
    })

    it('enabled false iken zamanlayıcı istek göndermez', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const f = mockFetch({ 'GET /health': ok(1) })
      renderHook(() => useQuery('/health', { refetchInterval: 1000, enabled: false }))
      await settle(() => vi.advanceTimersByTime(10_000))
      expect(f.calls).toHaveLength(0)
    })
  })
})

describe('useAction', () => {
  it('çalışırken pending true olur, sonucu döndürür ve onSuccess çağrılır', async () => {
    const work = deferred<string>()
    const onSuccess = vi.fn()
    const fn = vi.fn((_name: string, _force: boolean) => work.promise)
    const { result } = renderHook(() => useAction(fn, { onSuccess }))
    expect(result.current).toMatchObject({ pending: false, error: null })

    let done!: Promise<string | undefined>
    act(() => {
      done = result.current.run('nginx', true)
    })
    expect(result.current.pending).toBe(true)
    expect(fn).toHaveBeenCalledWith('nginx', true)

    await act(async () => work.resolve('tamam'))
    await expect(done).resolves.toBe('tamam')
    expect(result.current).toMatchObject({ pending: false, error: null })
    expect(onSuccess).toHaveBeenCalledWith('tamam')
  })

  it('hata durumunda undefined döndürür, mesajı error alanına yazar, fırlatmaz', async () => {
    mockFetch({ 'POST /docker/restart': fail(502, 'docker_down', 'Docker servisine ulaşılamıyor.') })
    const { api } = await import('@/services/api')
    const onError = vi.fn()
    const onSuccess = vi.fn()
    const { result } = renderHook(() => useAction(() => api.post('/docker/restart'), { onError, onSuccess }))
    let value: unknown = 'değişmedi'
    await act(async () => {
      value = await result.current.run()
    })
    expect(value).toBeUndefined()
    expect(result.current).toMatchObject({ pending: false, error: 'Docker servisine ulaşılamıyor.' })
    expect(onError).toHaveBeenCalledWith('Docker servisine ulaşılamıyor.')
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it('mesajı olmayan hatada genel Türkçe mesaj kullanır', async () => {
    const { result } = renderHook(() => useAction(() => Promise.reject('dizgi')))
    await act(async () => {
      await result.current.run()
    })
    expect(result.current.error).toBe('İşlem tamamlanamadı.')
  })

  it('yeni deneme önceki hatayı temizler; clearError hatayı siler', async () => {
    const fn = vi.fn().mockRejectedValueOnce(new Error('Olmadı.')).mockResolvedValueOnce(1).mockRejectedValueOnce(new Error('Yine olmadı.'))
    const { result } = renderHook(() => useAction(fn))
    await act(async () => {
      await result.current.run()
    })
    expect(result.current.error).toBe('Olmadı.')
    await act(async () => {
      await result.current.run()
    })
    expect(result.current.error).toBeNull()
    await act(async () => {
      await result.current.run()
    })
    expect(result.current.error).toBe('Yine olmadı.')
    act(() => result.current.clearError())
    expect(result.current.error).toBeNull()
  })

  it('her zaman en güncel işlevi çağırır', async () => {
    const { result, rerender } = renderHook((id: number) => useAction(() => Promise.resolve(id)), { initialProps: 1 })
    rerender(2)
    let value: number | undefined
    await act(async () => {
      value = await result.current.run()
    })
    expect(value).toBe(2)
  })
})
