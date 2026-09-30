import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/services/api'
import { fail, makeStatus, makeUser, mockFetch, ok, signedIn } from '@/test/helpers'
import { useAuth } from './auth'

beforeEach(() => {
  useAuth.setState({ status: null, loadError: null, user: null, isAdmin: false })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** The CSRF token the client currently sends, observed on a real request. */
async function sentCsrf(f: ReturnType<typeof mockFetch>): Promise<string | undefined> {
  await api.post('/probe').catch(() => undefined)
  return f.find('POST /probe').at(-1)!.headers['X-CSRF-Token']
}

describe('load', () => {
  it('durumu, kullanıcıyı ve CSRF belirtecini yükler', async () => {
    const f = mockFetch({ 'GET /auth/status': ok(signedIn()), 'POST /probe': ok(null) })
    await useAuth.getState().load()
    const s = useAuth.getState()
    expect(s.status?.authenticated).toBe(true)
    expect(s.user?.username).toBe('abdullah')
    expect(s.isAdmin).toBe(true)
    expect(s.loadError).toBeNull()
    expect(await sentCsrf(f)).toBe('csrf-session')
  })

  it('yönetici olmayan kullanıcıda isAdmin false olur', async () => {
    mockFetch({ 'GET /auth/status': ok(signedIn({ role: 'user' })) })
    await useAuth.getState().load()
    expect(useAuth.getState().isAdmin).toBe(false)
  })

  it('istek başarısız olursa loadError ayarlanır ve durum boş kalır', async () => {
    mockFetch({ 'GET /auth/status': fail(503, 'unavailable', 'Sunucu şu anda yanıt veremiyor.') })
    await useAuth.getState().load()
    expect(useAuth.getState().loadError).toBe('Sunucu şu anda yanıt veremiyor.')
    expect(useAuth.getState().status).toBeNull()
  })

  it('ağ hatasında Türkçe loadError ayarlanır', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    await useAuth.getState().load()
    expect(useAuth.getState().loadError).toBe('Sunucuya ulaşılamıyor. Ağ bağlantınızı kontrol edin.')
  })

  it('başarılı yeniden deneme loadError değerini temizler', async () => {
    useAuth.setState({ loadError: 'Sunucuya ulaşılamıyor.' })
    mockFetch({ 'GET /auth/status': ok(makeStatus()) })
    await useAuth.getState().load()
    expect(useAuth.getState().loadError).toBeNull()
    expect(useAuth.getState().status).not.toBeNull()
  })
})

describe('login', () => {
  it('kimlik bilgilerini gönderir, durumu ve CSRF belirtecini günceller', async () => {
    useAuth.setState({ status: makeStatus() })
    const f = mockFetch({ 'POST /auth/login': ok(signedIn()), 'POST /probe': ok(null) })
    await useAuth.getState().login('abdullah', 'çok-gizli-parola')
    expect(f.find('POST /auth/login')[0]!.body).toEqual({ username: 'abdullah', password: 'çok-gizli-parola' })
    expect(useAuth.getState().status?.authenticated).toBe(true)
    expect(useAuth.getState().user?.id).toBe(1)
    expect(await sentCsrf(f)).toBe('csrf-session')
  })

  it('hatalı parolada hata fırlatır ve durumu değiştirmez', async () => {
    const before = makeStatus()
    useAuth.setState({ status: before })
    mockFetch({ 'POST /auth/login': fail(401, 'invalid_credentials', 'Kullanıcı adı veya parola hatalı.') })
    await expect(useAuth.getState().login('abdullah', 'yanlış')).rejects.toThrow('Kullanıcı adı veya parola hatalı.')
    expect(useAuth.getState().status).toBe(before)
    expect(useAuth.getState().user).toBeNull()
  })
})

describe('logout', () => {
  it('oturumu kapatır, kullanıcıyı ve CSRF belirtecini temizler', async () => {
    const f = mockFetch({ 'GET /auth/status': ok(signedIn()), 'POST /auth/logout': ok(null), 'POST /probe': ok(null) })
    await useAuth.getState().load()
    await useAuth.getState().logout()
    expect(f.find('POST /auth/logout')[0]!.headers['X-CSRF-Token']).toBe('csrf-session')
    const s = useAuth.getState()
    expect(s.status?.authenticated).toBe(false)
    expect(s.status?.setup_complete).toBe(true)
    expect(s.user).toBeNull()
    expect(s.isAdmin).toBe(false)
    expect(await sentCsrf(f)).toBeUndefined()
  })

  it('sunucu hata verse bile yerel oturumu kapatır', async () => {
    useAuth.setState({ status: signedIn(), user: makeUser(), isAdmin: true })
    mockFetch({ 'POST /auth/logout': fail(500, 'internal', 'Beklenmeyen hata.') })
    await expect(useAuth.getState().logout()).rejects.toThrow()
    expect(useAuth.getState().status?.authenticated).toBe(false)
    expect(useAuth.getState().user).toBeNull()
  })
})

describe('setup', () => {
  it('girilen değerleri gönderir, uyarıları döndürür ve oturumu açar', async () => {
    useAuth.setState({ status: makeStatus({ setup_complete: false }) })
    const result = { ...signedIn(), warnings: ['Saat dilimi uygulanamadı.'] }
    const f = mockFetch({ 'POST /auth/setup': ok(result), 'POST /probe': ok(null) })
    const input = { username: 'abdullah', password: 'uzun-bir-parola', hostname: 'evsunucu', timezone: 'Europe/Istanbul' }
    const warnings = await useAuth.getState().setup(input)
    expect(warnings).toEqual(['Saat dilimi uygulanamadı.'])
    expect(f.find('POST /auth/setup')[0]!.body).toEqual(input)
    const s = useAuth.getState()
    expect(s.status?.setup_complete).toBe(true)
    expect(s.status).not.toHaveProperty('warnings')
    expect(s.isAdmin).toBe(true)
    expect(await sentCsrf(f)).toBe('csrf-session')
  })

  it('hata durumunda fırlatır ve kurulum durumunu değiştirmez', async () => {
    const before = makeStatus({ setup_complete: false })
    useAuth.setState({ status: before })
    mockFetch({ 'POST /auth/setup': fail(409, 'setup_done', 'Kurulum zaten tamamlanmış.') })
    await expect(useAuth.getState().setup({ username: 'a', password: 'b', hostname: 'c', timezone: 'd' })).rejects.toThrow('Kurulum zaten tamamlanmış.')
    expect(useAuth.getState().status).toBe(before)
  })
})

describe('oturumun düşmesi', () => {
  it('oturum açıkken gelen "unauthorized" yanıtı giriş sayfasına döndürür', async () => {
    const f = mockFetch({
      'GET /auth/status': ok(signedIn()),
      'GET /storage/disks': fail(401, 'unauthorized', 'Oturum açmanız gerekiyor.'),
      'POST /probe': ok(null),
    })
    await useAuth.getState().load()
    await expect(api.get('/storage/disks')).rejects.toThrow('Oturum açmanız gerekiyor.')
    const s = useAuth.getState()
    expect(s.status?.authenticated).toBe(false)
    expect(s.status?.setup_complete).toBe(true)
    expect(s.user).toBeNull()
    expect(s.isAdmin).toBe(false)
    expect(await sentCsrf(f)).toBeUndefined()
  })

  it('başka hatalar oturumu kapatmaz', async () => {
    mockFetch({
      'GET /auth/status': ok(signedIn()),
      'GET /a': fail(403, 'forbidden', 'Yetkiniz bulunmuyor.'),
      'GET /b': fail(500, 'internal', 'Hata.'),
      'GET /c': fail(401, 'invalid_credentials', 'Parola hatalı.'),
    })
    await useAuth.getState().load()
    for (const p of ['/a', '/b', '/c']) await api.get(p).catch(() => undefined)
    expect(useAuth.getState().status?.authenticated).toBe(true)
    expect(useAuth.getState().user).not.toBeNull()
  })

  it('durum henüz yüklenmemişken hata vermez', async () => {
    mockFetch({ 'GET /x': fail(401, 'unauthorized', 'Oturum açmanız gerekiyor.') })
    await expect(api.get('/x')).rejects.toThrow()
    expect(useAuth.getState().status).toBeNull()
  })
})
