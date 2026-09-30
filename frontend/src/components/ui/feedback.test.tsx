import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast, useUI } from '@/stores/ui'
import { ErrorBoundary, Toasts } from './feedback'

let broken = true
function Flaky({ label = 'Widget içeriği' }: { label?: string }) {
  if (broken) throw new Error('render patladı')
  return <p>{label}</p>
}

beforeEach(() => {
  broken = true
  // React and the boundary both report the caught error; keep the output clean.
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.restoreAllMocks()
  useUI.setState({ toasts: [] })
})

describe('ErrorBoundary', () => {
  it('hata yokken içeriği gösterir', () => {
    broken = false
    render(
      <ErrorBoundary name="Depolama">
        <Flaky />
      </ErrorBoundary>,
    )
    expect(screen.getByText('Widget içeriği')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('çizim sırasında hata fırlatan bileşen yerine Türkçe açıklama gösterir', () => {
    render(
      <ErrorBoundary name="Depolama">
        <Flaky />
      </ErrorBoundary>,
    )
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('Depolama görüntülenemiyor')
    expect(alert).toHaveTextContent('Bu bölümde beklenmeyen bir hata oluştu. Panelin diğer bölümleri çalışmaya devam ediyor.')
    expect(alert).not.toHaveTextContent('render patladı')
  })

  it('ad verilmediğinde genel başlık kullanır', () => {
    render(
      <ErrorBoundary>
        <Flaky />
      </ErrorBoundary>,
    )
    expect(screen.getByRole('alert')).toHaveTextContent('Bu bölüm görüntülenemiyor')
  })

  it('kardeş bileşenler çalışmaya devam eder', () => {
    render(
      <div>
        <ErrorBoundary name="Depolama">
          <Flaky />
        </ErrorBoundary>
        <ErrorBoundary name="Ağ">
          <p>Ağ trafiği</p>
        </ErrorBoundary>
        <p>Sayfa altlığı</p>
      </div>,
    )
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(screen.getByText('Ağ trafiği')).toBeInTheDocument()
    expect(screen.getByText('Sayfa altlığı')).toBeInTheDocument()
  })

  it('Tekrar Dene bileşeni yeniden çizer', async () => {
    const user = userEvent.setup()
    render(
      <ErrorBoundary name="Depolama">
        <Flaky />
      </ErrorBoundary>,
    )
    broken = false
    await user.click(screen.getByRole('button', { name: 'Tekrar Dene' }))
    expect(screen.getByText('Widget içeriği')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('hata sürüyorsa Tekrar Dene sonrası açıklama yeniden gösterilir', async () => {
    const user = userEvent.setup()
    render(
      <ErrorBoundary name="Depolama">
        <Flaky />
      </ErrorBoundary>,
    )
    await user.click(screen.getByRole('button', { name: 'Tekrar Dene' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Depolama görüntülenemiyor')
  })

  it('resetKey değişince (başka sayfaya geçiş) kendini sıfırlar', () => {
    const { rerender } = render(
      <ErrorBoundary name="Docker" resetKey="/docker">
        <Flaky />
      </ErrorBoundary>,
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
    broken = false
    rerender(
      <ErrorBoundary name="Docker" resetKey="/docker">
        <Flaky />
      </ErrorBoundary>,
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
    rerender(
      <ErrorBoundary name="Docker" resetKey="/files">
        <Flaky />
      </ErrorBoundary>,
    )
    expect(screen.getByText('Widget içeriği')).toBeInTheDocument()
  })
})

describe('Toasts', () => {
  it('hata mesajını role="alert", diğerlerini role="status" ile gösterir', () => {
    render(<Toasts />)
    act(() => {
      toast.error('Disk bağlanamadı.')
      toast.success('Ayarlar kaydedildi.')
    })
    expect(screen.getByRole('alert')).toHaveTextContent('Disk bağlanamadı.')
    expect(screen.getByRole('status')).toHaveTextContent('Ayarlar kaydedildi.')
  })

  it('kapatma düğmesinin erişilebilir adı vardır ve balonu kaldırır', async () => {
    const user = userEvent.setup()
    render(<Toasts />)
    act(() => toast.error('Disk bağlanamadı.'))
    await user.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Bildirimi kapat' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
