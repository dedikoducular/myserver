import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { FakeEventSource, fail, mockFetch, ok, signedIn, type FetchMock } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import { useUI } from '@/stores/ui'
import BackupPage from './index'
import { PassphraseDialog, UploadDialog } from './dialogs'
import { RestoreDialog } from './RestoreDialog'
import { JobCard } from './parts'
import { BackupSettingsSection } from './settings'
import type { AppSummary, BackupRecord, EncryptionInfo, Job, Overview, Preview } from './types'

// The network is faked at the fetch / EventSource / XMLHttpRequest boundary.

const NOW = Math.floor(Date.now() / 1000)
const SECRET = 'cok-gizli-parola-123'

function record(over: Partial<BackupRecord> = {}): BackupRecord {
  return {
    id: 7,
    slug: 'fotolar',
    app_name: 'Fotolar',
    app_version: '1.2.3',
    file_name: 'fotolar-20260101T030000Z.tar.gz.enc',
    size: 5 * 1024 * 1024,
    created_at: NOW - 3600,
    duration: 12,
    status: 'success',
    consistency: 'stopped',
    encrypted: true,
    includes_binds: true,
    trigger: 'manual',
    error: '',
    warnings: [],
    verified_at: 0,
    verify_ok: false,
    file_missing: false,
    ...over,
  }
}

function encryption(over: Partial<EncryptionInfo> = {}): EncryptionInfo {
  return { enabled: false, set_at: 0, cipher: 'AES-256-GCM', kdf: 'Argon2id', key_file: '/var/lib/myserver/backup-key.json', error: '', ...over }
}

function app(over: Partial<AppSummary> = {}): AppSummary {
  const rec = record()
  return {
    slug: 'fotolar',
    name: 'Fotolar',
    version: '1.2.3',
    volumes: [{ service: 'app', type: 'volume', source: 'myserver-fotolar-data', target: '/data', read_only: false }],
    backup_count: 1,
    total_size: rec.size,
    last_backup: rec,
    last_success: rec,
    schedule: null,
    ...over,
  }
}

function overview(over: Partial<Overview> = {}): Overview {
  return {
    apps_available: true,
    apps_error: '',
    apps: [app()],
    orphans: [],
    dir: '/var/lib/myserver/backups',
    free_bytes: 50 * 2 ** 30,
    total_bytes: 100 * 2 ** 30,
    used_bytes: 5 * 2 ** 20,
    encryption: encryption(),
    jobs: [],
    ...over,
  }
}

function preview(over: Partial<Preview> = {}): Preview {
  return {
    slug: 'fotolar',
    app_name: 'Fotolar',
    app_version: '1.2.3',
    created_at: NOW - 3600,
    panel_version: '1.0.0',
    consistency: 'stopped',
    encrypted: true,
    images: ['ghcr.io/ornek/fotolar:1.2.3'],
    entries: [
      { kind: 'volume', source: 'myserver-fotolar-data', service: 'app', target: '/data', read_only: false, files: 120, bytes: 4096, exists: true },
      { kind: 'bind', source: '/data/medya', service: 'app', target: '/medya', read_only: false, files: 3, bytes: 1024, exists: false },
    ],
    untouched: [],
    warnings: [],
    apps_available: true,
    app_installed: true,
    app_running: true,
    installed_version: '1.2.4',
    secret_count: 2,
    ...over,
  }
}

function job(over: Partial<Job> = {}): Job {
  return {
    id: 'job-1',
    kind: 'verify',
    slug: 'fotolar',
    name: 'Fotolar',
    trigger: 'manual',
    backup_id: 7,
    status: 'running',
    step: 'Yedek doğrulanıyor',
    error: '',
    bytes_done: 0,
    bytes_total: 0,
    cancellable: true,
    cancel_requested: false,
    started_at: NOW,
    finished_at: 0,
    logs: [],
    result: null,
    ...over,
  }
}

function page(ov: Overview, records: BackupRecord[] = [record()], extra: Parameters<typeof mockFetch>[0] = {}): FetchMock {
  return mockFetch({
    'GET /backup/overview': ok(ov),
    'GET /backup/backups': ok(records),
    'GET /settings': ok({}),
    ...extra,
  })
}

function renderIn(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>)
}

function stateChanging(net: FetchMock) {
  return net.calls.filter((c) => c.method !== 'GET')
}

function passwordInputs(root: HTMLElement = document.body): HTMLInputElement[] {
  return [...root.querySelectorAll<HTMLInputElement>('input[type="password"]')]
}

/** No input of the document, whatever its type, may hold the secret. */
function expectSecretGone(secret = SECRET) {
  for (const input of document.querySelectorAll('input')) expect(input.value).not.toContain(secret)
  expect(document.body.textContent).not.toContain(secret)
}

beforeEach(() => {
  FakeEventSource.install()
  useAuth.getState().adopt(signedIn())
  useUI.setState({ toasts: [] })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Yedekleme sayfası', () => {
  it('yedeklerin gizli bilgi içerdiğini ve şifrelenmediğini açıkça söyler', async () => {
    const net = page(overview({ encryption: encryption({ enabled: false }) }))
    renderIn(<BackupPage />)
    expect(await screen.findByText('Yedekler gizli bilgi içerir')).toBeInTheDocument()
    expect(screen.getByText(/veritabanı parolaları gibi gizli değerlerini ve tüm verilerini içerir/)).toBeInTheDocument()
    expect(screen.getByText('Yedekler şifrelenmiyor')).toBeInTheDocument()
    expect(screen.getByText(/şifresiz olarak yazılıyor/)).toBeInTheDocument()
    expect(screen.queryByText('Yeni yedekler şifreleniyor')).not.toBeInTheDocument()
    expect(stateChanging(net)).toEqual([])
  })

  it('şifreleme açıkken bunu ve kullanılan yöntemi söyler', async () => {
    page(overview({ encryption: encryption({ enabled: true, set_at: NOW }) }))
    renderIn(<BackupPage />)
    expect(await screen.findByText('Yeni yedekler şifreleniyor')).toBeInTheDocument()
    expect(screen.getByText(/AES-256-GCM/)).toBeInTheDocument()
    expect(screen.getByText(/Argon2id/)).toBeInTheDocument()
    expect(screen.getByText('Yedekler gizli bilgi içerir')).toBeInTheDocument()
    expect(screen.queryByText('Yedekler şifrelenmiyor')).not.toBeInTheDocument()
  })

  it('anahtar dosyası okunamıyorsa şifreli olduğunu iddia etmez', async () => {
    page(overview({ encryption: encryption({ enabled: false, error: 'Şifreleme anahtarı dosyası okunamıyor. Parolayı yeniden belirleyin.' }) }))
    renderIn(<BackupPage />)
    expect(await screen.findByText('Şifreleme anahtarı okunamıyor')).toBeInTheDocument()
    expect(screen.getByText(/Parolayı yeniden belirleyin/)).toBeInTheDocument()
    expect(screen.queryByText('Yeni yedekler şifreleniyor')).not.toBeInTheDocument()
  })

  it('her yedeğin şifreli olup olmadığını gösterir', async () => {
    page(overview(), [record({ id: 1, encrypted: true }), record({ id: 2, encrypted: false, file_name: 'fotolar-20260102T030000Z.tar.gz' })])
    renderIn(<BackupPage />)
    const table = await screen.findByRole('table')
    const rows = within(table).getAllByRole('row').slice(1)
    expect(within(rows[0]!).getByText('Şifreli')).toBeInTheDocument()
    expect(within(rows[1]!).getByText('Şifresiz')).toBeInTheDocument()
  })

  it('kurulu uygulama yokken boş durumu gösterir', async () => {
    const net = page(overview({ apps: [] }), [])
    renderIn(<BackupPage />)
    expect(await screen.findByText('Kurulu uygulama yok')).toBeInTheDocument()
    expect(screen.getByText(/Önce Uygulamalar sayfasından bir uygulama kurun/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Uygulamalara Git' })).toHaveAttribute('href', '/apps')
    expect(screen.queryByRole('button', { name: 'Şimdi Yedekle' })).not.toBeInTheDocument()
    expect(screen.getByText('Yedek yok')).toBeInTheDocument()
    expect(stateChanging(net)).toEqual([])
  })

  it('uygulama modülü kullanılamıyorken boş durum yerine hatayı gösterir', async () => {
    page(overview({ apps_available: false, apps_error: 'Uygulama modülü kullanılamıyor.', apps: [] }), [])
    renderIn(<BackupPage />)
    expect(await screen.findByText('Uygulama modülü kullanılamıyor.')).toBeInTheDocument()
    expect(screen.queryByText('Kurulu uygulama yok')).not.toBeInTheDocument()
  })

  it('yükleme hatasını gösterir ve yeniden denemeye izin verir', async () => {
    mockFetch({
      'GET /backup/overview': fail(502, 'backup_error', 'Docker servisine ulaşılamıyor.'),
      'GET /backup/backups': ok([]),
      'GET /settings': ok({}),
    })
    renderIn(<BackupPage />)
    expect(await screen.findByText('Docker servisine ulaşılamıyor.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Tekrar|Yeniden/ })).toBeInTheDocument()
  })

  it('yönetici olmayan kullanıcıya hiçbir istek göndermez', async () => {
    useAuth.getState().adopt(signedIn({ role: 'user' }))
    const net = page(overview())
    renderIn(<BackupPage />)
    expect(screen.getByText('Yönetici yetkisi gerekiyor')).toBeInTheDocument()
    expect(screen.getByText(/gizli bilgilerini içerdiği için/)).toBeInTheDocument()
    await new Promise((r) => setTimeout(r, 20))
    expect(net.calls).toEqual([])
    expect(FakeEventSource.instances).toEqual([])
  })

  it('doğrulamada önce sunucudaki anahtarı dener, parolayı yalnızca gerekirse sorar ve yanlış parolayı Türkçe bildirir', async () => {
    const user = userEvent.setup()
    const net = page(overview(), [record()], {
      'POST /backup/backups/7/verify': (c) => {
        const pass = (c.body as { passphrase: string }).passphrase
        if (pass === '') return fail(400, 'passphrase_required', 'Bu yedek şifreli. Açmak için yedeğin alındığı sırada geçerli olan parolayı girin.')
        if (pass !== SECRET) return fail(400, 'wrong_passphrase', 'Parola yanlış: yedek bu parolayla açılamıyor.')
        return ok(job(), 202)
      },
    })
    renderIn(<BackupPage />)
    const table = await screen.findByRole('table')
    await user.click(within(table).getByRole('button', { name: 'Doğrula' }))

    const dialog = await screen.findByRole('dialog')
    expect(net.find('POST /backup/backups/7/verify')[0]!.body).toEqual({ passphrase: '' })
    const input = within(dialog).getByLabelText('Yedeğin parolası') as HTMLInputElement
    expect(input.type).toBe('password')
    expect(input.getAttribute('autocomplete')).toBe('off')
    expect(within(dialog).getByText(/Girdiğiniz parola her zaman denetlenir/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Parola kaydedilmez/)).toBeInTheDocument()

    await user.type(input, 'yanlis-parola-999')
    await user.click(within(dialog).getByRole('button', { name: 'Devam' }))
    expect(await screen.findByText('Parola yanlış: yedek bu parolayla açılamıyor.')).toBeInTheDocument()
    expect((screen.getByLabelText('Yedeğin parolası') as HTMLInputElement).value).toBe('')
    expectSecretGone('yanlis-parola-999')

    await user.type(screen.getByLabelText('Yedeğin parolası'), SECRET)
    await user.click(screen.getByRole('button', { name: 'Devam' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(net.find('POST /backup/backups/7/verify').map((c) => c.body)).toEqual([
      { passphrase: '' },
      { passphrase: 'yanlis-parola-999' },
      { passphrase: SECRET },
    ])
    expectSecretGone()
    for (const c of net.calls) expect(c.query.toString()).not.toContain(SECRET)
  })

  it('bozuk yedek hatasını Türkçe gösterir', async () => {
    const user = userEvent.setup()
    page(overview(), [record({ encrypted: false })], {
      'POST /backup/backups/7/verify': fail(400, 'invalid_backup', 'Şifreli yedek bozuk, eksik veya değiştirilmiş; doğrulama başarısız.'),
    })
    renderIn(<BackupPage />)
    const table = await screen.findByRole('table')
    await user.click(within(table).getByRole('button', { name: 'Doğrula' }))
    await waitFor(() => expect(useUI.getState().toasts.map((x) => x.message)).toContain('Şifreli yedek bozuk, eksik veya değiştirilmiş; doğrulama başarısız.'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('PassphraseDialog', () => {
  it('parola alanı gizlidir ve gönderilince boşaltılır', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    render(<PassphraseDialog onClose={() => {}} onSubmit={onSubmit} />)
    const input = screen.getByLabelText('Yedeğin parolası') as HTMLInputElement
    expect(input.type).toBe('password')
    expect(screen.getByRole('button', { name: 'Devam' })).toBeDisabled()
    await user.type(input, SECRET + '{Enter}')
    expect(onSubmit).toHaveBeenCalledExactlyOnceWith(SECRET)
    expect(input.value).toBe('')
    expectSecretGone()
  })
})

describe('RestoreDialog', () => {
  function setup(opts: { jobs?: Job[]; routes?: Parameters<typeof mockFetch>[0]; backups?: BackupRecord[] } = {}) {
    const net = mockFetch(opts.routes ?? {})
    const onChanged = vi.fn()
    const onClose = vi.fn()
    const props = { name: 'Fotolar', backups: opts.backups ?? [record()], onClose, onChanged }
    const view = render(<RestoreDialog {...props} jobs={opts.jobs ?? []} />)
    const setJobs = (jobs: Job[]) => view.rerender(<RestoreDialog {...props} jobs={jobs} />)
    return { net, onChanged, onClose, setJobs }
  }

  /** Brings the dialog to the review step of a verified backup. */
  async function toReview(user: ReturnType<typeof userEvent.setup>, p: Preview, routes: Parameters<typeof mockFetch>[0] = {}) {
    const s = setup({ routes: { 'POST /backup/backups/7/verify': ok(job(), 202), ...routes } })
    await user.click(screen.getByRole('button', { name: 'Doğrula ve İncele' }))
    await screen.findByText(/Yedek doğrulanıyor\. Dosyanın tamamı okunuyor/)
    s.setJobs([job({ status: 'success', finished_at: NOW, result: p })])
    await screen.findByText('Yedek okunabilir durumda ve sağlama toplamları eşleşiyor.')
    return s
  }

  it('doğrulanmadan geri yükleme başlatılamaz', async () => {
    const { net } = setup()
    expect(screen.getByRole('button', { name: 'Doğrula ve İncele' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'Geri Yüklemeyi Başlat' })).not.toBeInTheDocument()
    expect(screen.getByText('fotolar-20260101T030000Z.tar.gz.enc')).toBeInTheDocument()
    expect(screen.getByText('Şifreli')).toBeInTheDocument()
    expect(net.calls).toEqual([])
  })

  it('nelerin üzerine yazılacağını gösterir', async () => {
    const user = userEvent.setup()
    await toReview(user, preview())
    expect(screen.getByText('myserver-fotolar-data')).toBeInTheDocument()
    expect(screen.getByText('/data/medya')).toBeInTheDocument()
    expect(screen.getByText('Mevcut içerik silinip üzerine yazılacak')).toBeInTheDocument()
    expect(screen.getByText('Yeniden oluşturulacak')).toBeInTheDocument()
    expect(screen.getByText(/120 dosya/)).toBeInTheDocument()
    expect(screen.getByText(/2 gizli değer/)).toBeInTheDocument()
    expect(screen.getByText(/Yedekteki sürüm: 1\.2\.3/)).toBeInTheDocument()
    expect(screen.getByText(/Kurulu sürüm: 1\.2\.4/)).toBeInTheDocument()
    expect(screen.getByText(/Uygulama durdurulacak, verileri yedektekilerle değiştirilecek/)).toBeInTheDocument()
    expect(screen.getByText(/Veriler yazılmaya başladıktan sonra işlem iptal edilemez/)).toBeInTheDocument()
    // Secret values themselves are never part of the preview.
    expect(document.body.textContent).not.toMatch(/DB_PASSWORD/)
  })

  it('uygulama kimliği yazılmadan geri yükleme isteği gönderilmez', async () => {
    const user = userEvent.setup()
    const { net } = await toReview(user, preview(), { 'POST /backup/backups/7/restore': ok(job({ id: 'job-2', kind: 'restore' }), 202) })
    await user.click(screen.getByRole('button', { name: 'Geri Yüklemeyi Başlat' }))
    const confirm = await screen.findByRole('dialog', { name: 'Geri yüklemeyi onaylayın' })
    expect(within(confirm).getByText(/şu anki verileri silinecek/)).toBeInTheDocument()
    expect(within(confirm).getByText(/Veriler yazılmaya başladıktan sonra işlem iptal edilemez/)).toBeInTheDocument()
    const go = within(confirm).getByRole('button', { name: 'Geri Yükle' })
    expect(go).toBeDisabled()

    const field = within(confirm).getByRole('textbox')
    for (const wrong of ['Fotolar', 'fotola', 'fotolarr', 'evet']) {
      await user.clear(field)
      await user.type(field, wrong)
      expect(go).toBeDisabled()
      await user.keyboard('{Enter}')
    }
    expect(net.find('POST /backup/backups/7/restore')).toEqual([])

    await user.clear(field)
    await user.type(field, 'fotolar')
    expect(go).toBeEnabled()
    await user.click(go)
    await waitFor(() => expect(net.find('POST /backup/backups/7/restore')).toHaveLength(1))
    expect(net.find('POST /backup/backups/7/restore')[0]!.body).toEqual({
      confirm: 'fotolar',
      passphrase: '',
      safety_backup: true,
      restore_binds: true,
    })
  })

  it('güvenlik yedeği kapatılınca geri alınamayacağını söyler', async () => {
    const user = userEvent.setup()
    const { net } = await toReview(user, preview(), { 'POST /backup/backups/7/restore': ok(job({ id: 'job-2', kind: 'restore' }), 202) })
    await user.click(screen.getByRole('switch', { name: 'Önce mevcut durumun güvenlik yedeğini al' }))
    expect(screen.getByText('Güvenlik yedeği olmadan geri yükleme geri alınamaz.')).toBeInTheDocument()
    await user.click(screen.getByRole('switch', { name: 'Sunucu klasörlerini de geri yükle' }))
    expect(screen.getByText('Dahil değil')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Geri Yüklemeyi Başlat' }))
    const confirm = await screen.findByRole('dialog', { name: 'Geri yüklemeyi onaylayın' })
    expect(within(confirm).getByText(/Güvenlik yedeği olmadan geri yükleme geri alınamaz/)).toBeInTheDocument()
    await user.type(within(confirm).getByRole('textbox'), 'fotolar')
    await user.click(within(confirm).getByRole('button', { name: 'Geri Yükle' }))
    await waitFor(() => expect(net.find('POST /backup/backups/7/restore')).toHaveLength(1))
    expect(net.find('POST /backup/backups/7/restore')[0]!.body).toMatchObject({ safety_backup: false, restore_binds: false })
  })

  it('parolayı yalnızca gerekince sorar, gizli alanda tutar ve kullanıldıktan sonra siler', async () => {
    const user = userEvent.setup()
    const s = setup({
      routes: {
        'POST /backup/backups/7/verify': (c) => {
          const pass = (c.body as { passphrase: string }).passphrase
          if (pass === '') return fail(400, 'passphrase_required', 'Bu yedek şifreli. Açmak için yedeğin alındığı sırada geçerli olan parolayı girin.')
          if (pass !== SECRET) return fail(400, 'wrong_passphrase', 'Parola yanlış: yedek bu parolayla açılamıyor.')
          return ok(job(), 202)
        },
        'POST /backup/backups/7/restore': ok(job({ id: 'job-2', kind: 'restore' }), 202),
      },
    })
    expect(passwordInputs()).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'Doğrula ve İncele' }))
    expect(await screen.findByText(/Bu yedek şifreli/)).toBeInTheDocument()
    const input = screen.getByLabelText('Yedeğin parolası') as HTMLInputElement
    expect(input.type).toBe('password')
    expect(screen.getByRole('button', { name: 'Doğrula ve İncele' })).toBeDisabled()

    await user.type(input, 'yanlis-parola-999')
    await user.click(screen.getByRole('button', { name: 'Doğrula ve İncele' }))
    expect(await screen.findByText('Parola yanlış: yedek bu parolayla açılamıyor.')).toBeInTheDocument()
    expect((screen.getByLabelText('Yedeğin parolası') as HTMLInputElement).value).toBe('')

    await user.type(screen.getByLabelText('Yedeğin parolası'), SECRET)
    await user.click(screen.getByRole('button', { name: 'Doğrula ve İncele' }))
    await screen.findByText(/Yedek doğrulanıyor\. Dosyanın tamamı okunuyor/)
    expectSecretGone()
    s.setJobs([job({ status: 'success', finished_at: NOW, result: preview() })])
    await screen.findByText('Yedek okunabilir durumda ve sağlama toplamları eşleşiyor.')

    await user.click(screen.getByRole('button', { name: 'Geri Yüklemeyi Başlat' }))
    const confirm = await screen.findByRole('dialog', { name: 'Geri yüklemeyi onaylayın' })
    await user.type(within(confirm).getByRole('textbox'), 'fotolar')
    await user.click(within(confirm).getByRole('button', { name: 'Geri Yükle' }))
    await waitFor(() => expect(s.net.find('POST /backup/backups/7/restore')).toHaveLength(1))
    expect(s.net.find('POST /backup/backups/7/restore')[0]!.body).toMatchObject({ confirm: 'fotolar', passphrase: SECRET })
    await screen.findByText(/İşlemler sunucuda çalışır/)
    expectSecretGone()

    // A second request from this dialog would not carry the passphrase.
    s.net.calls.length = 0
    expect(passwordInputs()).toHaveLength(0)
  })

  it('doğrulama başarısız olursa hatayı Türkçe gösterir ve geri yüklemeye geçmez', async () => {
    const user = userEvent.setup()
    const s = setup({ routes: { 'POST /backup/backups/7/verify': ok(job(), 202) } })
    await user.click(screen.getByRole('button', { name: 'Doğrula ve İncele' }))
    await screen.findByText(/Yedek doğrulanıyor\. Dosyanın tamamı okunuyor/)
    s.setJobs([job({ status: 'failed', finished_at: NOW, error: 'Sağlama toplamı uyuşmuyor: myserver-fotolar-data. Yedek dosyası bozulmuş veya değiştirilmiş.' })])
    expect(await screen.findByText(/Sağlama toplamı uyuşmuyor/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Geri Yüklemeyi Başlat' })).not.toBeInTheDocument()
    expect(stateChanging(s.net).map((c) => c.path)).toEqual(['/backup/backups/7/verify'])
  })

  it('sunucunun reddettiği isteğin hatasını onay penceresinde gösterir', async () => {
    const user = userEvent.setup()
    await toReview(user, preview(), {
      'POST /backup/backups/7/restore': fail(400, 'invalid_backup', 'Yedek dosyası güvenli olmayan bir kayıt içeriyor (parent directory reference). İşlem reddedildi.'),
    })
    await user.click(screen.getByRole('button', { name: 'Geri Yüklemeyi Başlat' }))
    const confirm = await screen.findByRole('dialog', { name: 'Geri yüklemeyi onaylayın' })
    await user.type(within(confirm).getByRole('textbox'), 'fotolar')
    await user.click(within(confirm).getByRole('button', { name: 'Geri Yükle' }))
    expect(await within(confirm).findByText(/güvenli olmayan bir kayıt içeriyor/)).toBeInTheDocument()
    expect(screen.queryByText('Geri yükleme sürüyor')).not.toBeInTheDocument()
  })

  it('geri yüklenebilir yedek yoksa bunu söyler', () => {
    setup({ backups: [] })
    expect(screen.getByText('Bu uygulamanın geri yüklenebilir bir yedeği yok.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Doğrula ve İncele' })).toBeDisabled()
  })
})

describe('JobCard', () => {
  it('veri yazılırken geri yüklemenin iptal edilemeyeceğini açıklar ve iptal düğmesini göstermez', () => {
    const net = mockFetch({})
    render(<JobCard job={job({ kind: 'restore', cancellable: false, step: 'Veri birimi geri yükleniyor: myserver-fotolar-data' })} />)
    expect(screen.getByText(/Bu işlem artık iptal edilemez: verilerin üzerine yazılıyor/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'İptal Et' })).not.toBeInTheDocument()
    expect(net.calls).toEqual([])
  })

  it('iptal edilebilir işte düğmeyi gösterir ve sunucunun reddini bildirir', async () => {
    const user = userEvent.setup()
    const net = mockFetch({
      'POST /backup/jobs/job-1/cancel': fail(409, 'conflict', 'Bu işlem artık iptal edilemez: veriler yazılmaya başlandı.'),
    })
    render(<JobCard job={job({ kind: 'restore' })} />)
    expect(screen.queryByText(/artık iptal edilemez/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'İptal Et' }))
    await waitFor(() => expect(useUI.getState().toasts.map((x) => x.message)).toContain('Bu işlem artık iptal edilemez: veriler yazılmaya başlandı.'))
    expect(net.find('POST /backup/jobs/job-1/cancel')).toHaveLength(1)
  })
})

describe('Şifreleme ayarları', () => {
  function settingsPage(enc: EncryptionInfo, extra: Parameters<typeof mockFetch>[0] = {}) {
    return mockFetch({
      'GET /settings': ok({}),
      'GET /backup/overview': ok(overview({ encryption: enc })),
      ...extra,
    })
  }

  it('parola alanları gizlidir, parola kaydedilince boşaltılır', async () => {
    const user = userEvent.setup()
    const net = settingsPage(encryption(), { 'PUT /backup/encryption': ok(encryption({ enabled: true, set_at: NOW })) })
    renderIn(<BackupSettingsSection />)
    const first = (await screen.findByLabelText('Yeni parola')) as HTMLInputElement
    const second = screen.getByLabelText('Yeni parola (tekrar)') as HTMLInputElement
    expect(first.type).toBe('password')
    expect(second.type).toBe('password')
    expect(screen.getByText(/Parolanın kendisi hiçbir yere kaydedilmez/)).toBeInTheDocument()
    expect(screen.getByText(/sunucuyu ele geçirmiş birine karşı koruma sağlamaz/)).toBeInTheDocument()

    await user.type(first, 'kisa')
    await user.type(second, 'kisa')
    await user.click(screen.getByRole('button', { name: 'Parolayı Belirle' }))
    expect(screen.getByText('Parola en az 12 karakter olmalıdır.')).toBeInTheDocument()
    await user.clear(first)
    await user.clear(second)
    await user.type(first, SECRET)
    await user.type(second, SECRET + 'x')
    await user.click(screen.getByRole('button', { name: 'Parolayı Belirle' }))
    expect(screen.getByText('Parolalar aynı değil.')).toBeInTheDocument()
    expect(stateChanging(net)).toEqual([])

    await user.clear(second)
    await user.type(second, SECRET)
    await user.click(screen.getByRole('button', { name: 'Parolayı Belirle' }))
    await waitFor(() => expect(net.find('PUT /backup/encryption')).toHaveLength(1))
    expect(net.find('PUT /backup/encryption')[0]!.body).toEqual({ passphrase: SECRET })
    await waitFor(() => expect(first.value).toBe(''))
    expect(second.value).toBe('')
    expectSecretGone()
    expect(useUI.getState().toasts.map((x) => x.message).join(' ')).not.toContain(SECRET)
  })

  it('sunucu hatasını gösterir', async () => {
    const user = userEvent.setup()
    settingsPage(encryption(), { 'PUT /backup/encryption': fail(409, 'conflict', 'Bir yedekleme işlemi sürerken parola değiştirilemez.') })
    renderIn(<BackupSettingsSection />)
    await user.type(await screen.findByLabelText('Yeni parola'), SECRET)
    await user.type(screen.getByLabelText('Yeni parola (tekrar)'), SECRET)
    await user.click(screen.getByRole('button', { name: 'Parolayı Belirle' }))
    expect(await screen.findByText('Bir yedekleme işlemi sürerken parola değiştirilemez.')).toBeInTheDocument()
  })
})

/* ---------- upload ---------- */

class FakeXHR {
  static instances: FakeXHR[] = []
  static respond: (xhr: FakeXHR) => { status: number; body: unknown } = () => ({ status: 500, body: null })
  upload: { onprogress: ((e: ProgressEvent) => void) | null } = { onprogress: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  onabort: (() => void) | null = null
  withCredentials = false
  status = 0
  responseText = ''
  method = ''
  url = ''
  headers: Record<string, string> = {}
  body: unknown = null
  constructor() {
    FakeXHR.instances.push(this)
  }
  open(method: string, url: string) {
    this.method = method
    this.url = url
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v
  }
  abort() {
    this.onabort?.()
  }
  send(body: unknown) {
    this.body = body
    const r = FakeXHR.respond(this)
    this.status = r.status
    this.responseText = JSON.stringify(r.body)
    queueMicrotask(() => this.onload?.())
  }
}

describe('UploadDialog', () => {
  beforeEach(() => {
    FakeXHR.instances = []
    vi.stubGlobal('XMLHttpRequest', FakeXHR)
  })

  async function pick(user: ReturnType<typeof userEvent.setup>) {
    const file = new File([new Uint8Array([0x1f, 0x8b, 1, 2, 3])], 'yedek.tar.gz.enc', { type: 'application/octet-stream' })
    await user.upload(screen.getByLabelText('Yedek dosyası'), file)
    await user.click(screen.getByRole('button', { name: 'Yükle ve Doğrula' }))
  }

  it('MyServer yedeği olmayan dosyanın reddini Türkçe gösterir', async () => {
    const user = userEvent.setup({ applyAccept: false })
    const net = mockFetch({})
    FakeXHR.respond = () => ({ status: 400, body: { success: false, data: null, error: { code: 'invalid_backup', message: 'Dosya bir MyServer yedeği değil.' } } })
    render(<UploadDialog jobs={[]} onClose={() => {}} onImported={() => {}} />)
    expect(screen.getByText(/Yalnızca kaynağına güvendiğiniz yedekleri yükleyin/)).toBeInTheDocument()
    await pick(user)
    expect(await screen.findByText('Dosya bir MyServer yedeği değil.')).toBeInTheDocument()
    expect(screen.getByText('Dosya kabul edilmedi')).toBeInTheDocument()
    expect(FakeXHR.instances[0]!.headers['X-CSRF-Token']).toBe('csrf-session')
    expect(net.calls).toEqual([])
  })

  it('başka parolayla şifrelenmiş dosyada parolayı gizli alanda sorar ve kullandıktan sonra siler', async () => {
    const user = userEvent.setup({ applyAccept: false })
    const net = mockFetch({
      'POST /backup/imports': (c) => {
        const pass = (c.body as { passphrase: string }).passphrase
        if (pass !== SECRET) return fail(400, 'wrong_passphrase', 'Parola yanlış: yedek bu parolayla açılamıyor.')
        return ok(job({ id: 'job-3', kind: 'import' }), 202)
      },
    })
    FakeXHR.respond = () => ({
      status: 200,
      body: { success: true, data: { upload_id: 'a'.repeat(24), size: 5, encrypted: true, needs_passphrase: true }, error: null },
    })
    render(<UploadDialog jobs={[]} onClose={() => {}} onImported={() => {}} />)
    await pick(user)
    const input = (await screen.findByLabelText('Yedeğin parolası')) as HTMLInputElement
    expect(input.type).toBe('password')
    expect(net.calls).toEqual([])

    await user.type(input, 'yanlis-parola-999')
    await user.click(screen.getByRole('button', { name: 'Devam' }))
    expect(await screen.findByText('Parola yanlış: yedek bu parolayla açılamıyor.')).toBeInTheDocument()
    expect((screen.getByLabelText('Yedeğin parolası') as HTMLInputElement).value).toBe('')

    await user.type(screen.getByLabelText('Yedeğin parolası'), SECRET)
    await user.click(screen.getByRole('button', { name: 'Devam' }))
    await waitFor(() => expect(net.find('POST /backup/imports')).toHaveLength(2))
    expect(net.find('POST /backup/imports')[1]!.body).toEqual({ upload_id: 'a'.repeat(24), passphrase: SECRET })
    await waitFor(() => expect(passwordInputs()).toHaveLength(0))
    expectSecretGone()
  })

  it('sunucudaki anahtarla açılan dosyada parola sormaz; doğrulama hatasını gösterir', async () => {
    const user = userEvent.setup({ applyAccept: false })
    const net = mockFetch({ 'POST /backup/imports': ok(job({ id: 'job-3', kind: 'import' }), 202) })
    FakeXHR.respond = () => ({
      status: 200,
      body: { success: true, data: { upload_id: 'b'.repeat(24), size: 5, encrypted: true, needs_passphrase: false }, error: null },
    })
    const props = { onClose: () => {}, onImported: vi.fn() }
    const view = render(<UploadDialog jobs={[]} {...props} />)
    await pick(user)
    await waitFor(() => expect(net.find('POST /backup/imports')).toHaveLength(1))
    expect(net.find('POST /backup/imports')[0]!.body).toEqual({ upload_id: 'b'.repeat(24), passphrase: '' })
    expect(passwordInputs()).toHaveLength(0)
    await act(async () => {
      view.rerender(
        <UploadDialog
          jobs={[job({ id: 'job-3', kind: 'import', status: 'failed', finished_at: NOW, error: 'Yedek dosyası okunamadı; dosya bozuk veya eksik.' })]}
          {...props}
        />,
      )
    })
    expect((await screen.findAllByText('Yedek dosyası okunamadı; dosya bozuk veya eksik.')).length).toBeGreaterThan(0)
    expect(screen.getByText('Dosya kabul edilmedi')).toBeInTheDocument()
    expect(props.onImported).not.toHaveBeenCalled()
  })
})
