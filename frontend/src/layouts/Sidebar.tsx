import type { ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { PanelLeftClose, PanelLeftOpen, X } from 'lucide-react'
import { cx } from '@/lib/format'
import { useAuth } from '@/stores/auth'
import { useUI } from '@/stores/ui'
import { messages } from '@/i18n'
import { brandIcon as Brand, navItems, navText, type NavId } from './navigation'

const t = messages({
  tr: {
    collapse: 'Kenar çubuğunu daralt',
    expand: 'Kenar çubuğunu genişlet',
    close: 'Menüyü kapat',
    nav: 'Ana menü',
  },
})

export interface SidebarProps {
  /** Badge counts per navigation item (e.g. available updates). */
  badges?: Partial<Record<NavId, number>>
  /** Rendered above the version line; receives the collapsed state. */
  footer?: (collapsed: boolean) => ReactNode
}

function Brandmark({ collapsed }: { collapsed: boolean }) {
  return (
    <div className={cx('flex h-16 shrink-0 items-center gap-2.5', collapsed ? 'justify-center px-2' : 'px-5')}>
      <span className="inline-flex size-9 shrink-0 items-center justify-center rounded-xl bg-accent-strong text-accent-fg shadow-glow">
        <Brand className="size-5" aria-hidden />
      </span>
      {!collapsed && <span className="truncate text-lg font-semibold tracking-tight text-fg">MyServer</span>}
    </div>
  )
}

function NavList({ collapsed, badges, onNavigate }: { collapsed: boolean; badges?: SidebarProps['badges']; onNavigate?: () => void }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  return (
    <nav aria-label={t('nav')} className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-3 py-2">
      {navItems
        .filter((item) => !item.adminOnly || isAdmin)
        .map((item) => {
          const badge = badges?.[item.id] ?? 0
          const label = navText(item.id)
          return (
            <NavLink
              key={item.id}
              to={item.path}
              end={item.path === '/'}
              onClick={onNavigate}
              title={collapsed ? label : undefined}
              className={({ isActive }) =>
                cx(
                  'group relative flex h-11 shrink-0 items-center gap-3 rounded-xl text-sm font-medium transition-colors',
                  collapsed ? 'justify-center px-0' : 'px-3',
                  isActive ? 'bg-accent-strong text-accent-fg shadow-glow' : 'text-muted hover:bg-raised hover:text-fg',
                )
              }
            >
              <span className="relative inline-flex">
                <item.icon className="size-5 shrink-0" aria-hidden />
                {badge > 0 && collapsed && <span className="absolute -top-1 -right-1 size-2 rounded-full bg-warning" aria-hidden />}
              </span>
              {!collapsed && <span className="min-w-0 flex-1 truncate">{label}</span>}
              {!collapsed && badge > 0 && (
                <span className="rounded-full bg-warning px-1.5 text-[11px] font-semibold leading-5 text-bg">{badge > 99 ? '99+' : badge}</span>
              )}
              {collapsed && <span className="sr-only">{label}</span>}
            </NavLink>
          )
        })}
    </nav>
  )
}

/** Desktop and tablet sidebar. Tablets always show the narrow form. */
export function Sidebar({ badges, footer }: SidebarProps) {
  const userCollapsed = useUI((s) => s.sidebarCollapsed)
  const toggle = useUI((s) => s.toggleSidebar)
  const version = useAuth((s) => s.status?.version)

  const render = (collapsed: boolean, className: string, showToggle: boolean) => (
    <aside className={cx('sticky top-0 h-dvh shrink-0 flex-col border-r border-line bg-surface transition-[width] duration-200', collapsed ? 'w-[76px]' : 'w-64', className)}>
      <Brandmark collapsed={collapsed} />
      <NavList collapsed={collapsed} badges={badges} />
      {footer && <div className={cx('shrink-0 border-t border-line', collapsed ? 'px-2 py-3' : 'px-4 py-3')}>{footer(collapsed)}</div>}
      <div className={cx('flex shrink-0 items-center border-t border-line py-2', collapsed ? 'justify-center px-2' : 'justify-between px-4')}>
        {!collapsed && <span className="truncate text-xs text-faint">MyServer v{version}</span>}
        {showToggle && (
          <button
            type="button"
            onClick={toggle}
            aria-label={collapsed ? t('expand') : t('collapse')}
            title={collapsed ? t('expand') : t('collapse')}
            className="inline-flex size-9 items-center justify-center rounded-lg text-muted transition-colors hover:bg-raised hover:text-fg"
          >
            {collapsed ? <PanelLeftOpen className="size-[18px]" aria-hidden /> : <PanelLeftClose className="size-[18px]" aria-hidden />}
          </button>
        )}
      </div>
    </aside>
  )

  return (
    <>
      {render(true, 'hidden md:flex lg:hidden', false)}
      {render(userCollapsed, 'hidden lg:flex', true)}
    </>
  )
}

/** Slide-in navigation drawer for phones. */
export function MobileDrawer({ badges, footer }: SidebarProps) {
  const open = useUI((s) => s.mobileNavOpen)
  const setOpen = useUI((s) => s.setMobileNav)
  const version = useAuth((s) => s.status?.version)
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 md:hidden">
      <div className="absolute inset-0 bg-black/60" onClick={() => setOpen(false)} aria-hidden />
      <aside role="dialog" aria-modal="true" aria-label={t('nav')} className="ms-fade-in absolute inset-y-0 left-0 flex w-72 max-w-[85vw] flex-col border-r border-line bg-surface">
        <div className="flex items-center justify-between pr-3">
          <Brandmark collapsed={false} />
          <button
            type="button"
            aria-label={t('close')}
            onClick={() => setOpen(false)}
            className="inline-flex size-10 items-center justify-center rounded-lg text-muted hover:bg-raised hover:text-fg"
          >
            <X className="size-5" aria-hidden />
          </button>
        </div>
        <NavList collapsed={false} badges={badges} onNavigate={() => setOpen(false)} />
        {footer && <div className="shrink-0 border-t border-line px-4 py-3">{footer(false)}</div>}
        <div className="shrink-0 border-t border-line px-4 py-3 text-xs text-faint">MyServer v{version}</div>
      </aside>
    </div>
  )
}

/** Bottom bar with the most used pages, for phones. */
export function BottomNav() {
  const isAdmin = useAuth((s) => s.isAdmin)
  return (
    <nav
      aria-label={t('nav')}
      className="fixed inset-x-0 bottom-0 z-30 flex border-t border-line bg-surface/95 pb-[env(safe-area-inset-bottom)] backdrop-blur md:hidden"
    >
      {navItems
        .filter((item) => item.primary && (!item.adminOnly || isAdmin))
        .map((item) => (
          <NavLink
            key={item.id}
            to={item.path}
            end={item.path === '/'}
            className={({ isActive }) =>
              cx('flex min-h-14 min-w-0 flex-1 flex-col items-center justify-center gap-1 text-[11px] font-medium', isActive ? 'text-accent' : 'text-muted')
            }
          >
            <item.icon className="size-5" aria-hidden />
            <span className="max-w-full truncate px-1">{navText(item.id)}</span>
          </NavLink>
        ))}
    </nav>
  )
}
