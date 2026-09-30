import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, apiUrl, errorMessage, setCsrfToken, setUnauthorizedHandler, wsUrl } from './api'
import { fail, mockFetch, ok } from '@/test/helpers'

const unauthorized = vi.fn()

beforeEach(() => {
  unauthorized.mockReset()
  setUnauthorizedHandler(unauthorized)
  setCsrfToken('tok-123')
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function caught(p: Promise<unknown>): Promise<unknown> {
  try {
    await p
  } catch (e) {
    return e
  }
  throw new Error('beklenen hata oluşmadı')
}

describe('api istekleri', () => {
  it('başarılı yanıtta data alanını döndürür', async () => {
    mockFetch({ 'GET /storage/disks': ok([{ name: 'sda' }]) })
    await expect(api.get('/storage/disks')).resolves.toEqual([{ name: 'sda' }])
  })

  it('hata yanıtında sunucunun mesajı ve koduyla ApiError fırlatır', async () => {
    mockFetch({ 'POST /storage/mount': fail(502, 'mount_failed', 'Disk bağlanamadı.') })
    const e = await caught(api.post('/storage/mount', { device: 'sdb1' }))
    expect(e).toBeInstanceOf(ApiError)
    expect(e).toMatchObject({ message: 'Disk bağlanamadı.', code: 'mount_failed', status: 502 })
  })

  it('HTTP 200 olsa bile success:false ise hata fırlatır', async () => {
    mockFetch({
      'GET /x': new Response(JSON.stringify({ success: false, data: null, error: { code: 'bad', message: 'Olmadı.' } }), { status: 200 }),
    })
    expect(await caught(api.get('/x'))).toMatchObject({ message: 'Olmadı.', code: 'bad' })
  })

  it.each([
    [401, 'Oturum açmanız gerekiyor.'],
    [403, 'Yetkiniz bulunmuyor.'],
    [404, 'İstenen kayıt bulunamadı.'],
    [413, 'Gönderilen veri çok büyük.'],
    [429, 'Çok fazla istek gönderildi. Lütfen biraz bekleyin.'],
    [502, 'Sunucu şu anda yanıt veremiyor.'],
    [500, 'Sunucu beklenmeyen bir yanıt verdi (500).'],
  ])('gövde JSON değilse %i için Türkçe mesaj üretir', async (status, message) => {
    mockFetch({ 'GET /x': new Response('<html>Bad Gateway</html>', { status }) })
    const e = await caught(api.get('/x'))
    expect(e).toBeInstanceOf(ApiError)
    expect(e).toMatchObject({ message, code: 'http_' + status, status })
  })

  it('JSON olmayan 200 yanıtını başarı saymaz', async () => {
    mockFetch({ 'GET /x': new Response('tamam', { status: 200 }) })
    expect(await caught(api.get('/x'))).toBeInstanceOf(ApiError)
  })

  it('ağ hatasında Türkçe mesajlı ApiError fırlatır', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const e = await caught(api.get('/x'))
    expect(e).toBeInstanceOf(ApiError)
    expect(e).toMatchObject({ message: 'Sunucuya ulaşılamıyor. Ağ bağlantınızı kontrol edin.', code: 'network_error', status: 0 })
  })

  it('AbortError ApiError yapılmadan iletilir', async () => {
    const abort = new DOMException('aborted', 'AbortError')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(abort))
    const e = await caught(api.get('/x'))
    expect(e).toBe(abort)
    expect(e).not.toBeInstanceOf(ApiError)
  })

  it('iptal sinyalini fetch çağrısına iletir', async () => {
    const f = mockFetch({ 'GET /x': ok(1) })
    const ctrl = new AbortController()
    await api.get('/x', { signal: ctrl.signal })
    expect(f.calls[0]!.init.signal).toBe(ctrl.signal)
  })
})

describe('CSRF ve çerezler', () => {
  it.each(['post', 'put', 'del'] as const)('%s isteğinde X-CSRF-Token gönderir', async (method) => {
    const verb = method === 'del' ? 'DELETE' : method.toUpperCase()
    const f = mockFetch({ [`${verb} /x`]: ok(null) })
    await api[method]('/x', { a: 1 })
    expect(f.calls[0]!.headers['X-CSRF-Token']).toBe('tok-123')
    expect(f.calls[0]!.method).toBe(verb)
  })

  it('GET isteğinde X-CSRF-Token göndermez', async () => {
    const f = mockFetch({ 'GET /x': ok(null) })
    await api.get('/x')
    expect(f.calls[0]!.headers).not.toHaveProperty('X-CSRF-Token')
  })

  it('çerezleri same-origin olarak gönderir', async () => {
    const f = mockFetch({ 'GET /x': ok(null), 'POST /x': ok(null) })
    await api.get('/x')
    await api.post('/x')
    expect(f.calls.map((c) => c.init.credentials)).toEqual(['same-origin', 'same-origin'])
  })

  it('gövdeyi JSON olarak ve doğru Content-Type ile gönderir', async () => {
    const f = mockFetch({ 'POST /x': ok(null) })
    await api.post('/x', { device: 'sdb1', force: false })
    expect(f.calls[0]!.headers['Content-Type']).toBe('application/json')
    expect(f.calls[0]!.body).toEqual({ device: 'sdb1', force: false })
  })

  it('gövdesiz POST isteğinde Content-Type ve gövde göndermez', async () => {
    const f = mockFetch({ 'POST /auth/logout': ok(null) })
    await api.post('/auth/logout')
    expect(f.calls[0]!.headers).not.toHaveProperty('Content-Type')
    expect(f.calls[0]!.init.body).toBeUndefined()
  })

  it('FormData gövdesinde Content-Type başlığını tarayıcıya bırakır', async () => {
    const f = mockFetch({ 'POST /x': ok(null) })
    const form = new FormData()
    form.append('a', 'b')
    await api.post('/x', form)
    expect(f.calls[0]!.headers).not.toHaveProperty('Content-Type')
    expect(f.calls[0]!.init.body).toBe(form)
  })
})

describe('sorgu dizgileri', () => {
  it('parametreleri ekler ve undefined değerleri atlar', () => {
    expect(apiUrl('/logs', { limit: 50, level: undefined, follow: false, q: 'a b&c' })).toBe('/api/v1/logs?limit=50&follow=false&q=a+b%26c')
  })

  it('parametre yoksa ya da hepsi undefined ise soru işareti eklemez', () => {
    expect(apiUrl('/logs')).toBe('/api/v1/logs')
    expect(apiUrl('/logs', { a: undefined })).toBe('/api/v1/logs')
  })

  it('0 ve boş dizgi değerlerini korur', () => {
    expect(apiUrl('/x', { offset: 0, q: '' })).toBe('/api/v1/x?offset=0&q=')
  })

  it('istekte sorguyu URL’ye yazar', async () => {
    const f = mockFetch({ 'GET /notifications': ok([]) })
    await api.get('/notifications', { query: { limit: 30, unread: undefined } })
    expect(String(f.fn.mock.calls[0]![0])).toBe('/api/v1/notifications?limit=30')
  })

  it('wsUrl sayfanın protokolüne uygun adres üretir', () => {
    expect(wsUrl('/terminal/ws', { cols: 80 })).toBe(`ws://${window.location.host}/api/v1/terminal/ws?cols=80`)
  })
})

describe('oturum düşmesi', () => {
  it('401 + "unauthorized" kodunda işleyiciyi çağırır', async () => {
    mockFetch({ 'GET /x': fail(401, 'unauthorized', 'Oturum açmanız gerekiyor.') })
    await caught(api.get('/x'))
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })

  it('401 başka bir kodla geldiğinde (hatalı parola) işleyiciyi çağırmaz', async () => {
    mockFetch({ 'POST /auth/login': fail(401, 'invalid_credentials', 'Kullanıcı adı veya parola hatalı.') })
    await caught(api.post('/auth/login', {}))
    expect(unauthorized).not.toHaveBeenCalled()
  })

  it.each([
    [403, 'forbidden'],
    [404, 'not_found'],
    [500, 'internal'],
  ])('%i hatasında işleyiciyi çağırmaz', async (status, code) => {
    mockFetch({ 'GET /x': fail(status, code, 'Hata.') })
    await caught(api.get('/x'))
    expect(unauthorized).not.toHaveBeenCalled()
  })

  it('ağ hatasında işleyiciyi çağırmaz', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('x')))
    await caught(api.get('/x'))
    expect(unauthorized).not.toHaveBeenCalled()
  })
})

class FakeXHR {
  static last: FakeXHR
  method = ''
  url = ''
  withCredentials = false
  headers: Record<string, string> = {}
  status = 0
  responseText = ''
  body: unknown
  aborted = false
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null }
  onerror: (() => void) | null = null
  onabort: (() => void) | null = null
  onload: (() => void) | null = null

  constructor() {
    FakeXHR.last = this
  }
  open(method: string, url: string) {
    this.method = method
    this.url = url
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v
  }
  send(body: unknown) {
    this.body = body
  }
  abort() {
    this.aborted = true
    this.onabort?.()
  }
  respond(status: number, text: string) {
    this.status = status
    this.responseText = text
    this.onload?.()
  }
}

describe('api.upload', () => {
  beforeEach(() => vi.stubGlobal('XMLHttpRequest', FakeXHR))

  it('POST ile, CSRF başlığı ve sorguyla gönderir; ilerlemeyi bildirir; veriyi döndürür', async () => {
    const onProgress = vi.fn()
    const form = new FormData()
    const p = api.upload<{ name: string }>('/files/upload', form, { query: { path: '/srv' }, onProgress })
    const xhr = FakeXHR.last
    expect(xhr.method).toBe('POST')
    expect(xhr.url).toBe('/api/v1/files/upload?path=%2Fsrv')
    expect(xhr.headers['X-CSRF-Token']).toBe('tok-123')
    expect(xhr.body).toBe(form)

    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 10, total: 40 })
    xhr.upload.onprogress?.({ lengthComputable: false, loaded: 20, total: 0 })
    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 40, total: 40 })
    expect(onProgress.mock.calls).toEqual([
      [10, 40],
      [40, 40],
    ])

    xhr.respond(200, JSON.stringify({ success: true, data: { name: 'a.txt' }, error: null }))
    await expect(p).resolves.toEqual({ name: 'a.txt' })
  })

  it('sunucunun hata mesajıyla reddeder', async () => {
    const p = api.upload('/files/upload', new FormData())
    FakeXHR.last.respond(507, JSON.stringify({ success: false, data: null, error: { code: 'no_space', message: 'Diskte yeterli alan yok.' } }))
    const e = await caught(p)
    expect(e).toBeInstanceOf(ApiError)
    expect(e).toMatchObject({ message: 'Diskte yeterli alan yok.', code: 'no_space', status: 507 })
  })

  it('JSON olmayan hata gövdesinde Türkçe mesaj üretir', async () => {
    const p = api.upload('/files/upload', new FormData())
    FakeXHR.last.respond(413, '<html>too large</html>')
    expect(await caught(p)).toMatchObject({ message: 'Gönderilen veri çok büyük.', code: 'http_413' })
  })

  it('ağ hatasında Türkçe mesajla reddeder', async () => {
    const p = api.upload('/files/upload', new FormData())
    FakeXHR.last.onerror?.()
    expect(await caught(p)).toMatchObject({ code: 'network_error', message: 'Sunucuya ulaşılamıyor. Ağ bağlantınızı kontrol edin.' })
  })

  it('sinyal iptal edildiğinde isteği keser ve AbortError ile reddeder', async () => {
    const ctrl = new AbortController()
    const p = api.upload('/files/upload', new FormData(), { signal: ctrl.signal })
    ctrl.abort()
    const e = await caught(p)
    expect(FakeXHR.last.aborted).toBe(true)
    expect(e).toBeInstanceOf(DOMException)
    expect((e as DOMException).name).toBe('AbortError')
  })

  it('oturum işleyicisini yalnızca 401 + "unauthorized" için çağırır', async () => {
    const denied = api.upload('/files/upload', new FormData())
    FakeXHR.last.respond(401, JSON.stringify({ success: false, data: null, error: { code: 'csrf_invalid', message: 'Güvenlik belirteci geçersiz.' } }))
    await caught(denied)
    expect(unauthorized).not.toHaveBeenCalled()

    const gone = api.upload('/files/upload', new FormData())
    FakeXHR.last.respond(401, JSON.stringify({ success: false, data: null, error: { code: 'unauthorized', message: 'Oturum açmanız gerekiyor.' } }))
    await caught(gone)
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })
})

describe('errorMessage', () => {
  it('ApiError ve Error mesajını, diğerlerinde yedek metni döndürür', () => {
    expect(errorMessage(new ApiError('Disk bağlı değil.', 'x', 400))).toBe('Disk bağlı değil.')
    expect(errorMessage(new Error('boom'))).toBe('boom')
    expect(errorMessage('dizgi')).toBe('İşlem tamamlanamadı.')
    expect(errorMessage(null, 'Kaydedilemedi.')).toBe('Kaydedilemedi.')
    expect(errorMessage(new Error(''))).toBe('İşlem tamamlanamadı.')
  })
})
