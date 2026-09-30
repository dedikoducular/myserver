import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { lazyNamed, Slot } from './slot'

let broken = true
function Throwing() {
  if (broken) throw new Error('widget patladı')
  return <p>Depolama verisi</p>
}
function Healthy() {
  return <p>Ağ trafiği verisi</p>
}

beforeEach(() => {
  broken = true
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('lazyNamed', () => {
  it('adı verilen dışa aktarımı bileşen olarak yükler', async () => {
    const Widget = lazyNamed(async () => ({ Healthy, other: 1 }), 'Healthy')
    render(<Slot name="Ağ" component={Widget} />)
    expect(await screen.findByText('Ağ trafiği verisi')).toBeInTheDocument()
  })
})

describe('Slot', () => {
  it('çizimde hata veren widget yalnızca kendi kutusunu kaybeder', () => {
    render(
      <div>
        <Slot name="Depolama" component={Throwing} />
        <Slot name="Ağ Trafiği" component={Healthy} />
      </div>,
    )
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('Depolama görüntülenemiyor')
    expect(alert).toHaveTextContent('Panelin diğer bölümleri çalışmaya devam ediyor.')
    expect(screen.getByText('Ağ trafiği verisi')).toBeInTheDocument()
  })

  it('yüklenemeyen (import reddedilen) widget Türkçe açıklama gösterir, kardeşleri çalışır', async () => {
    const Missing = lazyNamed(() => Promise.reject<{ W: typeof Healthy }>(new Error('Failed to fetch dynamically imported module')), 'W')
    const Lazy = lazyNamed(async () => ({ Healthy }), 'Healthy')
    render(
      <div>
        <Slot name="Uygulamalar" component={Missing} />
        <Slot name="Ağ Trafiği" component={Lazy} />
        <Slot name="Servisler" component={Healthy} />
      </div>,
    )
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Uygulamalar görüntülenemiyor')
    expect(alert).not.toHaveTextContent('Failed to fetch')
    expect(await screen.findAllByText('Ağ trafiği verisi')).toHaveLength(2)
    expect(screen.getAllByRole('alert')).toHaveLength(1)
  })

  it('Tekrar Dene widgetı yeniden çizer', async () => {
    const user = userEvent.setup()
    render(
      <div>
        <Slot name="Depolama" component={Throwing} />
        <Slot name="Ağ Trafiği" component={Healthy} />
      </div>,
    )
    broken = false
    await user.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Tekrar Dene' }))
    expect(screen.getByText('Depolama verisi')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByText('Ağ trafiği verisi')).toBeInTheDocument()
  })

  it('yüklenemeyen widgetta Tekrar Dene paneli bozmaz', async () => {
    const user = userEvent.setup()
    const Missing = lazyNamed(() => Promise.reject<{ W: typeof Healthy }>(new Error('chunk')), 'W')
    render(
      <div>
        <Slot name="Uygulamalar" component={Missing} />
        <Slot name="Servisler" component={Healthy} />
      </div>,
    )
    await user.click(within(await screen.findByRole('alert')).getByRole('button', { name: 'Tekrar Dene' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Uygulamalar görüntülenemiyor')
    expect(screen.getByText('Ağ trafiği verisi')).toBeInTheDocument()
  })

  it('geçici bir yükleme hatasından sonra Tekrar Dene widgetı yükler', async () => {
    const user = userEvent.setup()
    const load = vi
      .fn<() => Promise<{ Healthy: typeof Healthy }>>()
      .mockRejectedValueOnce(new Error('Failed to fetch dynamically imported module'))
      .mockResolvedValue({ Healthy })
    const Widget = lazyNamed(load, 'Healthy')
    render(<Slot name="Uygulamalar" component={Widget} />)
    await user.click(within(await screen.findByRole('alert')).getByRole('button', { name: 'Tekrar Dene' }))
    expect(await screen.findByText('Ağ trafiği verisi')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('yüklenirken yer tutucu gösterir; bare ise hiçbir şey göstermez', async () => {
    const never = new Promise<{ Healthy: typeof Healthy }>(() => undefined)
    const Pending = lazyNamed(() => never, 'Healthy')
    const { container, unmount } = render(<Slot name="Ölçümler" component={Pending} />)
    expect(container.querySelector('.animate-pulse')).not.toBeNull()
    unmount()
    const bare = render(<Slot name="Durum" component={Pending} bare />)
    expect(bare.container).toBeEmptyDOMElement()
  })
})
