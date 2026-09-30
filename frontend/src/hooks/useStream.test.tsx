import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { controlVisibility, FakeEventSource, restoreVisibility } from '@/test/helpers'
import { useEventSource, useSocket, type EventSourceOptions } from './useStream'

beforeEach(() => FakeEventSource.install())

afterEach(() => {
  vi.unstubAllGlobals()
  restoreVisibility()
})

const latest = () => FakeEventSource.instances.at(-1)!

describe('useEventSource', () => {
  it('API yoluna çerezlerle bağlanır ve durumunu bildirir', () => {
    const { result } = renderHook(() => useEventSource('/system/stream', () => undefined, { query: { interval: 2, skip: undefined } }))
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(latest().url).toBe('/api/v1/system/stream?interval=2')
    expect(latest().init).toEqual({ withCredentials: true })
    expect(result.current).toBe('connecting')
    act(() => latest().emitOpen())
    expect(result.current).toBe('open')
  })

  it('JSON olayları ayrıştırır', () => {
    const onEvent = vi.fn()
    renderHook(() => useEventSource('/system/stream', onEvent))
    act(() => {
      latest().emit('message', '{"cpu":12.5,"cores":[1,2]}')
      latest().emit('message', '42')
      latest().emit('message', 'null')
    })
    expect(onEvent.mock.calls).toEqual([
      ['message', { cpu: 12.5, cores: [1, 2] }],
      ['message', 42],
      ['message', null],
    ])
  })

  it('düz metni (günlük satırı) olduğu gibi iletir', () => {
    const onEvent = vi.fn()
    renderHook(() => useEventSource('/logs/stream', onEvent))
    act(() => {
      latest().emit('message', 'Sep 29 01:45 sshd[812]: Accepted publickey')
      latest().emit('message', '{yarım json')
      latest().emit('message', '')
    })
    expect(onEvent.mock.calls).toEqual([
      ['message', 'Sep 29 01:45 sshd[812]: Accepted publickey'],
      ['message', '{yarım json'],
      ['message', ''],
    ])
  })

  it('yalnızca istenen adlandırılmış olayları dinler ve adını iletir', () => {
    const onEvent = vi.fn()
    renderHook(() => useEventSource('/notifications/stream', onEvent, { events: ['notification', 'health'] }))
    act(() => {
      latest().emit('notification', '{"id":7}')
      latest().emit('health', '"WARNING"')
      latest().emit('message', '{"id":8}')
    })
    expect(onEvent.mock.calls).toEqual([
      ['notification', { id: 7 }],
      ['health', 'WARNING'],
    ])
  })

  it('yeniden bağlanmadan en güncel işleyiciyi kullanır', () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender } = renderHook((fn: (e: string, d: unknown) => void) => useEventSource('/s', fn), { initialProps: first })
    rerender(second)
    act(() => latest().emit('message', '1'))
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledWith('message', 1)
    expect(FakeEventSource.instances).toHaveLength(1)
  })

  it('sekme gizlenince kapanır, görünür olunca yeniden açılır', () => {
    const setHidden = controlVisibility()
    const onEvent = vi.fn()
    const { result } = renderHook(() => useEventSource('/system/stream', onEvent))
    const first = latest()
    act(() => first.emitOpen())

    act(() => setHidden(true))
    expect(first.closed).toBe(true)
    expect(result.current).toBe('closed')
    expect(FakeEventSource.open).toHaveLength(0)

    act(() => setHidden(false))
    expect(FakeEventSource.instances).toHaveLength(2)
    expect(FakeEventSource.open).toHaveLength(1)
    expect(result.current).toBe('connecting')
    act(() => {
      latest().emitOpen()
      latest().emit('message', '{"cpu":1}')
    })
    expect(result.current).toBe('open')
    expect(onEvent).toHaveBeenCalledWith('message', { cpu: 1 })
  })

  it('görünürlük olayı art arda gelse de tek bağlantı açık kalır', () => {
    const setHidden = controlVisibility()
    renderHook(() => useEventSource('/s', () => undefined))
    act(() => {
      setHidden(false)
      setHidden(false)
      setHidden(true)
      setHidden(false)
      setHidden(false)
    })
    expect(FakeEventSource.open).toHaveLength(1)
  })

  it('sekme gizliyken başlatılırsa görünür olana kadar bağlanmaz', () => {
    const setHidden = controlVisibility()
    setHidden(true)
    renderHook(() => useEventSource('/s', () => undefined))
    expect(FakeEventSource.instances).toHaveLength(0)
    act(() => setHidden(false))
    expect(FakeEventSource.open).toHaveLength(1)
  })

  it('bileşen kaldırılınca kapanır ve görünürlük olayını dinlemeyi bırakır', () => {
    const setHidden = controlVisibility()
    const { unmount } = renderHook(() => useEventSource('/s', () => undefined))
    unmount()
    expect(latest().closed).toBe(true)
    act(() => {
      setHidden(true)
      setHidden(false)
    })
    expect(FakeEventSource.instances).toHaveLength(1)
  })

  it('enabled false ya da yol null iken bağlanmaz; etkinleşince bağlanır', () => {
    const { result, rerender } = renderHook((p: { path: string | null; opts: EventSourceOptions }) => useEventSource(p.path, () => undefined, p.opts), {
      initialProps: { path: '/s' as string | null, opts: { enabled: false } },
    })
    expect(FakeEventSource.instances).toHaveLength(0)
    expect(result.current).toBe('closed')
    rerender({ path: null, opts: { enabled: true } })
    expect(FakeEventSource.instances).toHaveLength(0)
    rerender({ path: '/s', opts: { enabled: true } })
    expect(FakeEventSource.open).toHaveLength(1)
  })

  it('yol değişince eski bağlantıyı kapatıp yenisini açar', () => {
    const { rerender } = renderHook((path: string) => useEventSource(path, () => undefined), { initialProps: '/docker/containers/a/logs' })
    const first = latest()
    rerender('/docker/containers/b/logs')
    expect(first.closed).toBe(true)
    expect(latest().url).toBe('/api/v1/docker/containers/b/logs')
    expect(FakeEventSource.open).toHaveLength(1)
  })

  it('bağlantı hatasında yeniden bağlanırken "connecting", kalıcı kapanışta "closed" bildirir', () => {
    const { result } = renderHook(() => useEventSource('/s', () => undefined))
    act(() => latest().emitOpen())
    act(() => {
      latest().readyState = FakeEventSource.CONNECTING
      latest().onerror?.()
    })
    expect(result.current).toBe('connecting')
    act(() => {
      latest().readyState = FakeEventSource.CLOSED
      latest().onerror?.()
    })
    expect(result.current).toBe('closed')
  })
})

class FakeSocket {
  static readonly OPEN = 1
  static instances: FakeSocket[] = []
  readyState = 0
  binaryType = ''
  sent: unknown[] = []
  closed = false
  onopen: (() => void) | null = null
  onmessage: ((e: { data: unknown }) => void) | null = null
  onclose: ((e: unknown) => void) | null = null
  constructor(readonly url: string) {
    FakeSocket.instances.push(this)
  }
  send(data: unknown) {
    this.sent.push(data)
  }
  close() {
    this.closed = true
    this.readyState = 3
  }
}

describe('useSocket', () => {
  beforeEach(() => {
    FakeSocket.instances = []
    vi.stubGlobal('WebSocket', FakeSocket)
  })

  it('bağlanır, mesajları iletir, yalnızca açıkken gönderir ve kaldırılınca kapanır', () => {
    const onMessage = vi.fn()
    const onClose = vi.fn()
    const { result, unmount } = renderHook(() => useSocket('/terminal/ws', { query: { cols: 80 }, onMessage, onClose }))
    const ws = FakeSocket.instances[0]!
    expect(ws.url).toBe(`ws://${window.location.host}/api/v1/terminal/ws?cols=80`)
    expect(ws.binaryType).toBe('arraybuffer')
    expect(result.current.state).toBe('connecting')

    act(() => result.current.send('erken'))
    expect(ws.sent).toEqual([])

    act(() => {
      ws.readyState = FakeSocket.OPEN
      ws.onopen?.()
    })
    expect(result.current.state).toBe('open')
    act(() => result.current.send('ls\n'))
    expect(ws.sent).toEqual(['ls\n'])

    act(() => ws.onmessage?.({ data: 'çıktı' }))
    expect(onMessage).toHaveBeenCalledWith('çıktı')

    unmount()
    expect(ws.closed).toBe(true)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('sunucu kapattığında durumu bildirir ve yeniden bağlanmaz', () => {
    const onClose = vi.fn()
    const { result } = renderHook(() => useSocket('/terminal/ws', { onMessage: () => undefined, onClose }))
    const ws = FakeSocket.instances[0]!
    act(() => ws.onclose?.({ code: 1000 }))
    expect(result.current.state).toBe('closed')
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(FakeSocket.instances).toHaveLength(1)
  })
})
