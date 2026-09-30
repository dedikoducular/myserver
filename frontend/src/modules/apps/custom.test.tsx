import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { FakeEventSource, fail, mockFetch, ok, signedIn } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import { CustomAppDialog, MAX_COMPOSE_BYTES } from './CustomAppDialog'
import AppsPage from './index'
import type { CatalogApp, CustomPreview } from './types'

// The network is faked at the fetch / EventSource boundary.

const COMPOSE = `services:
  kuma:
    image: louislam/uptime-kuma:1
    ports:
      - "3001:3001"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
`

const PREVIEW = 'POST /apps/custom/preview'
const CREATE = 'POST /apps/custom'

function catalogApp(over: Partial<CatalogApp> = {}): CatalogApp {
  return {
    slug: 'custom-uptime-kuma',
    name: 'Uptime Kuma',
    description: 'Compose dosyasından eklenen özel uygulama',
    category: 'custom',
    category_label: 'Özel',
    icon: 'custom.svg',
    version: '',
    website: '',
    images: ['louislam/uptime-kuma:1'],
    warning_level: 'danger',
    architectures: [],
    installable: true,
    unsupported_reason: '',
    installed: false,
    operation: '',
    job_id: '',
    custom: true,
    ...over,
  }
}

function preview(over: Partial<CustomPreview> = {}): CustomPreview {
  return {
    ...catalogApp(),
    long_description: '',
    notes: '',
    conversion_notes: [{ service: 'kuma', key: 'container_name', message: 'yok sayıldı; panel konteyneri kendisi adlandırır.' }],
    warnings: [{ level: 'danger', code: 'docker_socket', title: 'Docker soketine erişim', message: 'Bu uygulama Docker soketine erişir.' }],
    requires_risk_acceptance: true,
    allowed_roots: ['/data'],
    bind_address: 'all',
    publishes_ports: true,
    host_network: false,
    fields: {
      ports: [{ key: 'kuma-3001-tcp', service: 'kuma', label: 'Web arayüzü', container: 3001, protocol: 'tcp', default: 3001, value: 3001, fixed: false, web_ui: true, option: '' }],
      env: [{ key: 'ADMIN_PASSWORD', name: 'ADMIN_PASSWORD', label: 'ADMIN_PASSWORD', description: '', default: '', value: '', required: false, secret: true, generated: true, has_value: false }],
      paths: [{ key: 'kuma-backup', service: 'kuma', label: 'Klasör: /backup', description: '', target: '/backup', default: '/data/yedek', value: '/data/yedek', required: true, read_only: false }],
      options: [],
    },
    services: [{ name: 'kuma', image: 'louislam/uptime-kuma:1', depends_on: [] }],
    volumes: [{ service: 'kuma', type: 'system', source: '/var/run/docker.sock', target: '/var/run/docker.sock', label: 'Docker soketi', read_only: false }],
    installed_app: null,
    digest: 'a'.repeat(64),
    manifest_yaml: 'schema: 1\nname: Uptime Kuma\nslug: custom-uptime-kuma\n',
    ...over,
  }
}

beforeEach(() => {
  FakeEventSource.install()
  useAuth.getState().adopt(signedIn())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function renderDialog(routes: Parameters<typeof mockFetch>[0]) {
  const net = mockFetch(routes)
  const onSaved = vi.fn()
  const onClose = vi.fn()
  render(
    <MemoryRouter>
      <CustomAppDialog open onClose={onClose} onSaved={onSaved} />
    </MemoryRouter>,
  )
  return { net, onSaved, onClose }
}

const composeBox = () => screen.getByLabelText('docker-compose.yml içeriği')

describe('custom application dialog', () => {
  it('previews the conversion: services, ports, folders, settings, warnings and notes', async () => {
    const user = userEvent.setup()
    const { net } = renderDialog({ [PREVIEW]: ok(preview()) })
    await user.type(screen.getByLabelText('Uygulama adı'), 'Uptime Kuma')
    fireEvent.change(composeBox(), { target: { value: COMPOSE } })
    await user.click(screen.getByRole('button', { name: 'Önizle' }))

    await waitFor(() => expect(net.find(PREVIEW)).toHaveLength(1))
    expect(net.find(PREVIEW)[0]?.body).toEqual({ name: 'Uptime Kuma', compose: COMPOSE })
    expect(await screen.findByText('custom-uptime-kuma')).toBeInTheDocument()
    expect(screen.getAllByText('louislam/uptime-kuma:1').length).toBeGreaterThan(0)
    expect(screen.getByText('3001 → 3001/tcp')).toBeInTheDocument()
    expect(screen.getByText('/data/yedek')).toBeInTheDocument()
    expect(screen.getAllByText('/var/run/docker.sock').length).toBeGreaterThan(0)
    expect(screen.getByText('ADMIN_PASSWORD')).toBeInTheDocument()
    expect(screen.getByText('Docker soketine erişim')).toBeInTheDocument()
    expect(screen.getByText(/Bu uygulamayı kurarken bu uyarıları ayrıca kabul etmeniz istenecek/)).toBeInTheDocument()
    expect(screen.getByText(/panel konteyneri kendisi adlandırır/)).toBeInTheDocument()
    // Nothing is stored by a preview.
    expect(net.find(CREATE)).toHaveLength(0)
  })

  it('lists every problem of a refused file', async () => {
    const user = userEvent.setup()
    const message =
      'Compose dosyası kabul edilmedi (2 sorun):\n• servis "web", build: Panel görüntü derlemez.\n• servis "web", volumes: /etc bir sistem klasörüdür ve uygulamaya bağlanamaz.'
    renderDialog({ [PREVIEW]: fail(400, 'compose_refused', message) })
    fireEvent.change(composeBox(), { target: { value: 'services:\n  web:\n    build: .\n' } })
    await user.click(screen.getByRole('button', { name: 'Önizle' }))

    expect(await screen.findByText('Compose dosyası kabul edilmedi')).toBeInTheDocument()
    const items = screen.getAllByRole('listitem').map((li) => li.textContent)
    expect(items).toEqual(['servis "web", build: Panel görüntü derlemez.', 'servis "web", volumes: /etc bir sistem klasörüdür ve uygulamaya bağlanamaz.'])
    // The form stays, so the file can be corrected.
    expect(composeBox()).toHaveValue('services:\n  web:\n    build: .\n')
    expect(screen.queryByRole('button', { name: 'Kaydet' })).not.toBeInTheDocument()
  })

  it('does not send an empty file', async () => {
    const user = userEvent.setup()
    const { net } = renderDialog({})
    await user.click(screen.getByRole('button', { name: 'Önizle' }))
    expect(await screen.findByText('Compose dosyasının içeriğini girin.')).toBeInTheDocument()
    expect(net.calls).toHaveLength(0)
  })

  it('fills the text area from an uploaded file and refuses other files', async () => {
    const user = userEvent.setup({ applyAccept: false })
    const { net } = renderDialog({})
    const input = screen.getByTestId('compose-file')
    await user.upload(input, new File([COMPOSE], 'docker-compose.yml', { type: 'application/x-yaml' }))
    await waitFor(() => expect(composeBox()).toHaveValue(COMPOSE))

    await user.upload(input, new File(['x'], 'kurulum.sh', { type: 'text/x-sh' }))
    expect(await screen.findByText('Yalnızca .yml veya .yaml dosyaları yüklenebilir.')).toBeInTheDocument()
    await user.upload(input, new File(['#'.repeat(MAX_COMPOSE_BYTES + 1)], 'buyuk.yaml'))
    expect(await screen.findByText(/Dosya çok büyük/)).toBeInTheDocument()
    expect(composeBox()).toHaveValue(COMPOSE)
    expect(net.calls).toHaveLength(0)
  })

  it('saves exactly the previewed definition', async () => {
    const user = userEvent.setup()
    const saved = catalogApp()
    const { net, onSaved } = renderDialog({ [PREVIEW]: ok(preview()), [CREATE]: ok(saved, 201) })
    await user.type(screen.getByLabelText('Uygulama adı'), 'Uptime Kuma')
    fireEvent.change(composeBox(), { target: { value: COMPOSE } })
    await user.click(screen.getByRole('button', { name: 'Önizle' }))
    await user.click(await screen.findByRole('button', { name: 'Kaydet' }))

    await waitFor(() => expect(net.find(CREATE)).toHaveLength(1))
    const call = net.find(CREATE)[0]!
    expect(call.body).toEqual({ name: 'Uptime Kuma', compose: COMPOSE, digest: 'a'.repeat(64) })
    expect(call.headers['X-CSRF-Token']).toBe('csrf-session')
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(saved))
  })

  it('goes back to the form to edit after a preview', async () => {
    const user = userEvent.setup()
    renderDialog({ [PREVIEW]: ok(preview()) })
    fireEvent.change(composeBox(), { target: { value: COMPOSE } })
    await user.click(screen.getByRole('button', { name: 'Önizle' }))
    await user.click(await screen.findByRole('button', { name: 'Düzenle' }))
    expect(composeBox()).toHaveValue(COMPOSE)
  })

  it('shows the error of a failed save', async () => {
    const user = userEvent.setup()
    renderDialog({ [PREVIEW]: ok(preview()), [CREATE]: fail(409, 'custom_changed', 'Tanım önizlemeden sonra değişti. Kaydetmeden önce yeniden önizleyin.') })
    fireEvent.change(composeBox(), { target: { value: COMPOSE } })
    await user.click(screen.getByRole('button', { name: 'Önizle' }))
    await user.click(await screen.findByRole('button', { name: 'Kaydet' }))
    expect(await screen.findByText(/yeniden önizleyin/)).toBeInTheDocument()
  })
})

describe('store page', () => {
  function renderStore(apps: CatalogApp[]) {
    const net = mockFetch({
      'GET /apps/catalog': ok({ apps, categories: [{ id: 'custom', label: 'Özel' }], invalid: [], loaded_at: 1, load_error: '' }),
      'GET /apps/installed': ok({ apps: [], categories: [], docker_available: true }),
      'DELETE /apps/custom/custom-uptime-kuma': ok({ slug: 'custom-uptime-kuma' }),
    })
    render(
      <MemoryRouter initialEntries={['/apps?view=store']}>
        <AppsPage />
      </MemoryRouter>,
    )
    return net
  }

  it('offers "Özel Uygulama Ekle" to administrators and opens the dialog', async () => {
    const user = userEvent.setup()
    renderStore([catalogApp()])
    await user.click(await screen.findByRole('button', { name: 'Özel Uygulama Ekle' }))
    expect(await screen.findByRole('dialog', { name: 'Özel Uygulama Ekle' })).toBeInTheDocument()
    expect(screen.getByLabelText('docker-compose.yml içeriği')).toBeInTheDocument()
  })

  it('shows neither the button nor the delete action to other users', async () => {
    useAuth.getState().adopt(signedIn({ role: 'user' }))
    renderStore([catalogApp()])
    expect(await screen.findByText('Uptime Kuma')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Özel Uygulama Ekle' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Uptime Kuma işlemleri' })).not.toBeInTheDocument()
  })

  it('marks custom applications and deletes a definition after confirmation', async () => {
    const user = userEvent.setup()
    const net = renderStore([catalogApp()])
    const card = (await screen.findByText('Uptime Kuma')).closest('div.flex.gap-3') as HTMLElement
    expect(within(card).getByText('Özel')).toBeInTheDocument()
    await user.click(within(card).getByRole('button', { name: 'Uptime Kuma işlemleri' }))
    await user.click(screen.getByRole('menuitem', { name: 'Tanımı Sil' }))
    expect(net.find('DELETE /apps/custom/custom-uptime-kuma')).toHaveLength(0)
    const dialog = await screen.findByRole('dialog', { name: 'Uptime Kuma tanımı silinsin mi?' })
    await user.click(within(dialog).getByRole('button', { name: 'Tanımı Sil' }))
    await waitFor(() => expect(net.find('DELETE /apps/custom/custom-uptime-kuma')).toHaveLength(1))
  })

  it('offers no delete action for an installed custom application', async () => {
    renderStore([catalogApp({ installed: true })])
    expect(await screen.findByText('Uptime Kuma')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Uptime Kuma işlemleri' })).not.toBeInTheDocument()
  })
})
