import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { HardDrive } from 'lucide-react'
import { expectAccessibleNames } from '@/test/a11y'
import { makeUser, signedIn } from '@/test/helpers'
import { useAuth } from '@/stores/auth'
import { useUI } from '@/stores/ui'
import { matches, registerSearchProvider, SearchPalette, type SearchProvider } from './SearchPalette'

describe('matches (Türkçe harf kuralları)', () => {
  it.each([
    ['İstanbul', 'istanbul'],
    ['İstanbul', 'İSTANBUL'],
    ['istanbul', 'İSTANBUL'],
    ['ISPARTA', 'ısparta'],
    ['ısparta', 'ISPARTA'],
    ['Isparta', 'ısp'],
    ['DİSK YÖNETİMİ', 'disk yönetimi'],
    ['Güncellemeler', 'GÜNCEL'],
    ['Ağ', 'AĞ'],
    ['Çalışıyor', 'ÇALIŞ'],
    ['Depolama', ''],
  ])('"%s" içinde "%s" bulunur', (text, query) => {
    expect(matches(text, query)).toBe(true)
  })

  it.each([
    ['ISPARTA', 'isparta'],
    ['ısparta', 'isparta'],
    ['İstanbul', 'ıstanbul'],
    ['istanbul', 'ISTANBUL'],
    ['Docker', 'dosya'],
  ])('"%s" içinde "%s" bulunmaz', (text, query) => {
    expect(matches(text, query)).toBe(false)
  })
})

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>
}

function renderPalette() {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <Where />
      <SearchPalette />
    </MemoryRouter>,
  )
}

const input = () => screen.getByRole('combobox')
const options = () => screen.queryAllByRole('option').map((o) => o.textContent)
const selected = () => screen.getAllByRole('option').find((o) => o.getAttribute('aria-selected') === 'true')?.textContent
const where = () => screen.getByTestId('where').textContent

function asAdmin() {
  useAuth.setState({ status: signedIn(), user: makeUser(), isAdmin: true })
}
function asUser() {
  useAuth.setState({ status: signedIn({ role: 'user' }), user: makeUser({ role: 'user' }), isAdmin: false })
}

/** Providers live in a module-level registry; tests swap this one's behaviour. */
let provider: SearchProvider = () => []
let failing: SearchProvider = () => []
registerSearchProvider('test-records', (q, s) => provider(q, s))
registerSearchProvider('test-failing', (q, s) => failing(q, s))

beforeEach(() => {
  provider = () => []
  failing = () => []
  Element.prototype.scrollIntoView = vi.fn()
  asAdmin()
  useUI.setState({ searchOpen: true })
})

afterEach(() => {
  useUI.setState({ searchOpen: false })
})

describe('Arama paleti', () => {
  it('açıldığında odak arama alanındadır ve öğelerin erişilebilir adı vardır', () => {
    renderPalette()
    expect(screen.getByRole('dialog', { name: 'Ara' })).toBeInTheDocument()
    expect(input()).toHaveFocus()
    expect(input()).toHaveAccessibleName('Uygulama, ayar, dosya ara...')
    expectAccessibleNames(screen.getByRole('dialog'))
  })

  it('Ctrl+K ile açılıp kapanır', async () => {
    const user = userEvent.setup()
    useUI.setState({ searchOpen: false })
    renderPalette()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await user.keyboard('{Control>}k{/Control}')
    expect(screen.getByRole('dialog', { name: 'Ara' })).toBeInTheDocument()
    await user.keyboard('{Control>}k{/Control}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('sayfaları adına ve anahtar sözcüklerine göre bulur', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'firewall')
    expect(options()).toEqual(['Ağ'])
    await user.clear(input())
    await user.type(input(), 'GÜNCEL')
    expect(options()).toEqual(['Güncellemeler'])
  })

  it('Türkçe büyük harfle yazılan sorguyu eşleştirir', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'DİSK')
    expect(options()).toContain('Depolama')
    await user.clear(input())
    await user.type(input(), 'SICAKLIK')
    expect(options()).toEqual(['Ana Sayfa'])
  })

  it('eşleşme yoksa bunu bildirir', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'qqqq')
    await waitFor(() => expect(screen.getByText('Sonuç bulunamadı')).toBeInTheDocument())
    expect(options()).toEqual([])
  })

  it('yönetici olmayan kullanıcıya yönetici sayfalarını önermez', async () => {
    const user = userEvent.setup()
    asUser()
    renderPalette()
    expect(options()).not.toEqual([])
    for (const hidden of ['Terminal', 'Yedekleme', 'Ayarlar']) expect(options()).not.toContain(hidden)

    for (const q of ['terminal', 'yedek', 'ayar', 'kullanıcı']) {
      await user.clear(input())
      await user.type(input(), q)
      expect(options()).toEqual([])
    }
  })

  it('yöneticiye yönetici sayfalarını önerir', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'terminal')
    expect(options()).toEqual(['Terminal'])
  })

  it('ok tuşlarıyla gezilir ve Enter seçili sonucu açar', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'do')
    expect(options()).toEqual(['Docker', 'Dosyalar'])
    expect(selected()).toBe('Docker')

    await user.keyboard('{ArrowDown}')
    expect(selected()).toBe('Dosyalar')
    await user.keyboard('{ArrowDown}{ArrowDown}')
    expect(selected()).toBe('Dosyalar')
    await user.keyboard('{ArrowUp}')
    expect(selected()).toBe('Docker')
    await user.keyboard('{ArrowUp}')
    expect(selected()).toBe('Docker')

    await user.keyboard('{ArrowDown}{Enter}')
    expect(where()).toBe('/files')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('Enter ilk sonucu açar; sonuç yokken hiçbir şey yapmaz', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'qqqq{Enter}')
    expect(where()).toBe('/')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await user.clear(input())
    await user.type(input(), 'docker{Enter}')
    expect(where()).toBe('/docker')
  })

  it('sorgu değişince seçim ilk sonuca döner', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'do')
    await user.keyboard('{ArrowDown}')
    expect(selected()).toBe('Dosyalar')
    await user.type(input(), 'c')
    expect(options()).toEqual(['Docker'])
    expect(selected()).toBe('Docker')
  })

  it('tıklanan sonucu açar', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'do')
    await user.click(screen.getByRole('option', { name: /Dosyalar/ }))
    expect(where()).toBe('/files')
  })

  it('yeniden açıldığında sorgu temizlenir', async () => {
    const user = userEvent.setup()
    renderPalette()
    await user.type(input(), 'docker')
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await user.keyboard('{Control>}k{/Control}')
    expect(input()).toHaveValue('')
  })

  describe('arama sağlayıcıları', () => {
    const record = { id: 'disk:sda', group: 'Diskler', title: 'Samsung SSD 870', subtitle: '/dev/sda', icon: HardDrive, to: '/storage/sda' }

    it('sağlayıcı sonuçlarını sayfalarla birlikte gösterir ve açar', async () => {
      const user = userEvent.setup()
      const seen: string[] = []
      provider = async (q) => {
        seen.push(q)
        return [record]
      }
      renderPalette()
      await user.type(input(), ' samsung ')
      expect(await screen.findByRole('option', { name: /Samsung SSD 870/ })).toHaveTextContent('/dev/sda')
      expect(screen.getByText('Diskler')).toBeInTheDocument()
      expect(seen.at(-1)).toBe('samsung')
      await user.keyboard('{Enter}')
      expect(where()).toBe('/storage/sda')
    })

    it.each([
      ['reddedilen', () => Promise.reject(new Error('Docker servisine ulaşılamıyor.'))],
      [
        'eşzamanlı hata fırlatan',
        () => {
          throw new Error('patladı')
        },
      ],
    ] as Array<[string, SearchProvider]>)('%s sağlayıcı diğerlerini ve sayfa sonuçlarını bozmaz', async (_name, bad) => {
      const user = userEvent.setup()
      failing = bad
      provider = () => [record]
      renderPalette()
      await user.type(input(), 'ss')
      expect(await screen.findByRole('option', { name: /Samsung SSD 870/ })).toBeInTheDocument()
      await user.clear(input())
      await user.type(input(), 'docker')
      await waitFor(() => expect(options()).toContain('Docker'))
      expect(screen.getByRole('dialog')).toBeInTheDocument()
    })

    it('tek karakterlik sorguda sağlayıcıları çağırmaz', async () => {
      const user = userEvent.setup()
      const spy = vi.fn(() => [record])
      provider = spy
      renderPalette()
      await user.type(input(), 's')
      await new Promise((resolve) => setTimeout(resolve, 250))
      expect(spy).not.toHaveBeenCalled()
    })

    it('hızlı yazarken her tuş için değil, duraklamadan sonra arar ve eski aramayı iptal eder', async () => {
      const user = userEvent.setup()
      const signals: AbortSignal[] = []
      const queries: string[] = []
      provider = (q, signal) => {
        queries.push(q)
        signals.push(signal)
        return [record]
      }
      renderPalette()
      await user.type(input(), 'samsung')
      await screen.findByRole('option', { name: /Samsung SSD 870/ })
      expect(queries).toEqual(['samsung'])
      await user.type(input(), ' 870')
      await waitFor(() => expect(queries).toEqual(['samsung', 'samsung 870']))
      expect(signals[0]!.aborted).toBe(true)
    })

    it('geç gelen eski arama sonucu yeni sorgunun sonuçlarının yerine geçmez', async () => {
      const user = userEvent.setup()
      let releaseOld!: (r: (typeof record)[]) => void
      provider = (q) => {
        if (q === 'eski') return new Promise((resolve) => (releaseOld = resolve))
        return [{ ...record, id: 'new', title: 'Yeni sonuç' }]
      }
      renderPalette()
      await user.type(input(), 'eski')
      await waitFor(() => expect(releaseOld).toBeDefined())
      await user.clear(input())
      await user.type(input(), 'yeni')
      await screen.findByRole('option', { name: /Yeni sonuç/ })
      releaseOld([{ ...record, id: 'old', title: 'Eski sonuç' }])
      await new Promise((resolve) => setTimeout(resolve, 50))
      expect(screen.queryByRole('option', { name: /Eski sonuç/ })).not.toBeInTheDocument()
      expect(screen.getByRole('option', { name: /Yeni sonuç/ })).toBeInTheDocument()
    })
  })
})
