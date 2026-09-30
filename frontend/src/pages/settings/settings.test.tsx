import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toasts } from '@/components/ui'
import { expectAccessibleNames } from '@/test/a11y'
import { deferred, fail, makeUser, mockFetch, ok, signedIn, type Routes } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import { useUI } from '@/stores/ui'
import type { User } from '@/types/api'
import { GeneralSection } from './General'
import { UsersSection } from './Users'
import { useSettings } from './useSettings'

const server = {
  'general.hostname': 'evsunucu',
  'general.timezone': 'Europe/Istanbul',
  'general.language': 'tr',
  'security.session_hours': '12',
}

beforeEach(() => {
  useAuth.setState({ status: signedIn(), user: makeUser(), isAdmin: true })
  useUI.setState({ toasts: [] })
})

afterEach(() => {
  vi.unstubAllGlobals()
  useUI.setState({ toasts: [] })
})

const toasts = () => useUI.getState().toasts.map((t) => [t.kind, t.message])

describe('useSettings', () => {
  async function loaded(routes: Routes = {}) {
    const f = mockFetch({ 'GET /settings': ok(server), 'PUT /settings': (c) => ok({ ...server, ...(c.body as object) }), ...routes })
    const hook = renderHook(() => useSettings())
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    return { f, ...hook }
  }

  it('sunucudaki değerleri yükler ve temiz başlar', async () => {
    const { result } = await loaded()
    expect(result.current.values).toEqual(server)
    expect(result.current.dirty()).toBe(false)
    expect(result.current.dirty(['general.hostname'])).toBe(false)
  })

  it('yükleme hatasını bildirir', async () => {
    mockFetch({ 'GET /settings': fail(500, 'internal', 'Ayarlar okunamadı.') })
    const { result } = renderHook(() => useSettings())
    await waitFor(() => expect(result.current.error).toBe('Ayarlar okunamadı.'))
  })

  it('değişiklik durumunu anahtar bazında doğru bildirir', async () => {
    const { result } = await loaded()
    act(() => result.current.set('general.hostname', 'nas'))
    expect(result.current.values['general.hostname']).toBe('nas')
    expect(result.current.dirty()).toBe(true)
    expect(result.current.dirty(['general.hostname'])).toBe(true)
    expect(result.current.dirty(['general.timezone', 'general.language'])).toBe(false)
    expect(result.current.dirty([])).toBe(false)
  })

  it('değer eski haline getirilince değişmemiş sayılır', async () => {
    const { result } = await loaded()
    act(() => result.current.set('general.hostname', 'nas'))
    act(() => result.current.set('general.hostname', 'evsunucu'))
    expect(result.current.dirty()).toBe(false)
    expect(result.current.dirty(['general.hostname'])).toBe(false)
  })

  it('yalnızca değişen anahtarları gönderir', async () => {
    const { result, f } = await loaded()
    act(() => {
      result.current.set('general.hostname', 'nas')
      result.current.set('general.timezone', 'Europe/Istanbul')
      result.current.set('security.session_hours', '24')
    })
    let saved = false
    await act(async () => {
      saved = await result.current.save(['general.hostname', 'general.timezone', 'general.language'])
    })
    expect(saved).toBe(true)
    const put = f.find('PUT /settings')
    expect(put).toHaveLength(1)
    expect(put[0]!.body).toEqual({ 'general.hostname': 'nas' })
    expect(put[0]!.headers['X-CSRF-Token']).toBeUndefined()
  })

  it('kaydedilen anahtarlar temizlenir, diğer bölümün düzenlemeleri korunur', async () => {
    const { result } = await loaded()
    act(() => {
      result.current.set('general.hostname', 'nas')
      result.current.set('security.session_hours', '24')
    })
    await act(async () => {
      await result.current.save(['general.hostname'])
    })
    expect(result.current.dirty(['general.hostname'])).toBe(false)
    expect(result.current.dirty(['security.session_hours'])).toBe(true)
    expect(result.current.values).toMatchObject({ 'general.hostname': 'nas', 'security.session_hours': '24' })
    expect(toasts()).toEqual([['success', 'Ayarlar kaydedildi.']])
  })

  it('sunucunun döndürdüğü (düzeltilmiş) değeri gösterir', async () => {
    const { result } = await loaded({ 'PUT /settings': ok({ ...server, 'general.hostname': 'nas-01' }) })
    act(() => result.current.set('general.hostname', 'nas-01 '))
    await act(async () => {
      await result.current.save(['general.hostname'])
    })
    expect(result.current.values['general.hostname']).toBe('nas-01')
    expect(result.current.dirty()).toBe(false)
  })

  it('değişiklik yokken istek göndermez', async () => {
    const { result, f } = await loaded()
    act(() => result.current.set('general.hostname', 'evsunucu'))
    let saved = false
    await act(async () => {
      saved = await result.current.save(['general.hostname', 'general.timezone'])
    })
    expect(saved).toBe(true)
    expect(f.find('PUT /settings')).toHaveLength(0)
    expect(toasts()).toEqual([])
  })

  it('kaydetme başarısız olursa düzenlemeleri korur ve hatayı bildirir', async () => {
    const { result } = await loaded({ 'PUT /settings': fail(400, 'invalid', 'Sunucu adı geçersiz.') })
    act(() => {
      result.current.set('general.hostname', 'nas')
      result.current.set('general.timezone', 'Asia/Tokyo')
    })
    let saved = true
    await act(async () => {
      saved = await result.current.save(['general.hostname', 'general.timezone'])
    })
    expect(saved).toBe(false)
    expect(result.current.values).toMatchObject({ 'general.hostname': 'nas', 'general.timezone': 'Asia/Tokyo' })
    expect(result.current.dirty(['general.hostname', 'general.timezone'])).toBe(true)
    expect(result.current.saving).toBe(false)
    expect(toasts()).toEqual([['error', 'Sunucu adı geçersiz.']])
  })

  it('kaydederken saving true olur', async () => {
    const res = deferred<Response>()
    const { result } = await loaded({ 'PUT /settings': () => res.promise })
    act(() => result.current.set('general.hostname', 'nas'))
    let done!: Promise<boolean>
    act(() => {
      done = result.current.save(['general.hostname'])
    })
    expect(result.current.saving).toBe(true)
    await act(async () => {
      res.resolve(ok({ ...server, 'general.hostname': 'nas' }))
      await done
    })
    expect(result.current.saving).toBe(false)
  })

  it('reset verilen anahtarların düzenlemelerini geri alır', async () => {
    const { result } = await loaded()
    act(() => {
      result.current.set('general.hostname', 'nas')
      result.current.set('general.timezone', 'Asia/Tokyo')
    })
    act(() => result.current.reset(['general.hostname']))
    expect(result.current.values).toMatchObject({ 'general.hostname': 'evsunucu', 'general.timezone': 'Asia/Tokyo' })
    act(() => result.current.reset())
    expect(result.current.values).toEqual(server)
    expect(result.current.dirty()).toBe(false)
  })
})

describe('Ayarlar: Genel', () => {
  const zones = ['Asia/Tokyo', 'Europe/Berlin', 'Europe/Istanbul']
  function mount(routes: Routes = {}) {
    const f = mockFetch({
      'GET /settings': ok(server),
      'GET /settings/timezones': ok(zones),
      'PUT /settings': (c) => ok({ ...server, ...(c.body as object) }),
      ...routes,
    })
    render(
      <>
        <GeneralSection />
        <Toasts />
      </>,
    )
    return f
  }
  const hostname = () => screen.findByLabelText('Sunucu adı')
  const save = () => screen.getByRole('button', { name: 'Kaydet' })

  it('değerleri gösterir; değişiklik yokken Kaydet devre dışıdır; alanların adı vardır', async () => {
    mount()
    expect(await hostname()).toHaveValue('evsunucu')
    await waitFor(() => expect(screen.getByLabelText('Saat dilimi')).toHaveValue('Europe/Istanbul'))
    expect(save()).toBeDisabled()
    expectAccessibleNames()
  })

  it('sunucu adı değiştirilmeden önce onay ister', async () => {
    const user = userEvent.setup()
    const f = mount()
    await user.clear(await hostname())
    await user.type(await hostname(), 'NAS-01')
    expect(await hostname()).toHaveValue('nas-01')
    await user.click(save())

    const dialog = screen.getByRole('dialog', { name: 'Sunucu adı değiştirilsin mi?' })
    expect(dialog).toHaveTextContent('Sunucu adı "nas-01" olarak değiştirilecek.')
    expect(f.find('PUT /settings')).toHaveLength(0)

    await user.click(within(dialog).getByRole('button', { name: 'Değiştir' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(f.find('PUT /settings')).toHaveLength(1)
    expect(f.find('PUT /settings')[0]!.body).toEqual({ 'general.hostname': 'nas-01' })
    expect(await screen.findByText('Genel ayarlar kaydedildi.')).toBeInTheDocument()
    expect(save()).toBeDisabled()
  })

  it('Enter ile gönderildiğinde de onay ister', async () => {
    const user = userEvent.setup()
    const f = mount()
    await user.type(await hostname(), '2{Enter}')
    expect(screen.getByRole('dialog', { name: 'Sunucu adı değiştirilsin mi?' })).toBeInTheDocument()
    expect(f.find('PUT /settings')).toHaveLength(0)
  })

  it('onaydan vazgeçilirse hiçbir şey gönderilmez ve düzenleme korunur', async () => {
    const user = userEvent.setup()
    const f = mount()
    await user.type(await hostname(), '2')
    await user.click(save())
    await user.click(screen.getByRole('button', { name: 'Vazgeç' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(f.find('PUT /settings')).toHaveLength(0)
    expect(await hostname()).toHaveValue('evsunucu2')
    expect(save()).toBeEnabled()
  })

  it('yalnızca saat dilimi değiştiğinde onay istemeden kaydeder', async () => {
    const user = userEvent.setup()
    const f = mount()
    await hostname()
    await waitFor(() => expect(screen.getByLabelText('Saat dilimi')).toBeEnabled())
    await user.selectOptions(screen.getByLabelText('Saat dilimi'), 'Asia/Tokyo')
    await user.click(save())
    await waitFor(() => expect(f.find('PUT /settings')).toHaveLength(1))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(f.find('PUT /settings')[0]!.body).toEqual({ 'general.timezone': 'Asia/Tokyo' })
  })

  it.each(['-nas', 'nas_01', 'nas 01'])('geçersiz sunucu adı "%s" kaydedilemez', async (value) => {
    const user = userEvent.setup()
    const f = mount()
    await user.clear(await hostname())
    await user.type(await hostname(), value)
    expect(screen.getByRole('alert')).toHaveTextContent('Sunucu adı geçersiz.')
    expect(save()).toBeDisabled()
    await user.type(await hostname(), '{Enter}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(f.find('PUT /settings')).toHaveLength(0)
  })

  it('kaydetme başarısız olursa hatayı gösterir ve düzenlemeyi korur', async () => {
    const user = userEvent.setup()
    mount({ 'PUT /settings': fail(502, 'hostname_failed', 'Sunucu adı değiştirilemedi.') })
    await user.type(await hostname(), '2')
    await user.click(save())
    await user.click(screen.getByRole('button', { name: 'Değiştir' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Sunucu adı değiştirilemedi.')
    expect(await hostname()).toHaveValue('evsunucu2')
    expect(save()).toBeEnabled()
    expect(screen.queryByText('Genel ayarlar kaydedildi.')).not.toBeInTheDocument()
  })

  it('ayarlar yüklenemezse hatayı ve yeniden deneme düğmesini gösterir', async () => {
    const user = userEvent.setup()
    let n = 0
    mount({ 'GET /settings': () => (++n === 1 ? fail(500, 'x', 'Ayarlar okunamadı.') : ok(server)) })
    expect(await screen.findByRole('alert')).toHaveTextContent('Ayarlar okunamadı.')
    await user.click(screen.getByRole('button', { name: 'Tekrar Dene' }))
    expect(await hostname()).toHaveValue('evsunucu')
  })
})

describe('Ayarlar: Kullanıcılar', () => {
  const me = makeUser({ id: 1, username: 'abdullah', role: 'admin' })
  const other = makeUser({ id: 2, username: 'misafir', role: 'user', last_login_at: null })
  const admin2 = makeUser({ id: 3, username: 'zeynep', role: 'admin', disabled: true })

  function mount(routes: Routes = {}, users: User[] = [me, other, admin2]) {
    const f = mockFetch({
      'GET /auth/users': ok(users),
      'DELETE /auth/users/2': ok(null),
      'PUT /auth/users/1': ok(me),
      'PUT /auth/users/2': ok(other),
      'POST /auth/users': ok(makeUser({ id: 4, username: 'yeni' })),
      ...routes,
    })
    render(
      <>
        <UsersSection />
        <Toasts />
      </>,
    )
    return f
  }

  const row = async (name: string) => (await screen.findByText(name)).closest('li')!

  it('kullanıcıları rolleri ve durumlarıyla listeler; simge düğmelerinin adı vardır', async () => {
    mount()
    expect(within(await row('abdullah')).getByText('Siz')).toBeInTheDocument()
    expect(within(await row('abdullah')).getByText('Yönetici')).toBeInTheDocument()
    expect(within(await row('misafir')).getByText('Kullanıcı')).toBeInTheDocument()
    expect(within(await row('misafir')).queryByText('Siz')).not.toBeInTheDocument()
    expect(within(await row('misafir')).getByText('Hiç giriş yapmadı')).toBeInTheDocument()
    expect(within(await row('zeynep')).getByText('Devre dışı')).toBeInTheDocument()
    expectAccessibleNames()
  })

  it('geçerli kullanıcı kendini silemez', async () => {
    const user = userEvent.setup()
    const f = mount()
    const mine = within(await row('abdullah')).getByRole('button', { name: 'abdullah kullanıcısını sil' })
    expect(mine).toBeDisabled()
    await user.click(mine)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(f.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0)
    expect(within(await row('misafir')).getByRole('button', { name: 'misafir kullanıcısını sil' })).toBeEnabled()
  })

  it('geçerli kullanıcı kendi rolünü düşüremez ve hesabını devre dışı bırakamaz', async () => {
    const user = userEvent.setup()
    const f = mount()
    await user.click(within(await row('abdullah')).getByRole('button', { name: 'abdullah kullanıcısını düzenle' }))
    const dialog = screen.getByRole('dialog', { name: 'Kullanıcıyı düzenle' })
    expectAccessibleNames(dialog)
    const role = within(dialog).getByLabelText('Rol')
    expect(role).toBeDisabled()
    expect(role).toHaveValue('admin')
    const active = within(dialog).getByRole('switch', { name: 'Hesap etkin' })
    expect(active).toBeDisabled()
    expect(active).toBeChecked()
    await user.click(active)
    expect(active).toBeChecked()

    await user.type(within(dialog).getByLabelText('Yeni parola'), 'yepyeni-parola-1')
    await user.click(within(dialog).getByRole('button', { name: 'Kaydet' }))
    await waitFor(() => expect(f.find('PUT /auth/users/1')).toHaveLength(1))
    expect(f.find('PUT /auth/users/1')[0]!.body).toEqual({ role: 'admin', disabled: false, password: 'yepyeni-parola-1' })
  })

  it('başka bir kullanıcının rolü ve durumu değiştirilebilir; boş parola gönderilmez', async () => {
    const user = userEvent.setup()
    const f = mount()
    await user.click(within(await row('misafir')).getByRole('button', { name: 'misafir kullanıcısını düzenle' }))
    const dialog = screen.getByRole('dialog', { name: 'Kullanıcıyı düzenle' })
    expect(within(dialog).getByLabelText('Kullanıcı adı')).toBeDisabled()
    await user.selectOptions(within(dialog).getByLabelText('Rol'), 'admin')
    await user.click(within(dialog).getByRole('switch', { name: 'Hesap etkin' }))
    await user.click(within(dialog).getByRole('button', { name: 'Kaydet' }))
    await waitFor(() => expect(f.find('PUT /auth/users/2')).toHaveLength(1))
    expect(f.find('PUT /auth/users/2')[0]!.body).toEqual({ role: 'admin', disabled: true })
    expect(await screen.findByText('Kullanıcı güncellendi.')).toBeInTheDocument()
  })

  it('silmeden önce kullanıcının adını içeren bir onay ister', async () => {
    const user = userEvent.setup()
    const f = mount()
    await user.click(within(await row('misafir')).getByRole('button', { name: 'misafir kullanıcısını sil' }))
    const dialog = screen.getByRole('dialog', { name: 'Kullanıcı silinsin mi?' })
    expect(dialog).toHaveTextContent('"misafir" hesabı kalıcı olarak silinecek')
    expect(f.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0)

    await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(f.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0)
  })

  it('onaylanınca doğru kullanıcıyı siler ve listeyi yeniler', async () => {
    const user = userEvent.setup()
    const f = mount()
    await user.click(within(await row('misafir')).getByRole('button', { name: 'misafir kullanıcısını sil' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Sil' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(f.calls.filter((c) => c.method === 'DELETE').map((c) => c.path)).toEqual(['/auth/users/2'])
    expect(await screen.findByText('Kullanıcı silindi.')).toBeInTheDocument()
    await waitFor(() => expect(f.find('GET /auth/users')).toHaveLength(2))
  })

  it('silme başarısız olursa onay kutusu açık kalır ve hatayı gösterir', async () => {
    const user = userEvent.setup()
    mount({ 'DELETE /auth/users/2': fail(409, 'last_admin', 'Son yönetici hesabı silinemez.') })
    await user.click(within(await row('misafir')).getByRole('button', { name: 'misafir kullanıcısını sil' }))
    const dialog = screen.getByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Sil' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Son yönetici hesabı silinemez.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.queryByText('Kullanıcı silindi.')).not.toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }))
    await user.click(within(await row('zeynep')).getByRole('button', { name: 'zeynep kullanıcısını sil' }))
    expect(within(screen.getByRole('dialog')).queryByRole('alert')).not.toBeInTheDocument()
  })

  it('yeni kullanıcı oluşturur; sunucu hatasını kutunun içinde gösterir', async () => {
    const user = userEvent.setup()
    let n = 0
    const f = mount({
      'POST /auth/users': () => (++n === 1 ? fail(409, 'exists', 'Bu kullanıcı adı zaten kullanılıyor.') : ok(makeUser({ id: 4, username: 'yeni' }))),
    })
    await screen.findByText('misafir')
    await user.click(screen.getByRole('button', { name: 'Kullanıcı Ekle' }))
    const dialog = screen.getByRole('dialog', { name: 'Yeni kullanıcı' })
    expectAccessibleNames(dialog)
    expect(within(dialog).getByLabelText('Rol')).toHaveValue('user')
    await user.type(within(dialog).getByLabelText('Kullanıcı adı'), 'Yeni')
    await user.type(within(dialog).getByLabelText('Parola'), 'uzun-bir-parola')
    await user.click(within(dialog).getByRole('button', { name: 'Oluştur' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Bu kullanıcı adı zaten kullanılıyor.')

    await user.click(within(dialog).getByRole('button', { name: 'Oluştur' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(f.find('POST /auth/users')[1]!.body).toEqual({ username: 'yeni', password: 'uzun-bir-parola', role: 'user' })
  })

  it('liste alınamazsa hatayı gösterir', async () => {
    mount({ 'GET /auth/users': fail(403, 'forbidden', 'Yetkiniz bulunmuyor.') })
    expect(await screen.findByRole('alert')).toHaveTextContent('Yetkiniz bulunmuyor.')
  })
})
