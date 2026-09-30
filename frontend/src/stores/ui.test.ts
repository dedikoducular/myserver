import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The store reads localStorage while it is being imported, so each test
// imports a fresh copy.
async function freshStore() {
  vi.resetModules()
  return import('./ui')
}

beforeEach(() => {
  localStorage.clear()
  delete document.documentElement.dataset.theme
  document.head.querySelector('meta[name="theme-color"]')?.remove()
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('tema', () => {
  it('kayıt yoksa koyu temayla başlar ve data-theme yazar', async () => {
    const { useUI } = await freshStore()
    expect(useUI.getState().theme).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('kayıtlı açık temayı yükler', async () => {
    localStorage.setItem('myserver.theme', 'light')
    const { useUI } = await freshStore()
    expect(useUI.getState().theme).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('bozuk kayıtta koyu temaya düşer', async () => {
    localStorage.setItem('myserver.theme', 'mor')
    const { useUI } = await freshStore()
    expect(useUI.getState().theme).toBe('dark')
  })

  it('değiştirildiğinde data-theme yazar ve tercihi saklar', async () => {
    const { useUI } = await freshStore()
    useUI.getState().toggleTheme()
    expect(useUI.getState().theme).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem('myserver.theme')).toBe('light')
    useUI.getState().toggleTheme()
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('myserver.theme')).toBe('dark')
  })

  it('tarayıcının tema rengini günceller', async () => {
    const meta = document.createElement('meta')
    meta.name = 'theme-color'
    document.head.append(meta)
    const { useUI } = await freshStore()
    const dark = meta.content
    useUI.getState().setTheme('light')
    expect(meta.content).not.toBe(dark)
    expect(meta.content).not.toBe('')
  })

  it('localStorage kullanılamadığında da (okuma ve yazma hata verir) çalışır', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('denied', 'SecurityError')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('full', 'QuotaExceededError')
    })
    const { useUI } = await freshStore()
    expect(useUI.getState().theme).toBe('dark')
    expect(useUI.getState().sidebarCollapsed).toBe(false)

    expect(() => useUI.getState().toggleTheme()).not.toThrow()
    expect(useUI.getState().theme).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')

    expect(() => useUI.getState().toggleSidebar()).not.toThrow()
    expect(useUI.getState().sidebarCollapsed).toBe(true)
  })

  it('localStorage nesnesine erişim bile hata verdiğinde çalışır', async () => {
    const original = Object.getOwnPropertyDescriptor(window, 'localStorage')
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      get() {
        throw new DOMException('denied', 'SecurityError')
      },
    })
    try {
      const { useUI } = await freshStore()
      expect(() => useUI.getState().toggleTheme()).not.toThrow()
      expect(document.documentElement.dataset.theme).toBe('light')
    } finally {
      if (original) Object.defineProperty(window, 'localStorage', original)
    }
  })
})

describe('kenar çubuğu', () => {
  it('daraltma tercihini saklar ve yeniden yükler', async () => {
    const first = await freshStore()
    first.useUI.getState().toggleSidebar()
    expect(localStorage.getItem('myserver.sidebar')).toBe('collapsed')
    const second = await freshStore()
    expect(second.useUI.getState().sidebarCollapsed).toBe(true)
  })
})

describe('bildirim balonları', () => {
  it('eklenir, süre dolunca kalkar; hata mesajı daha uzun kalır', async () => {
    vi.useFakeTimers()
    const { useUI, toast } = await freshStore()
    toast.success('Kaydedildi.')
    toast.error('Disk bağlanamadı.')
    expect(useUI.getState().toasts.map((t) => [t.kind, t.message])).toEqual([
      ['success', 'Kaydedildi.'],
      ['error', 'Disk bağlanamadı.'],
    ])
    vi.advanceTimersByTime(4500)
    expect(useUI.getState().toasts.map((t) => t.message)).toEqual(['Disk bağlanamadı.'])
    vi.advanceTimersByTime(3500)
    expect(useUI.getState().toasts).toEqual([])
  })

  it('en fazla beş balon tutar ve en yenileri korur', async () => {
    vi.useFakeTimers()
    const { useUI, toast } = await freshStore()
    for (let i = 1; i <= 7; i++) toast.info('m' + i)
    expect(useUI.getState().toasts.map((t) => t.message)).toEqual(['m3', 'm4', 'm5', 'm6', 'm7'])
  })

  it('elle kapatılabilir', async () => {
    vi.useFakeTimers()
    const { useUI, toast } = await freshStore()
    toast.warning('Uyarı')
    const id = useUI.getState().toasts[0]!.id
    useUI.getState().dismissToast(id)
    expect(useUI.getState().toasts).toEqual([])
  })
})
