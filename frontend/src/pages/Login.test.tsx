import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expectAccessibleNames } from '@/test/a11y'
import { deferred, fail, makeStatus, mockFetch, ok, signedIn } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import LoginPage from './Login'

beforeEach(() => {
  useAuth.setState({ status: makeStatus({ version: '1.4.2' }), loadError: null, user: null, isAdmin: false })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const username = () => screen.getByLabelText('Kullanıcı adı')
const password = () => screen.getByLabelText('Parola')
const submit = () => screen.getByRole('button', { name: 'Oturum Aç' })

describe('Giriş sayfası', () => {
  it('alanların ve düğmenin erişilebilir adı vardır; parola gizlidir', () => {
    render(<LoginPage />)
    expectAccessibleNames()
    expect(password()).toHaveAttribute('type', 'password')
    expect(password()).toHaveAttribute('autocomplete', 'current-password')
    expect(username()).toHaveAttribute('autocomplete', 'username')
    expect(screen.getByText('MyServer v1.4.2')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('kimlik bilgilerini gönderir ve oturumu açar', async () => {
    const user = userEvent.setup()
    const f = mockFetch({ 'POST /auth/login': ok(signedIn()) })
    render(<LoginPage />)
    await user.type(username(), 'abdullah')
    await user.type(password(), 'Çok Gizli parola 1!')
    await user.click(submit())

    await waitFor(() => expect(useAuth.getState().status?.authenticated).toBe(true))
    expect(f.calls).toHaveLength(1)
    expect(f.calls[0]!.body).toEqual({ username: 'abdullah', password: 'Çok Gizli parola 1!' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('Enter ile gönderilebilir', async () => {
    const user = userEvent.setup()
    const f = mockFetch({ 'POST /auth/login': ok(signedIn()) })
    render(<LoginPage />)
    await user.type(username(), 'abdullah')
    await user.type(password(), 'parola-parola{Enter}')
    await waitFor(() => expect(f.calls).toHaveLength(1))
  })

  it('kullanıcı adındaki boşlukları kırpar, parolaya dokunmaz', async () => {
    const user = userEvent.setup()
    const f = mockFetch({ 'POST /auth/login': ok(signedIn()) })
    render(<LoginPage />)
    await user.type(username(), '  abdullah ')
    await user.type(password(), ' parola sonunda boşluk ')
    await user.click(submit())
    await waitFor(() => expect(f.calls).toHaveLength(1))
    expect(f.calls[0]!.body).toEqual({ username: 'abdullah', password: ' parola sonunda boşluk ' })
  })

  it('sunucunun hata mesajını role="alert" ile gösterir ve parolayı temizler', async () => {
    const user = userEvent.setup()
    mockFetch({ 'POST /auth/login': fail(401, 'invalid_credentials', 'Kullanıcı adı veya parola hatalı.') })
    render(<LoginPage />)
    await user.type(username(), 'abdullah')
    await user.type(password(), 'yanlış-parola')
    await user.click(submit())

    expect(await screen.findByRole('alert')).toHaveTextContent('Kullanıcı adı veya parola hatalı.')
    expect(password()).toHaveValue('')
    expect(username()).toHaveValue('abdullah')
    expect(submit()).toBeEnabled()
    expect(useAuth.getState().status?.authenticated).toBe(false)
  })

  it('çok fazla denemede sunucunun mesajını gösterir', async () => {
    const user = userEvent.setup()
    mockFetch({ 'POST /auth/login': fail(429, 'rate_limited', 'Çok fazla hatalı deneme. 5 dakika sonra tekrar deneyin.') })
    render(<LoginPage />)
    await user.type(username(), 'abdullah')
    await user.type(password(), 'x')
    await user.click(submit())
    expect(await screen.findByRole('alert')).toHaveTextContent('Çok fazla hatalı deneme. 5 dakika sonra tekrar deneyin.')
  })

  it('sunucuya ulaşılamazsa Türkçe mesaj gösterir', async () => {
    const user = userEvent.setup()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    render(<LoginPage />)
    await user.type(username(), 'abdullah')
    await user.type(password(), 'parola-parola')
    await user.click(submit())
    expect(await screen.findByRole('alert')).toHaveTextContent('Sunucuya ulaşılamıyor. Ağ bağlantınızı kontrol edin.')
    expect(password()).toHaveValue('')
  })

  it('istek sürerken düğme devre dışıdır ve ikinci istek gönderilmez', async () => {
    const user = userEvent.setup()
    const res = deferred<Response>()
    const f = mockFetch({ 'POST /auth/login': () => res.promise })
    render(<LoginPage />)
    await user.type(username(), 'abdullah')
    await user.type(password(), 'parola-parola')
    await user.click(submit())

    expect(submit()).toBeDisabled()
    await user.click(submit())
    await user.type(password(), '{Enter}')
    expect(f.calls).toHaveLength(1)

    res.resolve(fail(401, 'invalid_credentials', 'Kullanıcı adı veya parola hatalı.'))
    await waitFor(() => expect(submit()).toBeEnabled())
  })

  it('yeni denemede önceki hata mesajı kalkar', async () => {
    const user = userEvent.setup()
    const second = deferred<Response>()
    let n = 0
    mockFetch({ 'POST /auth/login': () => (++n === 1 ? fail(401, 'invalid_credentials', 'Kullanıcı adı veya parola hatalı.') : second.promise) })
    render(<LoginPage />)
    await user.type(username(), 'abdullah')
    await user.type(password(), 'yanlış')
    await user.click(submit())
    await screen.findByRole('alert')

    await user.type(password(), 'doğru-parola')
    await user.click(submit())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    second.resolve(ok(signedIn()))
    await waitFor(() => expect(useAuth.getState().status?.authenticated).toBe(true))
  })

  it('boş alanlarla istek göndermez', async () => {
    const user = userEvent.setup()
    const f = mockFetch({ 'POST /auth/login': ok(signedIn()) })
    render(<LoginPage />)
    await user.click(submit())
    expect(username()).toBeRequired()
    expect(password()).toBeRequired()
    expect(f.calls).toHaveLength(0)
  })
})
