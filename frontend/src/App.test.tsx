import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { api } from '@/services/api'
import { fail, FakeEventSource, makeStatus, mockFetch, ok, signedIn, type Routes } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import App from './App'

// Pages behind the routes used here are core pages (not-found, forbidden);
// feature-module pages are not rendered by these tests.

function renderApp(path = '/nonexistent-page') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  )
}

function routes(over: Routes): Routes {
  return {
    'GET /health': ok({ status: 'HEALTHY', checks: [], checked_at: 1 }),
    'GET /notifications': ok({ items: [], unread: 0 }),
    'GET /updates/summary': fail(503, 'unavailable', 'Güncelleme bilgisi alınamadı.'),
    ...over,
  }
}

const shell = () => screen.findAllByRole('navigation', { name: 'Ana menü' })

beforeEach(() => {
  FakeEventSource.install()
  vi.stubGlobal('scrollTo', vi.fn())
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  useAuth.setState({ status: null, loadError: null, user: null, isAdmin: false })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('Uygulama yönlendirmesi', () => {
  it('durum yüklenirken yükleniyor gösterir', async () => {
    mockFetch({ 'GET /auth/status': () => new Promise<Response>(() => undefined) })
    renderApp()
    expect(screen.getByRole('status', { name: 'Yükleniyor…' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Parola')).not.toBeInTheDocument()
  })

  it('kurulum tamamlanmamışsa kurulum sihirbazını gösterir', async () => {
    mockFetch(routes({ 'GET /auth/status': ok(makeStatus({ setup_complete: false })) }))
    renderApp('/')
    expect(await screen.findByRole('heading', { name: 'Yönetici hesabı oluşturun' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Oturum Aç' })).not.toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Ana menü' })).not.toBeInTheDocument()
  })

  it('oturum açılmamışsa hangi adres istenirse istensin giriş sayfasını gösterir', async () => {
    const f = mockFetch(routes({ 'GET /auth/status': ok(makeStatus()) }))
    renderApp('/settings')
    expect(await screen.findByRole('button', { name: 'Oturum Aç' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Ana menü' })).not.toBeInTheDocument()
    // Nothing behind the login may be requested.
    expect(f.calls.map((c) => c.path)).toEqual(['/auth/status'])
    expect(FakeEventSource.instances).toHaveLength(0)
  })

  it('oturum açıksa paneli gösterir', async () => {
    mockFetch(routes({ 'GET /auth/status': ok(signedIn()) }))
    renderApp()
    expect((await shell()).length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: 'Oturum Aç' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Hesap menüsü' })).toHaveTextContent('abdullah')
  })

  it('bilinmeyen adreste sayfa bulunamadı gösterir', async () => {
    mockFetch(routes({ 'GET /auth/status': ok(signedIn()) }))
    renderApp('/boyle-bir-sayfa/yok')
    expect(await screen.findByText('Sayfa bulunamadı')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Ana Sayfaya Dön' })).toHaveAttribute('href', '/')
  })

  it.each(['/settings', '/terminal', '/settings/alt-sayfa'])('yönetici olmayan kullanıcıya %s için yetki yok sayfasını gösterir', async (path) => {
    mockFetch(routes({ 'GET /auth/status': ok(signedIn({ id: 2, username: 'misafir', role: 'user' })) }))
    renderApp(path)
    expect(await screen.findByText('Bu sayfaya erişim yetkiniz yok')).toBeInTheDocument()
    expect(screen.getByText('Bu bölüm yalnızca yönetici hesapları tarafından kullanılabilir.')).toBeInTheDocument()
    expect(screen.queryByText('Sayfa bulunamadı')).not.toBeInTheDocument()
  })

  it('yönetici olmayan kullanıcıya yönetici sayfalarının bağlantılarını göstermez', async () => {
    mockFetch(routes({ 'GET /auth/status': ok(signedIn({ id: 2, username: 'misafir', role: 'user' })) }))
    renderApp()
    await shell()
    expect(screen.getAllByRole('link', { name: 'Docker' }).length).toBeGreaterThan(0)
    for (const name of ['Terminal', 'Ayarlar', 'Yedekleme']) {
      expect(screen.queryByRole('link', { name })).not.toBeInTheDocument()
    }
  })

  it('yöneticiye yönetici sayfalarının bağlantılarını gösterir', async () => {
    mockFetch(routes({ 'GET /auth/status': ok(signedIn()) }))
    renderApp()
    await shell()
    expect(screen.getAllByRole('link', { name: 'Terminal' }).length).toBeGreaterThan(0)
    expect(screen.getAllByRole('link', { name: 'Ayarlar' }).length).toBeGreaterThan(0)
  })

  it('menüdeki her bağlantı var olan bir sayfaya gider', async () => {
    const { moduleRoutes } = await import('@/modules/registry')
    const { navItems } = await import('@/layouts/navigation')
    const known = new Set(moduleRoutes.map((r) => r.path))
    expect(navItems.filter((n) => !known.has(n.path)).map((n) => n.path)).toEqual([])
  })

  it('yönetici sayfaları hem menüde hem rotada yönetici olarak işaretlidir', async () => {
    const { moduleRoutes } = await import('@/modules/registry')
    const { navItems } = await import('@/layouts/navigation')
    for (const item of navItems) {
      const route = moduleRoutes.find((r) => r.path === item.path)
      if (route) expect([item.id, Boolean(route.adminOnly)]).toEqual([item.id, Boolean(item.adminOnly)])
    }
  })
})

describe('Bağlantı hatası', () => {
  it('hata mesajını gösterir; Tekrar Dene çalışır', async () => {
    const user = userEvent.setup()
    let n = 0
    const f = mockFetch(
      routes({
        'GET /auth/status': () => {
          if (++n === 1) throw new TypeError('Failed to fetch')
          return ok(makeStatus())
        },
      }),
    )
    renderApp()
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Panele bağlanılamadı')
    expect(alert).toHaveTextContent('Sunucuya ulaşılamıyor. Ağ bağlantınızı kontrol edin.')

    await user.click(screen.getByRole('button', { name: 'Tekrar Dene' }))
    expect(await screen.findByRole('button', { name: 'Oturum Aç' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(f.find('GET /auth/status')).toHaveLength(2)
  })

  it('sunucu hata yanıtı verdiğinde de mesajı gösterir ve yeniden denemede hata sürerse ekranda kalır', async () => {
    const user = userEvent.setup()
    const f = mockFetch({ 'GET /auth/status': new Response('<html>502</html>', { status: 502 }) })
    renderApp()
    expect(await screen.findByRole('alert')).toHaveTextContent('Sunucu şu anda yanıt veremiyor.')
    await user.click(screen.getByRole('button', { name: 'Tekrar Dene' }))
    await waitFor(() => expect(f.calls).toHaveLength(2))
    expect(screen.getByRole('alert')).toHaveTextContent('Sunucu şu anda yanıt veremiyor.')
  })
})

describe('Oturumun düşmesi', () => {
  it('panel açıkken gelen "unauthorized" yanıtı giriş sayfasına döndürür', async () => {
    mockFetch(
      routes({
        'GET /auth/status': ok(signedIn()),
        'GET /storage/disks': fail(401, 'unauthorized', 'Oturum açmanız gerekiyor.'),
      }),
    )
    renderApp()
    await shell()
    await api.get('/storage/disks').catch(() => undefined)
    expect(await screen.findByRole('button', { name: 'Oturum Aç' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Ana menü' })).not.toBeInTheDocument()
    expect(FakeEventSource.open).toHaveLength(0)
  })

  it('çıkış yapınca giriş sayfası gösterilir', async () => {
    const user = userEvent.setup()
    const f = mockFetch(routes({ 'GET /auth/status': ok(signedIn()), 'POST /auth/logout': ok(null) }))
    renderApp()
    await shell()
    await user.click(screen.getByRole('button', { name: 'Hesap menüsü' }))
    await user.click(screen.getByRole('menuitem', { name: 'Çıkış Yap' }))
    expect(await screen.findByRole('button', { name: 'Oturum Aç' })).toBeInTheDocument()
    expect(f.find('POST /auth/logout')).toHaveLength(1)
  })
})
