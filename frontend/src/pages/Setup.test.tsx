import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import App from '@/App'
import { expectAccessibleNames } from '@/test/a11y'
import { deferred, fail, FakeEventSource, makeStatus, mockFetch, ok, signedIn, type Routes } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import type { SetupChecks } from '@/types/api'
import SetupPage from './Setup'

const goodChecks: SetupChecks = {
  hostname: 'EvSunucu',
  timezone: 'Europe/Istanbul',
  storage: { ok: true, message: 'Veri dizini yazılabilir.', detail: '/var/lib/myserver · 120 GB boş' },
  docker: { ok: true, message: 'Docker çalışıyor.', detail: 'Sürüm 28.1.1' },
  helper: { ok: true, message: 'Yardımcı araç kullanılabilir.' },
}

const zones = ['Europe/Berlin', 'Europe/Istanbul', 'America/New_York', 'Asia/Tokyo']

function routes(over: Routes = {}, checks: SetupChecks = goodChecks): Routes {
  return {
    'GET /auth/setup/checks': ok(checks),
    'GET /auth/setup/timezones': ok(zones),
    'POST /auth/setup': ok({ ...signedIn({ username: 'abdullah' }), warnings: [] }),
    ...over,
  }
}

beforeEach(() => {
  useAuth.setState({ status: makeStatus({ setup_complete: false }), loadError: null, user: null, isAdmin: false })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const next = () => screen.getByRole('button', { name: 'İleri' })
const heading = (name: string) => screen.getByRole('heading', { name })
const usernameInput = () => screen.getByLabelText('Kullanıcı adı')
const passwordInput = () => screen.getByLabelText('Parola')
const repeatInput = () => screen.getByLabelText('Parola (tekrar)')

async function fillAdmin(user: UserEvent, name: string, password: string, repeat = password) {
  await user.clear(usernameInput())
  if (name) await user.type(usernameInput(), name)
  await user.clear(passwordInput())
  if (password) await user.type(passwordInput(), password)
  await user.clear(repeatInput())
  if (repeat) await user.type(repeatInput(), repeat)
}

/** Walks from the first step to the summary with valid values. */
async function toSummary(user: UserEvent, opts: { name?: string; password?: string; hostname?: string; zone?: string } = {}) {
  await fillAdmin(user, opts.name ?? 'abdullah', opts.password ?? 'uzun-ve-gizli-parola')
  await user.click(next())
  await screen.findByRole('heading', { name: 'Sunucu adını belirleyin' })
  if (opts.hostname !== undefined) {
    await user.clear(screen.getByLabelText('Sunucu adı'))
    await user.type(screen.getByLabelText('Sunucu adı'), opts.hostname)
  }
  await user.click(next())
  await screen.findByRole('heading', { name: 'Saat dilimini seçin' })
  if (opts.zone) await user.selectOptions(await screen.findByLabelText('Saat dilimi'), opts.zone)
  await user.click(next())
  await screen.findByRole('heading', { name: 'Depolama denetimi' })
  await user.click(next())
  await screen.findByRole('heading', { name: 'Docker denetimi' })
  await user.click(next())
  await screen.findByRole('heading', { name: 'Kurulumu tamamlayın' })
}

describe('Kurulum sihirbazı: yönetici hesabı', () => {
  it('alanların erişilebilir adı vardır ve hata görünmeden başlar', () => {
    mockFetch(routes())
    render(<SetupPage />)
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
    expectAccessibleNames()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(passwordInput()).toHaveAttribute('type', 'password')
    expect(repeatInput()).toHaveAttribute('type', 'password')
    expect(screen.getByRole('button', { name: 'Geri' })).toBeDisabled()
  })

  it.each([
    ['çok kısa', 'ab'],
    ['rakamla başlıyor', '1admin'],
    ['boşluk içeriyor', 'ad min'],
    ['nokta içeriyor', 'ad.min'],
    ['Türkçe karakter içeriyor', 'çağrı'],
    ['33 karakter', 'a'.repeat(33)],
    ['tire ile başlıyor', '-admin'],
  ])('geçersiz kullanıcı adıyla (%s) ilerlenemez', async (_name, value) => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, value, 'uzun-ve-gizli-parola')
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('Kullanıcı adı 3-32 karakter olmalı ve küçük harfle başlamalıdır.')
    expect(usernameInput()).toBeInvalid()
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
  })

  it('boş kullanıcı adıyla ilerlenemez', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, '', 'uzun-ve-gizli-parola')
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('Kullanıcı adı 3-32 karakter')
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
  })

  it.each(['root', 'ROOT', 'Root'])('"%s" kullanıcı adıyla ilerlenemez', async (value) => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, value, 'uzun-ve-gizli-parola')
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('"root" kullanıcı adı panelde kullanılamaz.')
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
  })

  it.each([
    ['boş', ''],
    ['9 karakter', '123456789'],
  ])('kısa parolayla (%s) ilerlenemez', async (_name, value) => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, 'abdullah', value)
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('Parola en az 10 karakter olmalıdır.')
    expect(passwordInput()).toBeInvalid()
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
  })

  it('tam 10 karakterlik parola kabul edilir', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, 'abdullah', '1234567890')
    await user.click(next())
    expect(await screen.findByRole('heading', { name: 'Sunucu adını belirleyin' })).toBeInTheDocument()
  })

  it.each([
    ['aynı', 'administrator', 'administrator'],
    ['yalnızca harf büyüklüğü farklı', 'administrator', 'ADMINistrator'],
  ])('kullanıcı adıyla %s parolayla ilerlenemez', async (_name, name, password) => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, name, password)
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('Parola kullanıcı adıyla aynı olamaz.')
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
  })

  it.each([
    ['farklı', 'uzun-ve-gizli-parola', 'uzun-ve-gizli-parolb'],
    ['harf büyüklüğü farklı', 'uzun-ve-gizli-parola', 'Uzun-ve-gizli-parola'],
    ['sonunda boşluk var', 'uzun-ve-gizli-parola', 'uzun-ve-gizli-parola '],
    ['tekrar boş', 'uzun-ve-gizli-parola', ''],
  ])('eşleşmeyen parolalarla (%s) ilerlenemez', async (_name, password, repeat) => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, 'abdullah', password, repeat)
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('Parolalar eşleşmiyor.')
    expect(repeatInput()).toBeInvalid()
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
  })

  it('Enter ile de geçersiz adım atlanamaz', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, 'root', 'kısa', 'başka')
    await user.type(repeatInput(), '{Enter}')
    expect(screen.getAllByRole('alert')).toHaveLength(3)
    expect(heading('Yönetici hesabı oluşturun')).toBeInTheDocument()
  })

  it('hatalar düzeltilince kalkar ve ilerlenir', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await fillAdmin(user, 'root', 'kısa')
    await user.click(next())
    expect(screen.getAllByRole('alert').length).toBeGreaterThan(0)
    await fillAdmin(user, 'abdullah', 'uzun-ve-gizli-parola')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await user.click(next())
    expect(await screen.findByRole('heading', { name: 'Sunucu adını belirleyin' })).toBeInTheDocument()
  })
})

describe('Kurulum sihirbazı: sunucu adı', () => {
  async function toNameStep(user: UserEvent) {
    await fillAdmin(user, 'abdullah', 'uzun-ve-gizli-parola')
    await user.click(next())
    return screen.findByLabelText('Sunucu adı')
  }

  it('sunucuda algılanan adı küçük harfle önerir', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    expect(await toNameStep(user)).toHaveValue('evsunucu')
    expectAccessibleNames()
  })

  it.each([
    ['boş', ''],
    ['tire ile başlıyor', '-sunucu'],
    ['tire ile bitiyor', 'sunucu-'],
    ['alt çizgi içeriyor', 'ev_sunucu'],
    ['nokta içeriyor', 'ev.sunucu'],
    ['boşluk içeriyor', 'ev sunucu'],
    ['Türkçe karakter içeriyor', 'evsunucuş'],
  ])('geçersiz sunucu adıyla (%s) ilerlenemez', async (_name, value) => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    const input = await toNameStep(user)
    await user.clear(input)
    if (value) await user.type(input, value)
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('Sunucu adı geçersiz.')
    expect(input).toBeInvalid()
    expect(heading('Sunucu adını belirleyin')).toBeInTheDocument()
  })

  it('63 karakterden uzun ad yazılamaz', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    const input = await toNameStep(user)
    await user.clear(input)
    await user.type(input, 'a'.repeat(70))
    expect((input as HTMLInputElement).value).toHaveLength(63)
  })

  it('denetimler yüklenemediyse boş adla ilerlenemez', async () => {
    const user = userEvent.setup()
    mockFetch(routes({ 'GET /auth/setup/checks': fail(500, 'internal', 'Denetimler çalıştırılamadı.') }))
    render(<SetupPage />)
    const input = await toNameStep(user)
    expect(input).toHaveValue('')
    await user.click(next())
    expect(screen.getByRole('alert')).toHaveTextContent('Sunucu adı geçersiz.')
  })

  it('Geri düğmesi girilen değerleri koruyarak önceki adıma döner', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await toNameStep(user)
    await user.click(screen.getByRole('button', { name: 'Geri' }))
    expect(usernameInput()).toHaveValue('abdullah')
    expect(passwordInput()).toHaveValue('uzun-ve-gizli-parola')
  })
})

describe('Kurulum sihirbazı: saat dilimi', () => {
  async function toTimeStep(user: UserEvent) {
    await fillAdmin(user, 'abdullah', 'uzun-ve-gizli-parola')
    await user.click(next())
    await screen.findByRole('heading', { name: 'Sunucu adını belirleyin' })
    await user.click(next())
    await screen.findByRole('heading', { name: 'Saat dilimini seçin' })
  }

  it('algılanan saat dilimi seçili gelir; arama listeyi daraltır', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await toTimeStep(user)
    const select = await screen.findByLabelText('Saat dilimi')
    expect(select).toHaveValue('Europe/Istanbul')
    expectAccessibleNames()

    await user.type(screen.getByLabelText('Saat dilimi ara'), 'new york')
    expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual(['America/New York'])
  })

  it('eşleşme yoksa bunu bildirir', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await toTimeStep(user)
    await screen.findByLabelText('Saat dilimi')
    await user.type(screen.getByLabelText('Saat dilimi ara'), 'zzz')
    expect(screen.getByRole('alert')).toHaveTextContent('Eşleşen saat dilimi yok')
  })

  it('liste alınamazsa hatayı ve yeniden deneme düğmesini gösterir', async () => {
    const user = userEvent.setup()
    let n = 0
    mockFetch(routes({ 'GET /auth/setup/timezones': () => (++n === 1 ? fail(500, 'x', 'Saat dilimleri okunamadı.') : ok(zones)) }))
    render(<SetupPage />)
    await toTimeStep(user)
    expect(await screen.findByRole('alert')).toHaveTextContent('Saat dilimleri okunamadı.')
    await user.click(screen.getByRole('button', { name: 'Tekrar Dene' }))
    expect(await screen.findByLabelText('Saat dilimi')).toHaveValue('Europe/Istanbul')
  })
})

describe('Kurulum sihirbazı: denetimler', () => {
  async function toStorageStep(user: UserEvent) {
    await fillAdmin(user, 'abdullah', 'uzun-ve-gizli-parola')
    await user.click(next())
    await screen.findByRole('heading', { name: 'Sunucu adını belirleyin' })
    await user.click(next())
    await screen.findByRole('heading', { name: 'Saat dilimini seçin' })
    await user.click(next())
    await screen.findByRole('heading', { name: 'Depolama denetimi' })
  }

  it('başarılı depolama, yetki ve Docker sonuçlarını gösterir', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await toStorageStep(user)
    expect(screen.getByText('Veri dizini yazılabilir.')).toBeInTheDocument()
    expect(screen.getByText('/var/lib/myserver · 120 GB boş')).toBeInTheDocument()
    expect(screen.getByText('Yardımcı araç kullanılabilir.')).toBeInTheDocument()
    expect(screen.getAllByText('Hazır')).toHaveLength(2)
    expect(screen.queryByText('Sorun var')).not.toBeInTheDocument()
    expect(screen.queryByText(/kurulumu engellemez/)).not.toBeInTheDocument()
    expectAccessibleNames()

    await user.click(next())
    await screen.findByRole('heading', { name: 'Docker denetimi' })
    expect(screen.getByText('Docker çalışıyor.')).toBeInTheDocument()
    expect(screen.getByText('Sürüm 28.1.1')).toBeInTheDocument()
    expect(screen.getByText('Hazır')).toBeInTheDocument()
    expect(screen.queryByText(/Docker çalışmadığında/)).not.toBeInTheDocument()
  })

  it('başarısız sonuçları sorun olarak gösterir ama ilerlemeye izin verir', async () => {
    const user = userEvent.setup()
    mockFetch(
      routes(
        {},
        {
          ...goodChecks,
          storage: { ok: false, message: 'Veri dizinine yazılamıyor.', detail: '/var/lib/myserver' },
          helper: { ok: false, message: 'Yardımcı araç çalıştırılamıyor.', detail: 'sudoers kuralı eksik' },
          docker: { ok: false, message: 'Docker servisine ulaşılamıyor.' },
        },
      ),
    )
    render(<SetupPage />)
    await toStorageStep(user)
    expect(screen.getByText('Veri dizinine yazılamıyor.')).toBeInTheDocument()
    expect(screen.getByText('Yardımcı araç çalıştırılamıyor.')).toBeInTheDocument()
    expect(screen.getByText('sudoers kuralı eksik')).toBeInTheDocument()
    expect(screen.getAllByText('Sorun var')).toHaveLength(2)
    expect(screen.queryByText('Hazır')).not.toBeInTheDocument()
    expect(screen.getByText('Bu sorun kurulumu engellemez; daha sonra giderebilirsiniz.')).toBeInTheDocument()

    await user.click(next())
    await screen.findByRole('heading', { name: 'Docker denetimi' })
    expect(screen.getByText('Docker servisine ulaşılamıyor.')).toBeInTheDocument()
    expect(screen.getByText('Sorun var')).toBeInTheDocument()
    expect(screen.getByText('Docker çalışmadığında uygulama kurulumu ve konteyner yönetimi kullanılamaz.')).toBeInTheDocument()

    await user.click(next())
    expect(await screen.findByRole('heading', { name: 'Kurulumu tamamlayın' })).toBeInTheDocument()
  })

  it('yalnızca biri başarısızsa diğerini hazır gösterir', async () => {
    const user = userEvent.setup()
    mockFetch(routes({}, { ...goodChecks, helper: { ok: false, message: 'Yardımcı araç çalıştırılamıyor.' } }))
    render(<SetupPage />)
    await toStorageStep(user)
    expect(screen.getAllByText('Hazır')).toHaveLength(1)
    expect(screen.getAllByText('Sorun var')).toHaveLength(1)
    expect(screen.getByText(/kurulumu engellemez/)).toBeInTheDocument()
  })

  it('denetimler alınamazsa hatayı gösterir; Yeniden Denetle sonuçları getirir', async () => {
    const user = userEvent.setup()
    let n = 0
    const f = mockFetch(routes({ 'GET /auth/setup/checks': () => (++n === 1 ? fail(503, 'x', 'Denetimler çalıştırılamadı.') : ok(goodChecks)) }))
    render(<SetupPage />)
    await fillAdmin(user, 'abdullah', 'uzun-ve-gizli-parola')
    await user.click(next())
    await user.type(await screen.findByLabelText('Sunucu adı'), 'evsunucu')
    await user.click(next())
    await screen.findByRole('heading', { name: 'Saat dilimini seçin' })
    await user.click(next())
    await screen.findByRole('heading', { name: 'Depolama denetimi' })
    expect(screen.getByRole('alert')).toHaveTextContent('Denetimler çalıştırılamadı.')

    await user.click(screen.getByRole('button', { name: 'Yeniden Denetle' }))
    expect(await screen.findByText('Veri dizini yazılabilir.')).toBeInTheDocument()
    expect(f.find('GET /auth/setup/checks')).toHaveLength(2)
  })

  it('Yeniden Denetle güncel sonucu gösterir', async () => {
    const user = userEvent.setup()
    let n = 0
    mockFetch(
      routes({
        'GET /auth/setup/checks': () =>
          ok(++n === 1 ? { ...goodChecks, storage: { ok: false, message: 'Veri dizinine yazılamıyor.' } } : goodChecks),
      }),
    )
    render(<SetupPage />)
    await toStorageStep(user)
    expect(screen.getByText('Veri dizinine yazılamıyor.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Yeniden Denetle' }))
    expect(await screen.findByText('Veri dizini yazılabilir.')).toBeInTheDocument()
    expect(screen.queryByText('Sorun var')).not.toBeInTheDocument()
  })
})

describe('Kurulum sihirbazı: tamamlama', () => {
  it('özeti gösterir ve son düğmeye basılana kadar hiçbir şey göndermez', async () => {
    const user = userEvent.setup()
    const f = mockFetch(routes())
    render(<SetupPage />)
    await toSummary(user)
    const summary = screen.getByRole('heading', { name: 'Kurulumu tamamlayın' }).closest('section')!
    expect(summary).toHaveTextContent('abdullah')
    expect(summary).toHaveTextContent('evsunucu')
    expect(summary).toHaveTextContent('Europe/Istanbul')
    expect(summary).not.toHaveTextContent('uzun-ve-gizli-parola')
    expect(f.find('POST /auth/setup')).toHaveLength(0)
    expectAccessibleNames()
  })

  it('tam olarak girilen değerleri gönderir', async () => {
    const user = userEvent.setup()
    const f = mockFetch(routes())
    render(<SetupPage />)
    await toSummary(user, { name: 'yonetici_1', password: ' Parola İçinde Boşluk 9! ', hostname: 'nas-01', zone: 'Asia/Tokyo' })
    await user.click(screen.getByRole('button', { name: 'Kurulumu Tamamla' }))
    await screen.findByRole('heading', { name: 'Kurulum tamamlandı' })
    const sent = f.find('POST /auth/setup')
    expect(sent).toHaveLength(1)
    expect(sent[0]!.body).toEqual({
      username: 'yonetici_1',
      password: ' Parola İçinde Boşluk 9! ',
      hostname: 'nas-01',
      timezone: 'Asia/Tokyo',
    })
  })

  it('dokunulmayan alanlarda sunucuda algılanan değerleri gönderir', async () => {
    const user = userEvent.setup()
    const f = mockFetch(routes({}, { ...goodChecks, hostname: 'NAS-Salon', timezone: 'Europe/Berlin' }))
    render(<SetupPage />)
    await toSummary(user)
    await user.click(screen.getByRole('button', { name: 'Kurulumu Tamamla' }))
    await screen.findByRole('heading', { name: 'Kurulum tamamlandı' })
    expect(f.find('POST /auth/setup')[0]!.body).toMatchObject({ hostname: 'nas-salon', timezone: 'Europe/Berlin' })
  })

  it('istek sürerken düğmeler kilitlenir ve ikinci istek gönderilmez', async () => {
    const user = userEvent.setup()
    const res = deferred<Response>()
    const f = mockFetch(routes({ 'POST /auth/setup': () => res.promise }))
    render(<SetupPage />)
    await toSummary(user)
    const finish = screen.getByRole('button', { name: 'Kurulumu Tamamla' })
    await user.click(finish)
    expect(finish).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Geri' })).toBeDisabled()
    await user.click(finish)
    expect(f.find('POST /auth/setup')).toHaveLength(1)
    res.resolve(ok({ ...signedIn(), warnings: [] }))
    await screen.findByRole('heading', { name: 'Kurulum tamamlandı' })
  })

  it('sunucu hatasını gösterir, özet adımında kalır ve yeniden denenebilir', async () => {
    const user = userEvent.setup()
    let n = 0
    const f = mockFetch(
      routes({
        'POST /auth/setup': () => (++n === 1 ? fail(400, 'weak_password', 'Parola çok yaygın; başka bir parola seçin.') : ok({ ...signedIn(), warnings: [] })),
      }),
    )
    render(<SetupPage />)
    await toSummary(user)
    await user.click(screen.getByRole('button', { name: 'Kurulumu Tamamla' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Parola çok yaygın; başka bir parola seçin.')
    expect(heading('Kurulumu tamamlayın')).toBeInTheDocument()
    expect(useAuth.getState().status?.setup_complete).toBe(false)

    await user.click(screen.getByRole('button', { name: 'Kurulumu Tamamla' }))
    await screen.findByRole('heading', { name: 'Kurulum tamamlandı' })
    expect(f.find('POST /auth/setup')).toHaveLength(2)
  })

  it('sunucunun döndürdüğü uyarıları gösterir', async () => {
    const user = userEvent.setup()
    const warnings = ['Sunucu adı uygulanamadı: yardımcı araç çalıştırılamıyor.', 'Saat dilimi uygulanamadı.']
    mockFetch(routes({ 'POST /auth/setup': ok({ ...signedIn(), warnings }) }))
    render(<SetupPage />)
    await toSummary(user)
    await user.click(screen.getByRole('button', { name: 'Kurulumu Tamamla' }))
    await screen.findByRole('heading', { name: 'Kurulum tamamlandı' })
    expect(screen.getByText('Bazı ayarlar uygulanamadı')).toBeInTheDocument()
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(warnings)
    expectAccessibleNames()
  })

  it('uyarı yoksa uyarı kutusu göstermez', async () => {
    const user = userEvent.setup()
    mockFetch(routes())
    render(<SetupPage />)
    await toSummary(user)
    await user.click(screen.getByRole('button', { name: 'Kurulumu Tamamla' }))
    await screen.findByRole('heading', { name: 'Kurulum tamamlandı' })
    expect(screen.queryByText('Bazı ayarlar uygulanamadı')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Panele Git' })).toBeEnabled()
  })
})

describe('Kurulum sihirbazı: panele geçiş (uygulama içinde)', () => {
  beforeEach(() => {
    FakeEventSource.install()
    vi.stubGlobal('scrollTo', vi.fn())
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    useAuth.setState({ status: null, loadError: null, user: null, isAdmin: false })
  })

  afterEach(() => vi.restoreAllMocks())

  it('kullanıcı "Panele Git" düğmesine basana kadar panele girmez', async () => {
    const user = userEvent.setup()
    const warnings = ['Saat dilimi uygulanamadı.']
    mockFetch(
      routes({
        'GET /auth/status': ok(makeStatus({ setup_complete: false })),
        'POST /auth/setup': ok({ ...signedIn(), warnings }),
      }),
    )
    render(
      <MemoryRouter initialEntries={['/nonexistent-page']}>
        <App />
      </MemoryRouter>,
    )
    await screen.findByRole('heading', { name: 'Yönetici hesabı oluşturun' })
    await toSummary(user)
    await user.click(screen.getByRole('button', { name: 'Kurulumu Tamamla' }))

    expect(await screen.findByRole('heading', { name: 'Kurulum tamamlandı' })).toBeInTheDocument()
    expect(screen.getByText('Saat dilimi uygulanamadı.')).toBeInTheDocument()
    // Give a wrongly scheduled redirect the chance to happen.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.getByRole('heading', { name: 'Kurulum tamamlandı' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Ana menü' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Parola')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Panele Git' }))
    await waitFor(() => expect(screen.getAllByRole('navigation', { name: 'Ana menü' }).length).toBeGreaterThan(0))
    expect(screen.queryByRole('heading', { name: 'Kurulum tamamlandı' })).not.toBeInTheDocument()
    expect(useAuth.getState().status).toMatchObject({ setup_complete: true, authenticated: true })
  })
})
