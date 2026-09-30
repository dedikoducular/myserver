import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { FakeEventSource, fail, mockFetch, ok, signedIn } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import { AppDialog } from './AppDialog'
import { UninstallDialog } from './UninstallDialog'
import { AppsWidget } from './index'
import type { AppDetail, InstalledApp, Job } from './types'

// The network is faked at the fetch / EventSource boundary. Nothing here
// reaches a server; every request the components make is recorded.

function detail(over: Partial<AppDetail> = {}): AppDetail {
  return {
    slug: 'tek',
    name: 'Tek',
    description: 'Tek servisli deneme uygulaması',
    category: 'tools',
    category_label: 'Araçlar',
    icon: '',
    version: '1.0',
    website: '',
    images: ['example/tek:1.0'],
    warning_level: '',
    architectures: [],
    installable: true,
    unsupported_reason: '',
    installed: false,
    operation: '',
    job_id: '',
    long_description: '',
    notes: '',
    warnings: [],
    requires_risk_acceptance: false,
    allowed_roots: ['/data', '/media'],
    bind_address: 'all',
    publishes_ports: true,
    host_network: false,
    fields: {
      ports: [
        { key: 'web', service: 'app', label: 'Web arayüzü', container: 80, protocol: 'tcp', default: 18080, value: 18080, fixed: false, web_ui: true, option: '' },
      ],
      env: [
        { key: 'ADMIN_PASSWORD', name: 'ADMIN_PASSWORD', label: 'Yönetici parolası', description: '', default: '', value: '', required: true, secret: true, generated: false, has_value: false },
        { key: 'DB_PASSWORD', name: 'DB_PASSWORD', label: 'Veritabanı parolası', description: '', default: '', value: '', required: false, secret: true, generated: true, has_value: false },
        { key: 'TZ', name: 'TZ', label: 'Saat dilimi', description: '', default: 'Europe/Istanbul', value: 'Europe/Istanbul', required: false, secret: false, generated: false, has_value: false },
      ],
      paths: [
        { key: 'media', service: 'app', label: 'Medya klasörü', description: '', target: '/media', default: '', value: '', required: false, read_only: false },
      ],
      options: [{ key: 'rawnet', service: 'app', label: 'Ham ağ erişimi', description: '', default: false, value: false, cap_add: ['NET_RAW'] }],
    },
    services: [{ name: 'app', image: 'example/tek:1.0', depends_on: [] }],
    volumes: [{ service: 'app', type: 'volume', source: 'myserver-tek-config', target: '/config', label: '', read_only: false }],
    installed_app: null,
    ...over,
  }
}

const job: Job = {
  id: '0123456789abcdef01234567',
  slug: 'tek',
  name: 'Tek',
  kind: 'install',
  status: 'running',
  error: '',
  started_at: 1_700_000_000,
  finished_at: 0,
  last_step: '',
}

function installedApp(over: Partial<InstalledApp> = {}): InstalledApp {
  return {
    slug: 'tek',
    name: 'Tek',
    description: 'Tek servisli deneme uygulaması',
    category: 'tools',
    category_label: 'Araçlar',
    icon: '',
    version: '1.0',
    available_version: '1.0',
    manifest_available: true,
    installed_at: 1_700_000_000,
    updated_at: 1_700_000_000,
    state: 'running',
    operation: '',
    job_id: '',
    services: [],
    web_ui: null,
    ports: [],
    bind_address: 'all',
    ...over,
  }
}

const INSTALL = 'POST /apps/catalog/tek/install'
const UNINSTALL = 'POST /apps/installed/tek/uninstall'

beforeEach(() => {
  FakeEventSource.install()
  useAuth.getState().adopt(signedIn())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function renderInstall(d: AppDetail, install: Response = ok(job, 202)) {
  const net = mockFetch({ 'GET /apps/catalog/tek': ok(d), [INSTALL]: install })
  const onChanged = vi.fn()
  render(
    <MemoryRouter>
      <AppDialog slug="tek" mode="install" onClose={() => {}} onChanged={onChanged} />
    </MemoryRouter>,
  )
  return { net, onChanged }
}

const installButton = () => screen.findByRole('button', { name: 'Kurulumu Başlat' })

describe('install dialog', () => {
  it('renders the fields of the manifest detail', async () => {
    renderInstall(detail())
    expect(await screen.findByLabelText('Web arayüzü')).toHaveValue(18080)
    expect(screen.getByLabelText('Saat dilimi')).toHaveValue('Europe/Istanbul')
    expect(screen.getByLabelText('Medya klasörü')).toHaveValue('')
    expect(screen.getByLabelText('Yönetici parolası *')).toBeInTheDocument()
    expect(screen.getByLabelText('Veritabanı parolası')).toBeInTheDocument()
    expect(screen.getByRole('switch', { name: 'Ham ağ erişimi' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByText(/NET_RAW/)).toBeInTheDocument()
    expect(screen.getByText('myserver-tek-config')).toBeInTheDocument()
    expect(screen.getByText(/İzin verilen klasörler: \/data, \/media/)).toBeInTheDocument()
  })

  it('masks secret fields and never shows a stored secret', async () => {
    const d = detail({ installed: false })
    d.fields.env[0] = { ...d.fields.env[0]!, has_value: true, value: 'sunucudan-gelmemesi-gereken-deger' }
    renderInstall(d)
    const admin = await screen.findByLabelText('Yönetici parolası *')
    expect(admin).toHaveAttribute('type', 'password')
    expect(admin).toHaveValue('')
    expect(admin).toHaveAttribute('placeholder', 'Kayıtlı bir değer var')
    expect(screen.getByLabelText('Veritabanı parolası')).toHaveAttribute('type', 'password')
    expect(screen.getByLabelText('Saat dilimi')).not.toHaveAttribute('type', 'password')
    expect(document.body.innerHTML).not.toContain('sunucudan-gelmemesi-gereken-deger')
    expect(screen.getByText('Boş bırakırsanız rastgele bir değer üretilir.')).toBeInTheDocument()
  })

  it('keeps the install button disabled until the risks are accepted', async () => {
    const user = userEvent.setup()
    const { net } = renderInstall(
      detail({
        requires_risk_acceptance: true,
        warning_level: 'danger',
        warnings: [{ level: 'danger', code: 'docker_socket', title: 'Docker soketine erişim', message: 'Bu uygulama Docker soketine erişir.' }],
        fields: { ports: [], env: [], paths: [], options: [] },
      }),
    )
    const button = await installButton()
    expect(screen.getByText('Docker soketine erişim')).toBeInTheDocument()
    expect(screen.getByText('Bu uygulama Docker soketine erişir.')).toBeInTheDocument()
    expect(button).toBeDisabled()
    await user.click(button)
    expect(net.find(INSTALL)).toHaveLength(0)

    const accept = screen.getByRole('checkbox', { name: 'Uyarıları okudum, riskleri kabul ediyorum.' })
    expect(accept).not.toBeChecked()
    await user.click(accept)
    expect(button).toBeEnabled()
    await user.click(accept)
    expect(button).toBeDisabled()
    await user.click(accept)
    await user.click(button)
    await waitFor(() => expect(net.find(INSTALL)).toHaveLength(1))
    expect(net.find(INSTALL)[0]?.body).toMatchObject({ accept_risks: true })
  })

  it('does not ask for acceptance when the detail does not demand it', async () => {
    renderInstall(detail({ warnings: [{ level: 'warning', code: 'cap_add', title: 'Ek çekirdek yetkileri', message: 'NET_ADMIN' }] }))
    expect(await installButton()).toBeEnabled()
    expect(screen.getByText('Ek çekirdek yetkileri')).toBeInTheDocument()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })

  it('shows the exposure warning, and the loopback note when that option is on', async () => {
    const user = userEvent.setup()
    renderInstall(detail())
    expect(await screen.findByText('Bu portlar ağa açılır')).toBeInTheDocument()
    expect(screen.queryByText(/yalnızca 127\.0\.0\.1 adresinde yayınlanır/)).not.toBeInTheDocument()
    const loopback = screen.getByRole('switch', { name: 'Yalnızca bu sunucudan erişilsin (127.0.0.1)' })
    expect(loopback).toHaveAttribute('aria-checked', 'false')
    await user.click(loopback)
    expect(screen.getByText(/yalnızca 127\.0\.0\.1 adresinde yayınlanır/)).toBeInTheDocument()
    expect(screen.queryByText('Bu portlar ağa açılır')).not.toBeInTheDocument()
  })

  it('starts with the loopback choice the server reports', async () => {
    renderInstall(detail({ bind_address: 'loopback' }))
    expect(await screen.findByRole('switch', { name: 'Yalnızca bu sunucudan erişilsin (127.0.0.1)' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByText('Bu portlar ağa açılır')).not.toBeInTheDocument()
  })

  it('shows no exposure warning when nothing is published', async () => {
    renderInstall(detail({ publishes_ports: false, fields: { ports: [], env: [], paths: [], options: [] } }))
    await installButton()
    expect(screen.queryByText('Bu portlar ağa açılır')).not.toBeInTheDocument()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
  })

  it('warns about host networking separately', async () => {
    renderInstall(detail({ publishes_ports: false, host_network: true }))
    expect(await screen.findByText('Uygulama sunucunun ağını doğrudan kullanır')).toBeInTheDocument()
    expect(screen.queryByRole('switch', { name: /Yalnızca bu sunucudan/ })).not.toBeInTheDocument()
  })

  it('sends exactly the entered values', async () => {
    const user = userEvent.setup()
    const { net, onChanged } = renderInstall(detail())
    const port = await screen.findByLabelText('Web arayüzü')
    await user.clear(port)
    await user.type(port, '19000')
    await user.type(screen.getByLabelText('Yönetici parolası *'), 'gizli parola 1')
    await user.type(screen.getByLabelText('Medya klasörü'), '/data/medya')
    await user.click(screen.getByRole('switch', { name: 'Ham ağ erişimi' }))
    await user.click(screen.getByRole('switch', { name: 'Yalnızca bu sunucudan erişilsin (127.0.0.1)' }))
    await user.click(await installButton())

    await waitFor(() => expect(net.find(INSTALL)).toHaveLength(1))
    const call = net.find(INSTALL)[0]!
    expect(call.body).toEqual({
      ports: { web: 19000 },
      env: { ADMIN_PASSWORD: 'gizli parola 1', TZ: 'Europe/Istanbul' },
      paths: { media: '/data/medya' },
      options: { rawnet: true },
      bind_address: 'loopback',
      accept_risks: false,
    })
    expect(call.headers['X-CSRF-Token']).toBe('csrf-session')
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    // The dialog follows the job the server started.
    await waitFor(() => expect(FakeEventSource.open.some((s) => s.url.includes(`/apps/jobs/${job.id}/stream`))).toBe(true))
  })

  it('sends the defaults unchanged when nothing is edited', async () => {
    const user = userEvent.setup()
    const d = detail()
    d.fields.env[0] = { ...d.fields.env[0]!, required: false }
    const { net } = renderInstall(d)
    await user.click(await installButton())
    await waitFor(() => expect(net.find(INSTALL)).toHaveLength(1))
    expect(net.find(INSTALL)[0]?.body).toEqual({
      ports: { web: 18080 },
      env: { TZ: 'Europe/Istanbul' },
      paths: { media: '' },
      options: { rawnet: false },
      bind_address: 'all',
      accept_risks: false,
    })
  })

  it('does not send a request while a required field is empty', async () => {
    const user = userEvent.setup()
    const { net } = renderInstall(detail())
    await user.click(await installButton())
    expect(await screen.findByText('Bu alan zorunludur.')).toBeInTheDocument()
    expect(net.find(INSTALL)).toHaveLength(0)
  })

  it("shows the server's Turkish error", async () => {
    const user = userEvent.setup()
    const message = '18080/tcp portu (Web arayüzü) "eski-nginx" konteyneri tarafından kullanılıyor.'
    const { net } = renderInstall(detail(), fail(409, 'port_in_use', message))
    await user.type(await screen.findByLabelText('Yönetici parolası *'), 'gizli')
    await user.click(await installButton())
    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(net.find(INSTALL)).toHaveLength(1)
    // The form stays, with what was entered, so that the port can be changed.
    expect(screen.getByLabelText('Web arayüzü')).toHaveValue(18080)
    expect(screen.getByLabelText('Yönetici parolası *')).toHaveValue('gizli')
    expect(FakeEventSource.open.filter((s) => s.url.includes('/apps/jobs/'))).toHaveLength(0)
  })

  it('explains why an unsupported application cannot be installed', async () => {
    const reason = 'Bu uygulama sunucunuzun işlemci mimarisini (amd64) desteklemiyor. Desteklenen mimariler: arm64.'
    renderInstall(detail({ installable: false, unsupported_reason: reason, architectures: ['arm64'] }))
    expect(await screen.findByText(reason)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Kurulumu Başlat' })).not.toBeInTheDocument()
  })

  it('offers no install button to a user who is not an administrator', async () => {
    useAuth.getState().adopt(signedIn({ role: 'user' }))
    const { net } = renderInstall(detail())
    expect(await screen.findByText('Uygulamaları yalnızca yöneticiler kurabilir.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Kurulumu Başlat' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('Web arayüzü')).toBeDisabled()
    expect(net.find(INSTALL)).toHaveLength(0)
  })
})

describe('uninstall dialog', () => {
  function renderUninstall(response: Response = ok({ removed_volumes: [], kept_volumes: ['myserver-tek-config'], failed_volumes: [], kept_paths: [] })) {
    const net = mockFetch({ 'GET /apps/catalog/tek': ok(detail({ installed: true })), [UNINSTALL]: response })
    const onDone = vi.fn()
    const onClose = vi.fn()
    render(
      <MemoryRouter>
        <UninstallDialog app={installedApp()} onClose={onClose} onDone={onDone} />
      </MemoryRouter>,
    )
    return { net, onDone, onClose }
  }

  it('keeps the data by default', async () => {
    const user = userEvent.setup()
    const { net, onDone } = renderUninstall()
    expect(screen.getByRole('radio', { name: /Verileri koru/ })).toBeChecked()
    expect(screen.getByRole('radio', { name: /Veri birimlerini de sil/ })).not.toBeChecked()
    expect(screen.getByText(/Seçtiğiniz klasörler hiçbir durumda silinmez/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Devam Et' }))
    // A separate confirmation, without typing, since nothing is deleted.
    expect(net.find(UNINSTALL)).toHaveLength(0)
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'Kaldır' }))
    await waitFor(() => expect(net.find(UNINSTALL)).toHaveLength(1))
    expect(net.find(UNINSTALL)[0]?.body).toEqual({ delete_data: false, confirm: '' })
    await waitFor(() => expect(onDone).toHaveBeenCalled())
  })

  it('requires typing the slug before the data can be deleted', async () => {
    const user = userEvent.setup()
    const { net, onDone } = renderUninstall(ok({ removed_volumes: ['myserver-tek-config'], kept_volumes: [], failed_volumes: [], kept_paths: [] }))
    await user.click(screen.getByRole('radio', { name: /Veri birimlerini de sil/ }))
    expect(await screen.findByText('myserver-tek-config')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Devam Et' }))

    const confirm = await screen.findByRole('button', { name: 'Kaldır' })
    const input = screen.getByRole('textbox')
    expect(screen.getByText(/Bu işlem geri alınamaz/)).toBeInTheDocument()
    expect(confirm).toBeDisabled()
    await user.click(confirm)
    for (const wrong of ['Tek', 'te', 'tekk', 'myserver-tek', 'evet']) {
      await user.clear(input)
      await user.type(input, wrong)
      expect(confirm).toBeDisabled()
      await user.click(confirm)
      await user.type(input, '{Enter}')
    }
    expect(net.find(UNINSTALL)).toHaveLength(0)

    await user.clear(input)
    await user.type(input, 'tek')
    expect(confirm).toBeEnabled()
    await user.click(confirm)
    await waitFor(() => expect(net.find(UNINSTALL)).toHaveLength(1))
    expect(net.find(UNINSTALL)[0]?.body).toEqual({ delete_data: true, confirm: 'tek' })
    await waitFor(() => expect(onDone).toHaveBeenCalled())
  })

  it('shows the error of the server and does not report success', async () => {
    const user = userEvent.setup()
    const { onDone, onClose } = renderUninstall(fail(503, 'docker_unavailable', 'Docker servisine ulaşılamıyor.'))
    await user.click(screen.getByRole('button', { name: 'Devam Et' }))
    await user.click(await screen.findByRole('button', { name: 'Kaldır' }))
    expect(await screen.findByText('Docker servisine ulaşılamıyor.')).toBeInTheDocument()
    expect(onDone).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })
})

describe('dashboard widget', () => {
  // Names of the applications shipped in the catalog: none of them may be
  // shown unless the server reports it as installed.
  const shipped = ['Jellyfin', 'Nextcloud', 'Home Assistant', 'Pi-hole', 'Portainer', 'Immich', 'qBittorrent', 'Vaultwarden', 'AdGuard', 'Frigate', 'Plex']

  function renderWidget() {
    return render(
      <MemoryRouter>
        <AppsWidget />
      </MemoryRouter>,
    )
  }

  it('shows the empty state when nothing is installed, and no sample apps', async () => {
    const net = mockFetch({ 'GET /apps/installed': ok({ apps: [], categories: [], docker_available: true }) })
    renderWidget()
    expect(await screen.findByText('Henüz kurulu uygulama yok')).toBeInTheDocument()
    for (const name of shipped) expect(screen.queryByText(new RegExp(name, 'i'))).not.toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'Uygulama Kur' })[0]).toHaveAttribute('href', '/apps?view=store')
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()
    // Only the list of installed applications is requested.
    expect(net.calls.map((c) => `${c.method} ${c.path}`)).toEqual(['GET /apps/installed'])
  })

  it('shows nothing invented when the request fails', async () => {
    mockFetch({ 'GET /apps/installed': fail(500, 'internal_error', 'Beklenmeyen bir sunucu hatası oluştu.') })
    renderWidget()
    expect(await screen.findByText('Beklenmeyen bir sunucu hatası oluştu.')).toBeInTheDocument()
    for (const name of shipped) expect(screen.queryByText(new RegExp(name, 'i'))).not.toBeInTheDocument()
    expect(screen.queryByText('Henüz kurulu uygulama yok')).not.toBeInTheDocument()
  })

  it('tells a user without the admin role who can install', async () => {
    useAuth.getState().adopt(signedIn({ role: 'user' }))
    mockFetch({ 'GET /apps/installed': ok({ apps: [], categories: [], docker_available: true }) })
    renderWidget()
    expect(await screen.findByText('Uygulamaları yalnızca yöneticiler kurabilir.')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Uygulama Kur' })).not.toBeInTheDocument()
  })

  it('shows exactly the applications the server reports', async () => {
    mockFetch({
      'GET /apps/installed': ok({
        apps: [installedApp({ slug: 'ozel', name: 'Özel Uygulamam' })],
        categories: [{ id: 'tools', label: 'Araçlar' }],
        docker_available: true,
      }),
    })
    renderWidget()
    expect(await screen.findByText('Özel Uygulamam')).toBeInTheDocument()
    expect(screen.queryByText('Henüz kurulu uygulama yok')).not.toBeInTheDocument()
    for (const name of shipped) expect(screen.queryByText(new RegExp(name, 'i'))).not.toBeInTheDocument()
    expect(within(screen.getByRole('tablist')).getByRole('tab', { name: /Araçlar/ })).toBeInTheDocument()
  })
})
