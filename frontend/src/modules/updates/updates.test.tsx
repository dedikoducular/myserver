import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { FakeEventSource, mockFetch, ok, signedIn, type FetchMock } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import { AptSection } from './AptSection'
import { SelfSection } from './SelfSection'
import { UpdatesWidget } from './Widget'
import { useSummaryStore, useUpdateCount } from './summary'
import type { AptPackage, AptView, JobMeta, SelfView, Summary } from './types'

// The network is faked at the fetch / EventSource boundary. Nothing here
// reaches a server; every request the components make is recorded.

const NOW = Math.floor(Date.now() / 1000)

function summary(apt: number, docker: number, self: number): Summary {
  return {
    total: apt + docker + self,
    apt: { count: apt, checked_at: NOW, checking: false, error: null, security_count: 0, kernel_count: 0, reboot_required: false, running: false },
    docker: { count: docker, checked_at: NOW, checking: false, error: null, images: 5, unchecked: 0 },
    self: { count: self, checked_at: NOW, checking: false, error: null, installed_version: '1.2.0', latest_version: self ? '1.3.0' : '1.2.0', configured: true },
  }
}

function pkg(name: string, over: Partial<AptPackage> = {}): AptPackage {
  return {
    name,
    arch: 'amd64',
    current_version: '1.0-1',
    candidate_version: '1.0-2',
    origin: 'Ubuntu:24.04/noble-updates',
    security: false,
    kernel: false,
    new: false,
    ...over,
  }
}

function aptView(over: Partial<AptView> = {}): AptView {
  const packages = over.packages ?? [pkg('libc6', { security: true }), pkg('linux-image-6.8.0-45-generic', { kernel: true })]
  return {
    packages,
    count: packages.length,
    security_count: packages.filter((p) => p.security).length,
    kernel_count: packages.filter((p) => p.kernel).length,
    checked_at: NOW,
    lists_refreshed_at: NOW,
    error: null,
    checking: false,
    auto_check: true,
    reboot_required: false,
    reboot_packages: [],
    hostname: 'sunucu-1',
    job: null,
    ...over,
  }
}

function selfView(over: Partial<SelfView> = {}): SelfView {
  return {
    arch: 'amd64',
    asset: null,
    installable: false,
    install_blocker: '',
    last_update: null,
    installed_version: '1.2.0',
    source: 'github',
    source_label: 'github.com/ornek/myserver',
    configured: true,
    latest: null,
    update_available: false,
    checked_at: NOW,
    error: null,
    checking: false,
    auto_check: true,
    ...over,
  }
}

const SHA = 'a'.repeat(64)
const ASSET = { url: 'https://example.com/r/myserver-linux-amd64.tar.gz', sha256: SHA }

function available(over: Partial<SelfView> = {}, notes = 'Hata düzeltmeleri.'): SelfView {
  return selfView({
    latest: { version: '1.3.0', notes, published_at: NOW, url: 'https://example.com/r', assets: { amd64: ASSET } },
    update_available: true,
    asset: ASSET,
    installable: true,
    ...over,
  })
}

const JOB: JobMeta = {
  id: '0123456789abcdef',
  kind: 'apt_upgrade',
  title: 'Ubuntu paket güncellemesi',
  detail: 'Standart yükseltme, çekirdek hariç',
  username: 'abdullah',
  started_at: NOW,
  finished_at: null,
  status: 'running',
  message: '',
}

function stateChanging(net: FetchMock) {
  return net.calls.filter((c) => c.method !== 'GET')
}

function renderIn(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>)
}

beforeEach(() => {
  FakeEventSource.install()
  useAuth.getState().adopt(signedIn())
  useSummaryStore.setState({ data: undefined, error: null, loading: false, loadedAt: 0 })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('UpdatesWidget', () => {
  it('sunucunun bildirdiği gerçek sayıları gösterir', async () => {
    const net = mockFetch({ 'GET /updates/summary': ok(summary(3, 2, 1)) })
    renderIn(<UpdatesWidget />)
    expect(await screen.findByText('3 güncelleme')).toBeInTheDocument()
    expect(screen.getByText('2 güncelleme')).toBeInTheDocument()
    expect(screen.getByText('v1.3.0 mevcut')).toBeInTheDocument()
    expect(screen.getByText('6')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Güncellemeleri İncele' })).toBeEnabled()
    expect(stateChanging(net)).toEqual([])
  })

  it('güncelleme yokken düğme devre dışıdır ve sayı uydurulmaz', async () => {
    const net = mockFetch({ 'GET /updates/summary': ok(summary(0, 0, 0)) })
    renderIn(<UpdatesWidget />)
    const button = await screen.findByRole('button', { name: 'Yüklenecek güncelleme yok' })
    expect(button).toBeDisabled()
    expect(screen.getAllByText('Güncel')).toHaveLength(2)
    expect(screen.getByText('v1.2.0 (Güncel)')).toBeInTheDocument()
    expect(screen.queryByText(/\d+ güncelleme/)).not.toBeInTheDocument()
    expect(stateChanging(net)).toEqual([])
  })

  it('hiç denetlenmemişse "Güncel" demez', async () => {
    const s = summary(0, 0, 0)
    s.apt.checked_at = null
    s.docker.checked_at = null
    s.self.checked_at = null
    mockFetch({ 'GET /updates/summary': ok(s) })
    renderIn(<UpdatesWidget />)
    expect(await screen.findAllByText('Denetlenmedi')).toHaveLength(2)
    expect(screen.queryByText('Güncel')).not.toBeInTheDocument()
    expect(screen.queryByText('v1.2.0 (Güncel)')).not.toBeInTheDocument()
  })
})

describe('useUpdateCount', () => {
  it('yalnızca özet uç noktasını okur, hiçbir denetim başlatmaz', async () => {
    const net = mockFetch({ 'GET /updates/summary': ok(summary(3, 0, 1)) })
    const a = renderHook(() => useUpdateCount())
    const b = renderHook(() => useUpdateCount())
    await waitFor(() => expect(a.result.current).toBe(4))
    expect(b.result.current).toBe(4)
    a.rerender()
    b.rerender()
    await Promise.resolve()

    expect(net.calls.length).toBeGreaterThan(0)
    for (const c of net.calls) {
      expect(c.method).toBe('GET')
      expect(c.path).toBe('/updates/summary')
      expect(c.path).not.toContain('check')
    }
    // The cached copy is shared: two users of the hook cost one request.
    expect(net.calls).toHaveLength(1)
  })

  it('özet okunamazsa 0 döner ve yine denetim başlatmaz', async () => {
    const net = mockFetch({})
    const { result } = renderHook(() => useUpdateCount())
    await waitFor(() => expect(useSummaryStore.getState().error).not.toBeNull())
    expect(result.current).toBe(0)
    expect(net.calls.every((c) => c.method === 'GET' && c.path === '/updates/summary')).toBe(true)
  })
})

describe('AptSection yükseltme', () => {
  it('sayfa açılınca hiçbir şey yüklemez ve çekirdek seçeneği kapalıdır', async () => {
    const net = mockFetch({ 'GET /updates/apt': ok(aptView()), 'GET /updates/summary': ok(summary(2, 0, 0)) })
    renderIn(<AptSection />)
    const sw = await screen.findByRole('switch', { name: 'Çekirdek güncellemelerini de yükle' })
    expect(sw).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByRole('combobox')).toHaveValue('upgrade')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(stateChanging(net)).toEqual([])
  })

  it('açık onay olmadan istek göndermez; Vazgeç hiçbir şey başlatmaz', async () => {
    const user = userEvent.setup()
    const net = mockFetch({
      'GET /updates/apt': ok(aptView()),
      'GET /updates/summary': ok(summary(2, 0, 0)),
      'POST /updates/apt/upgrade': ok(JOB, 202),
    })
    renderIn(<AptSection />)
    await user.click(await screen.findByRole('button', { name: 'Güncellemeleri Yükle' }))
    const dialog = screen.getByRole('dialog', { name: 'Paket güncellemeleri yüklensin mi?' })
    expect(dialog).toHaveTextContent('Çekirdek paketleri yüklenmeyecek.')
    expect(stateChanging(net)).toEqual([])

    await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(stateChanging(net)).toEqual([])
  })

  it('onaylanınca varsayılan türle ve çekirdek hariç istek gönderir', async () => {
    const user = userEvent.setup()
    const net = mockFetch({
      'GET /updates/apt': ok(aptView()),
      'GET /updates/summary': ok(summary(2, 0, 0)),
      'POST /updates/apt/upgrade': ok(JOB, 202),
    })
    renderIn(<AptSection />)
    await user.click(await screen.findByRole('button', { name: 'Güncellemeleri Yükle' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Yükle' }))
    await waitFor(() => expect(net.find('POST /updates/apt/upgrade')).toHaveLength(1))
    expect(net.find('POST /updates/apt/upgrade')[0]?.body).toEqual({ mode: 'upgrade', include_kernel: false, confirm: true })
    expect(stateChanging(net)).toHaveLength(1)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('çekirdek yalnızca kullanıcı seçeneği açarsa dahil edilir ve onayda belirtilir', async () => {
    const user = userEvent.setup()
    const net = mockFetch({
      'GET /updates/apt': ok(aptView()),
      'GET /updates/summary': ok(summary(2, 0, 0)),
      'POST /updates/apt/upgrade': ok(JOB, 202),
    })
    renderIn(<AptSection />)
    await user.click(await screen.findByRole('switch', { name: 'Çekirdek güncellemelerini de yükle' }))
    await user.click(screen.getByRole('button', { name: 'Güncellemeleri Yükle' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('Çekirdek paketleri de yüklenecek.')
    await user.click(within(dialog).getByRole('button', { name: 'Yükle' }))
    await waitFor(() => expect(net.find('POST /updates/apt/upgrade')).toHaveLength(1))
    expect(net.find('POST /updates/apt/upgrade')[0]?.body).toEqual({ mode: 'upgrade', include_kernel: true, confirm: true })
  })

  it('yalnızca çekirdek paketi varken ve seçenek kapalıyken yükleme düğmesi devre dışıdır', async () => {
    mockFetch({
      'GET /updates/apt': ok(aptView({ packages: [pkg('linux-image-6.8.0-45-generic', { kernel: true })] })),
      'GET /updates/summary': ok(summary(1, 0, 0)),
    })
    renderIn(<AptSection />)
    expect(await screen.findByRole('button', { name: 'Güncellemeleri Yükle' })).toBeDisabled()
  })

  it('yönetici olmayan kullanıcıya yükleme ve yeniden başlatma denetimleri gösterilmez', async () => {
    useAuth.getState().adopt(signedIn({ role: 'user' }))
    mockFetch({
      'GET /updates/apt': ok(aptView({ reboot_required: true })),
      'GET /updates/summary': ok(summary(2, 0, 0)),
    })
    renderIn(<AptSection />)
    expect(await screen.findByText('libc6')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Güncellemeleri Yükle' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Yeniden Başlat' })).not.toBeInTheDocument()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Denetle' })).toBeDisabled()
  })
})

describe('AptSection yeniden başlatma', () => {
  const routes = () => ({
    'GET /updates/apt': ok(aptView({ reboot_required: true, reboot_packages: ['linux-image-6.8.0-45-generic'] })),
    'GET /updates/summary': ok(summary(2, 0, 0)),
    'POST /updates/reboot': ok({ rebooting: true }),
  })

  it('sunucu adı tam yazılmadan onaylanamaz', async () => {
    const user = userEvent.setup()
    const net = mockFetch(routes())
    renderIn(<AptSection />)
    await user.click(await screen.findByRole('button', { name: 'Yeniden Başlat' }))
    const dialog = screen.getByRole('dialog', { name: 'Sunucu yeniden başlatılsın mı?' })
    const confirm = within(dialog).getByRole('button', { name: 'Yeniden Başlat' })
    const input = within(dialog).getByRole('textbox')
    expect(confirm).toBeDisabled()

    for (const wrong of ['sunucu', 'SUNUCU-1', 'sunucu-11', 'sunucu 1', 'evet']) {
      await user.clear(input)
      await user.type(input, wrong + '{Enter}')
      expect(confirm).toBeDisabled()
      await user.click(confirm)
    }
    expect(stateChanging(net)).toEqual([])
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('sunucu adı yazılınca isteği o adla gönderir', async () => {
    const user = userEvent.setup()
    const net = mockFetch(routes())
    renderIn(<AptSection />)
    await user.click(await screen.findByRole('button', { name: 'Yeniden Başlat' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'sunucu-1')
    const confirm = within(dialog).getByRole('button', { name: 'Yeniden Başlat' })
    expect(confirm).toBeEnabled()
    await user.click(confirm)
    await waitFor(() => expect(net.find('POST /updates/reboot')).toHaveLength(1))
    expect(net.find('POST /updates/reboot')[0]?.body).toEqual({ hostname: 'sunucu-1' })
    expect(stateChanging(net)).toHaveLength(1)
  })

  it('sunucu adı bilinmiyorsa yeniden başlatma düğmesi devre dışıdır', async () => {
    const net = mockFetch({ ...routes(), 'GET /updates/apt': ok(aptView({ reboot_required: true, hostname: '' })) })
    renderIn(<AptSection />)
    expect(await screen.findByRole('button', { name: 'Yeniden Başlat' })).toBeDisabled()
    expect(stateChanging(net)).toEqual([])
  })

  it('yeniden başlatma gerekmiyorsa düğme hiç gösterilmez', async () => {
    mockFetch({ 'GET /updates/apt': ok(aptView()), 'GET /updates/summary': ok(summary(2, 0, 0)) })
    renderIn(<AptSection />)
    expect(await screen.findByText('libc6')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Yeniden Başlat' })).not.toBeInTheDocument()
  })
})

describe('SelfSection', () => {
  it('kaynak "none" iken kaynağın yapılandırılmadığını söyler ve denetlemez', async () => {
    const user = userEvent.setup()
    const net = mockFetch({
      'GET /updates/self': ok(selfView({ source: 'none', source_label: '', configured: false, checked_at: null })),
    })
    renderIn(<SelfSection />)
    expect((await screen.findAllByText('Güncelleme kaynağı yapılandırılmamış')).length).toBeGreaterThan(0)
    expect(screen.getByText(/sürüm kaynağı tanımlı değil/)).toBeInTheDocument()
    expect(screen.queryByText('MyServer güncel.')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'MyServer’ı Güncelle' })).not.toBeInTheDocument()
    const check = screen.getByRole('button', { name: 'Denetle' })
    expect(check).toBeDisabled()
    await user.click(check)
    expect(stateChanging(net)).toEqual([])
  })

  it('yüklenemeyen sürümde engel metnini gösterir ve yükleme düğmesi sunmaz', async () => {
    const blocker = 'Güncelleme kaynağı kurulum arşivinin SHA-256 özetini bildirmiyor. Doğrulanamayan bir sürüm panelden yüklenemez.'
    const net = mockFetch({
      'GET /updates/self': ok(available({ installable: false, install_blocker: blocker, asset: { ...ASSET, sha256: '' } })),
    })
    renderIn(<SelfSection />)
    expect(await screen.findByText(blocker)).toBeInTheDocument()
    expect(screen.getByText('Bu güncelleme panelden yüklenemez')).toBeInTheDocument()
    expect(screen.getByText('Kaynak bildirmedi.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'MyServer’ı Güncelle' })).not.toBeInTheDocument()
    expect(stateChanging(net)).toEqual([])
  })

  it('yüklenebilir sürümde onay ister ve denetlenen sürümü gönderir', async () => {
    const user = userEvent.setup()
    const net = mockFetch({
      'GET /updates/self': ok(available()),
      'POST /updates/self/apply': ok({ started: true, version: '1.3.0' }, 202),
    })
    renderIn(<SelfSection />)
    await user.click(await screen.findByRole('button', { name: 'MyServer’ı Güncelle' }))
    expect(stateChanging(net)).toEqual([])
    const dialog = screen.getByRole('dialog', { name: 'MyServer güncellensin mi?' })
    expect(dialog).toHaveTextContent('1.2.0')
    expect(dialog).toHaveTextContent('1.3.0')
    await user.click(within(dialog).getByRole('button', { name: 'MyServer’ı Güncelle' }))
    await waitFor(() => expect(net.find('POST /updates/self/apply')).toHaveLength(1))
    expect(net.find('POST /updates/self/apply')[0]?.body).toEqual({ version: '1.3.0', confirm: true })
  })

  it('sürüm notlarındaki işaretlemeyi HTML olarak değil düz metin olarak gösterir', async () => {
    const fired = vi.fn()
    vi.stubGlobal('__notesFired', fired)
    const notes =
      '<img src=x onerror="__notesFired()"> <script>__notesFired()</script>\n<b>kalın</b> <a href="javascript:__notesFired()">bağlantı</a> <iframe src="https://example.com"></iframe>'
    mockFetch({ 'GET /updates/self': ok(available({}, notes)) })
    const { container } = renderIn(<SelfSection />)
    const label = await screen.findByText('Sürüm notları')
    const pre = label.parentElement?.querySelector('pre') as HTMLElement
    expect(pre).not.toBeNull()
    expect(pre.textContent).toBe(notes)
    expect(pre.children).toHaveLength(0)
    expect(container.querySelector('img, script, b, iframe, a[href^="javascript"]')).toBeNull()
    expect(document.body.querySelector('script, iframe, img')).toBeNull()
    expect(screen.getByText(/<b>kalın<\/b>/)).toBeInTheDocument()
    expect(fired).not.toHaveBeenCalled()
  })

  it('son güncelleme mesajındaki işaretleme de düz metindir', async () => {
    mockFetch({
      'GET /updates/self': ok(
        selfView({ last_update: { state: 'failed', from_version: '1.1.0', to_version: '1.2.0', time: NOW, message: '<img src=x onerror=alert(1)>' } }),
      ),
    })
    const { container } = renderIn(<SelfSection />)
    expect(await screen.findByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
  })
})
