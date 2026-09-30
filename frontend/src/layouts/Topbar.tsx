import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import {
  AlertOctagon,
  AlertTriangle,
  Bell,
  CheckCheck,
  CheckCircle2,
  ChevronDown,
  HeartPulse,
  Info,
  KeyRound,
  LogOut,
  Menu as MenuIcon,
  Moon,
  Search,
  Settings,
  Sun,
  XCircle,
  type LucideIcon,
} from 'lucide-react'
import { Alert, Button, EmptyState, ErrorState, Field, Input, LoadingState, Modal, Status, type Tone } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { useEventSource } from '@/hooks/useStream'
import { cx, formatRelative } from '@/lib/format'
import { messages } from '@/i18n'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast, useUI } from '@/stores/ui'
import type { HealthReport, HealthStatus, Notification, NotificationList, Severity } from '@/types/api'

const t = messages({
  tr: {
    menu: 'Menüyü aç',
    search: 'Uygulama, ayar, dosya ara...',
    searchShort: 'Ara',
    notifications: 'Bildirimler',
    unread: '{count} okunmamış bildirim',
    noNotifications: 'Bildirim yok',
    noNotificationsHint: 'Sistem olayları burada görünecek.',
    markAll: 'Tümünü okundu işaretle',
    themeLight: 'Açık temaya geç',
    themeDark: 'Koyu temaya geç',
    settings: 'Ayarlar',
    health: 'Sistem Sağlığı',
    healthy: 'İyi',
    warning: 'Uyarı',
    critical: 'Kritik',
    healthUnknown: 'Bilinmiyor',
    healthAllGood: 'Tüm denetimler başarılı.',
    admin: 'Yönetici',
    user: 'Kullanıcı',
    account: 'Hesap menüsü',
    changePassword: 'Parolayı Değiştir',
    logout: 'Çıkış Yap',
    currentPassword: 'Mevcut parola',
    newPassword: 'Yeni parola',
    repeatPassword: 'Yeni parola (tekrar)',
    passwordHint: 'En az 10 karakter.',
    passwordMismatch: 'Parolalar eşleşmiyor.',
    passwordChanged: 'Parolanız değiştirildi. Diğer oturumlar kapatıldı.',
    save: 'Kaydet',
    cancel: 'Vazgeç',
  },
})

/** Closes a popover on outside click or Escape. */
function usePopover() {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])
  return { open, setOpen, ref }
}

function TopButton({ icon: Icon, label, onClick, badge, expanded }: { icon: LucideIcon; label: string; onClick: () => void; badge?: number; expanded?: boolean }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      aria-expanded={expanded}
      onClick={onClick}
      className="relative inline-flex size-10 shrink-0 items-center justify-center rounded-xl text-muted transition-colors hover:bg-raised hover:text-fg"
    >
      <Icon className="size-5" aria-hidden />
      {badge !== undefined && badge > 0 && (
        <span className="absolute top-1 right-1 min-w-4 rounded-full bg-danger px-1 text-center text-[10px] font-semibold leading-4 text-white">
          {badge > 9 ? '9+' : badge}
        </span>
      )}
    </button>
  )
}

function Popover({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cx('ms-fade-in absolute top-full right-0 z-40 mt-2 rounded-2xl border border-line bg-card shadow-card', className)}>{children}</div>
  )
}

const severityStyle: Record<Severity, { icon: LucideIcon; tone: string }> = {
  INFO: { icon: Info, tone: 'text-accent' },
  SUCCESS: { icon: CheckCircle2, tone: 'text-success' },
  WARNING: { icon: AlertTriangle, tone: 'text-warning' },
  ERROR: { icon: XCircle, tone: 'text-danger' },
  CRITICAL: { icon: AlertOctagon, tone: 'text-danger' },
}

function NotificationsMenu() {
  const { open, setOpen, ref } = usePopover()
  const { data, error, loading, reload } = useQuery<NotificationList>('/notifications', { query: { limit: 30 } })
  const [items, setItems] = useState<Notification[]>([])
  const [unread, setUnread] = useState(0)

  useEffect(() => {
    if (data) {
      setItems(data.items)
      setUnread(data.unread)
    }
  }, [data])

  useEventSource(
    '/notifications/stream',
    useCallback((_event: string, payload: unknown) => {
      const n = payload as Notification
      if (typeof n?.id !== 'number') return
      setItems((prev) => [n, ...prev.filter((p) => p.id !== n.id)].slice(0, 30))
      setUnread((u) => u + 1)
      if (n.severity === 'ERROR' || n.severity === 'CRITICAL') toast.error(n.title)
      else if (n.severity === 'WARNING') toast.warning(n.title)
    }, []),
    { events: ['notification'] },
  )

  const markAll = useAction(() => api.post('/notifications/read-all'), {
    onSuccess: () => {
      setItems((prev) => prev.map((n) => ({ ...n, read: true })))
      setUnread(0)
    },
    onError: (m) => toast.error(m),
  })

  const markOne = (n: Notification) => {
    if (n.read) return
    setItems((prev) => prev.map((p) => (p.id === n.id ? { ...p, read: true } : p)))
    setUnread((u) => Math.max(0, u - 1))
    api.post(`/notifications/${n.id}/read`).catch(() => void reload())
  }

  return (
    <div ref={ref} className="relative">
      <TopButton
        icon={Bell}
        label={unread > 0 ? t('unread', { count: unread }) : t('notifications')}
        badge={unread}
        expanded={open}
        onClick={() => setOpen(!open)}
      />
      {open && (
        <Popover className="fixed inset-x-3 top-16 right-3 mt-0 sm:absolute sm:inset-x-auto sm:top-full sm:right-0 sm:mt-2 sm:w-96">
          <div className="flex items-center justify-between gap-2 border-b border-line px-4 py-3">
            <h2 className="text-sm font-semibold text-fg">{t('notifications')}</h2>
            <Button size="sm" variant="ghost" icon={CheckCheck} disabled={unread === 0} loading={markAll.pending} onClick={() => void markAll.run()}>
              {t('markAll')}
            </Button>
          </div>
          <div className="max-h-[60dvh] overflow-y-auto p-1.5">
            {loading ? (
              <LoadingState className="py-6" />
            ) : error ? (
              <ErrorState message={error} onRetry={() => void reload()} className="py-6" />
            ) : items.length === 0 ? (
              <EmptyState icon={Bell} title={t('noNotifications')} description={t('noNotificationsHint')} className="py-6" />
            ) : (
              <ul className="flex flex-col">
                {items.map((n) => {
                  const { icon: Icon, tone } = severityStyle[n.severity] ?? severityStyle.INFO
                  return (
                    <li key={n.id}>
                      <button
                        type="button"
                        onClick={() => markOne(n)}
                        className={cx('flex w-full items-start gap-3 rounded-xl px-2.5 py-2.5 text-left transition-colors hover:bg-raised', !n.read && 'bg-accent/5')}
                      >
                        <Icon className={cx('mt-0.5 size-4 shrink-0', tone)} aria-hidden />
                        <span className="min-w-0 flex-1">
                          <span className="flex items-start gap-2">
                            <span className={cx('min-w-0 flex-1 break-words text-sm', n.read ? 'text-muted' : 'font-medium text-fg')}>{n.title}</span>
                            {!n.read && <span className="mt-1.5 size-2 shrink-0 rounded-full bg-accent" aria-hidden />}
                          </span>
                          {n.message && <span className="mt-0.5 block break-words text-xs text-muted">{n.message}</span>}
                          <span className="mt-1 block text-[11px] text-faint">{formatRelative(n.created_at)}</span>
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>
        </Popover>
      )}
    </div>
  )
}

const healthTone: Record<HealthStatus, Tone> = { HEALTHY: 'success', WARNING: 'warning', CRITICAL: 'danger' }

function healthLabel(status: HealthStatus | undefined): string {
  if (status === 'HEALTHY') return t('healthy')
  if (status === 'WARNING') return t('warning')
  if (status === 'CRITICAL') return t('critical')
  return t('healthUnknown')
}

function HealthMenu() {
  const { open, setOpen, ref } = usePopover()
  // Health changes slowly and the server caches it; one request a minute
  // is enough to keep the indicator current.
  const { data, error, reload } = useQuery<HealthReport>('/health', { refetchInterval: 60_000 })
  const tone: Tone = data ? healthTone[data.status] : 'neutral'
  const problems = data?.checks.filter((c) => c.status !== 'HEALTHY') ?? []
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        aria-expanded={open}
        aria-label={`${t('health')}: ${healthLabel(data?.status)}`}
        title={`${t('health')}: ${healthLabel(data?.status)}`}
        onClick={() => {
          if (!open) void reload()
          setOpen(!open)
        }}
        className="inline-flex h-10 items-center gap-2 rounded-xl px-2.5 text-muted transition-colors hover:bg-raised hover:text-fg"
      >
        <HeartPulse className="size-5" aria-hidden />
        <span className="hidden xl:inline">
          <Status tone={tone}>{healthLabel(data?.status)}</Status>
        </span>
        <span className="xl:hidden">
          <Status tone={tone}>
            <span className="sr-only">{healthLabel(data?.status)}</span>
          </Status>
        </span>
      </button>
      {open && (
        <Popover className="fixed inset-x-3 top-16 right-3 mt-0 sm:absolute sm:inset-x-auto sm:top-full sm:right-0 sm:mt-2 sm:w-96">
          <div className="flex items-center justify-between border-b border-line px-4 py-3">
            <h2 className="text-sm font-semibold text-fg">{t('health')}</h2>
            <Status tone={tone}>{healthLabel(data?.status)}</Status>
          </div>
          <div className="max-h-[60dvh] overflow-y-auto p-3">
            {error && !data ? (
              <ErrorState message={error} onRetry={() => void reload()} className="py-4" />
            ) : !data ? (
              <LoadingState className="py-4" />
            ) : (
              <ul className="flex flex-col gap-1.5">
                {problems.length === 0 && <li className="px-1 pb-1 text-xs text-muted">{t('healthAllGood')}</li>}
                {[...problems, ...data.checks.filter((c) => c.status === 'HEALTHY')].map((c) => (
                  <li key={c.id} className="flex items-start gap-2.5 rounded-xl border border-line bg-surface px-3 py-2">
                    <span className="mt-1.5">
                      <Status tone={healthTone[c.status]}>
                        <span className="sr-only">{healthLabel(c.status)}</span>
                      </Status>
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm text-fg">{c.name}</span>
                      <span className="block break-words text-xs text-muted">{c.message}</span>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Popover>
      )}
    </div>
  )
}

function PasswordDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const [mismatch, setMismatch] = useState(false)
  const change = useAction(() => api.post('/auth/password', { current_password: current, new_password: next }), {
    onSuccess: () => {
      toast.success(t('passwordChanged'))
      onClose()
    },
  })
  useEffect(() => {
    if (open) {
      setCurrent('')
      setNext('')
      setRepeat('')
      setMismatch(false)
      change.clearError()
    }
    // Reset only when the dialog opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (next !== repeat) {
      setMismatch(true)
      return
    }
    setMismatch(false)
    void change.run()
  }

  return (
    <Modal open={open} onClose={onClose} title={t('changePassword')} size="sm" busy={change.pending}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label={t('currentPassword')} htmlFor="pw-current">
          <Input id="pw-current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} data-autofocus />
        </Field>
        <Field label={t('newPassword')} htmlFor="pw-new" hint={t('passwordHint')}>
          <Input id="pw-new" type="password" autoComplete="new-password" required minLength={10} value={next} onChange={(e) => setNext(e.target.value)} />
        </Field>
        <Field label={t('repeatPassword')} htmlFor="pw-repeat" error={mismatch ? t('passwordMismatch') : null}>
          <Input id="pw-repeat" type="password" autoComplete="new-password" required invalid={mismatch} value={repeat} onChange={(e) => setRepeat(e.target.value)} />
        </Field>
        {change.error && <Alert tone="danger">{change.error}</Alert>}
        <div className="mt-1 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={change.pending}>
            {t('cancel')}
          </Button>
          <Button type="submit" variant="primary" loading={change.pending}>
            {t('save')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function UserMenu() {
  const { open, setOpen, ref } = usePopover()
  const user = useAuth((s) => s.user)
  const isAdmin = useAuth((s) => s.isAdmin)
  const logout = useAuth((s) => s.logout)
  const [passwordOpen, setPasswordOpen] = useState(false)
  const navigate = useNavigate()
  if (!user) return null
  const item = 'flex min-h-10 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-sm text-fg transition-colors hover:bg-raised'
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t('account')}
        onClick={() => setOpen(!open)}
        className="flex h-10 items-center gap-2.5 rounded-xl pr-1.5 pl-1 transition-colors hover:bg-raised"
      >
        <span className="inline-flex size-8 items-center justify-center rounded-full bg-accent-strong text-sm font-semibold text-accent-fg">
          {user.username.charAt(0).toLocaleUpperCase('tr-TR')}
        </span>
        <span className="hidden min-w-0 text-left leading-tight sm:block">
          <span className="block max-w-28 truncate text-sm font-medium text-fg">{user.username}</span>
          <span className="block text-[11px] text-muted">{isAdmin ? t('admin') : t('user')}</span>
        </span>
        <ChevronDown className="hidden size-4 text-muted sm:block" aria-hidden />
      </button>
      {open && (
        <Popover className="w-56 p-1">
          <div role="menu">
            <button
              type="button"
              role="menuitem"
              className={item}
              onClick={() => {
                setOpen(false)
                setPasswordOpen(true)
              }}
            >
              <KeyRound className="size-4 text-muted" aria-hidden />
              {t('changePassword')}
            </button>
            {isAdmin && (
              <button
                type="button"
                role="menuitem"
                className={item}
                onClick={() => {
                  setOpen(false)
                  navigate('/settings')
                }}
              >
                <Settings className="size-4 text-muted" aria-hidden />
                {t('settings')}
              </button>
            )}
            <div className="my-1 border-t border-line" />
            <button type="button" role="menuitem" className={cx(item, 'text-danger hover:bg-danger/10')} onClick={() => void logout()}>
              <LogOut className="size-4" aria-hidden />
              {t('logout')}
            </button>
          </div>
        </Popover>
      )}
      <PasswordDialog open={passwordOpen} onClose={() => setPasswordOpen(false)} />
    </div>
  )
}

export function Topbar() {
  const setMobileNav = useUI((s) => s.setMobileNav)
  const setSearch = useUI((s) => s.setSearch)
  const theme = useUI((s) => s.theme)
  const toggleTheme = useUI((s) => s.toggleTheme)
  const isAdmin = useAuth((s) => s.isAdmin)
  return (
    <header className="sticky top-0 z-30 flex h-16 shrink-0 items-center gap-2 border-b border-line bg-bg/85 px-3 backdrop-blur sm:gap-3 sm:px-5">
      <div className="md:hidden">
        <TopButton icon={MenuIcon} label={t('menu')} onClick={() => setMobileNav(true)} />
      </div>
      <button
        type="button"
        onClick={() => setSearch(true)}
        className="flex h-10 min-w-0 flex-1 items-center gap-2.5 rounded-xl border border-line bg-surface px-3 text-left text-sm text-faint transition-colors hover:border-line-strong sm:max-w-md"
      >
        <Search className="size-4 shrink-0" aria-hidden />
        <span className="min-w-0 flex-1 truncate">
          <span className="hidden sm:inline">{t('search')}</span>
          <span className="sm:hidden">{t('searchShort')}</span>
        </span>
        <kbd className="hidden rounded-md border border-line bg-raised px-1.5 py-0.5 font-sans text-[11px] text-muted lg:inline">Ctrl + K</kbd>
      </button>
      <div className="ml-auto flex shrink-0 items-center gap-0.5 sm:gap-1">
        <HealthMenu />
        <NotificationsMenu />
        <TopButton icon={theme === 'dark' ? Moon : Sun} label={theme === 'dark' ? t('themeLight') : t('themeDark')} onClick={toggleTheme} />
        {isAdmin && (
          <Link
            to="/settings"
            aria-label={t('settings')}
            title={t('settings')}
            className="hidden size-10 items-center justify-center rounded-xl text-muted transition-colors hover:bg-raised hover:text-fg sm:inline-flex"
          >
            <Settings className="size-5" aria-hidden />
          </Link>
        )}
        <UserMenu />
      </div>
    </header>
  )
}
