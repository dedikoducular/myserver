import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { CornerDownLeft, Search, type LucideIcon } from 'lucide-react'
import { Modal, Spinner } from '@/components/ui'
import { cx } from '@/lib/format'
import { messages } from '@/i18n'
import { useAuth } from '@/stores/auth'
import { useUI } from '@/stores/ui'
import { navItems, navText } from './navigation'

const t = messages({
  tr: {
    title: 'Ara',
    placeholder: 'Uygulama, ayar, dosya ara...',
    empty: 'Sonuç bulunamadı',
    hint: 'Sayfalar, ayarlar ve diğer kayıtlar arasında arayın',
    pages: 'Sayfalar',
    open: 'Aç',
  },
})

export interface SearchResult {
  id: string
  group: string
  title: string
  subtitle?: string
  icon: LucideIcon
  /** Route to open. */
  to: string
}

/** A search provider returns matches for a query. Modules register one to
 *  make their records (apps, settings sections, files) searchable. */
export type SearchProvider = (query: string, signal: AbortSignal) => Promise<SearchResult[]> | SearchResult[]

const providers = new Map<string, SearchProvider>()

export function registerSearchProvider(name: string, provider: SearchProvider): void {
  providers.set(name, provider)
}

/** Case-insensitive match using Turkish casing rules (I/ı, İ/i). */
export function matches(text: string, query: string): boolean {
  return text.toLocaleLowerCase('tr-TR').includes(query.toLocaleLowerCase('tr-TR'))
}

function pageResults(query: string, isAdmin: boolean): SearchResult[] {
  return navItems
    .filter((item) => !item.adminOnly || isAdmin)
    .filter((item) => matches(navText(item.id) + ' ' + item.keywords, query))
    .map((item) => ({ id: 'page:' + item.id, group: t('pages'), title: navText(item.id), icon: item.icon, to: item.path }))
}

export function SearchPalette() {
  const open = useUI((s) => s.searchOpen)
  const setOpen = useUI((s) => s.setSearch)
  const isAdmin = useAuth((s) => s.isAdmin)
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [remote, setRemote] = useState<SearchResult[]>([])
  const [busy, setBusy] = useState(false)
  const [active, setActive] = useState(0)
  const listRef = useRef<HTMLUListElement>(null)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setOpen(!useUI.getState().searchOpen)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [setOpen])

  useEffect(() => {
    if (open) {
      setQuery('')
      setRemote([])
      setActive(0)
    }
  }, [open])

  const q = query.trim()

  useEffect(() => {
    if (!open || q.length < 2 || providers.size === 0) {
      setRemote([])
      setBusy(false)
      return
    }
    const ctrl = new AbortController()
    setBusy(true)
    const timer = window.setTimeout(() => {
      void Promise.allSettled(Array.from(providers.values()).map(async (p) => p(q, ctrl.signal))).then((settled) => {
        if (ctrl.signal.aborted) return
        setRemote(settled.flatMap((s) => (s.status === 'fulfilled' ? s.value : [])))
        setBusy(false)
      })
    }, 180)
    return () => {
      window.clearTimeout(timer)
      ctrl.abort()
    }
  }, [q, open])

  const results = useMemo(() => [...pageResults(q, isAdmin), ...remote].slice(0, 40), [q, isAdmin, remote])

  useEffect(() => setActive(0), [q, remote.length])
  useEffect(() => {
    listRef.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active])

  const go = (r: SearchResult | undefined) => {
    if (!r) return
    setOpen(false)
    navigate(r.to)
  }

  let lastGroup = ''
  return (
    <Modal open={open} onClose={() => setOpen(false)} title={t('title')} size="lg">
      <div className="flex flex-col gap-3">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-faint" aria-hidden />
          <input
            data-autofocus
            role="combobox"
            aria-expanded="true"
            aria-controls="search-results"
            aria-label={t('placeholder')}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') {
                e.preventDefault()
                setActive((i) => Math.min(results.length - 1, i + 1))
              } else if (e.key === 'ArrowUp') {
                e.preventDefault()
                setActive((i) => Math.max(0, i - 1))
              } else if (e.key === 'Enter') {
                e.preventDefault()
                go(results[active])
              }
            }}
            placeholder={t('placeholder')}
            autoComplete="off"
            spellCheck={false}
            className="h-11 w-full rounded-xl border border-line bg-surface pr-10 pl-9 text-sm text-fg placeholder:text-faint focus:border-accent focus:outline-none"
          />
          {busy && (
            <span className="absolute top-1/2 right-3 -translate-y-1/2">
              <Spinner className="size-4" />
            </span>
          )}
        </div>
        <ul id="search-results" ref={listRef} role="listbox" aria-label={t('title')} className="flex max-h-[50dvh] min-h-24 flex-col overflow-y-auto">
          {results.length === 0 && <li className="px-2 py-6 text-center text-sm text-muted">{q ? t('empty') : t('hint')}</li>}
          {results.map((r, i) => {
            const header = r.group !== lastGroup
            lastGroup = r.group
            return (
              <li key={r.id} role="presentation">
                {header && <p className="px-2 pt-2 pb-1 text-[11px] font-medium uppercase tracking-wide text-faint">{r.group}</p>}
                <button
                  type="button"
                  role="option"
                  aria-selected={i === active}
                  onMouseEnter={() => setActive(i)}
                  onClick={() => go(r)}
                  className={cx('flex min-h-11 w-full items-center gap-3 rounded-lg px-2.5 py-2 text-left text-sm', i === active ? 'bg-raised text-fg' : 'text-muted')}
                >
                  <r.icon className="size-4 shrink-0" aria-hidden />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-fg">{r.title}</span>
                    {r.subtitle && <span className="block truncate text-xs text-faint">{r.subtitle}</span>}
                  </span>
                  {i === active && <CornerDownLeft className="size-3.5 shrink-0 text-faint" aria-label={t('open')} />}
                </button>
              </li>
            )
          })}
        </ul>
      </div>
    </Modal>
  )
}
