import { Check, Moon, PanelLeft, Sun, type LucideIcon } from 'lucide-react'
import { Card, CardHeader, Switch } from '@/components/ui'
import { cx } from '@/lib/format'
import { messages } from '@/i18n'
import { useUI, type Theme } from '@/stores/ui'

const t = messages({
  tr: {
    title: 'Görünüm',
    subtitle: 'Bu tercihler yalnızca bu tarayıcıda saklanır',
    theme: 'Tema',
    dark: 'Koyu',
    darkText: 'Koyu lacivert arka plan. Varsayılan görünüm.',
    light: 'Açık',
    lightText: 'Aydınlık ortamlar için açık arka plan.',
    sidebar: 'Kenar çubuğunu daralt',
    sidebarText: 'Geniş ekranlarda menüyü yalnızca simgelerle gösterir.',
  },
})

function ThemeOption({ value, label, text, icon: Icon }: { value: Theme; label: string; text: string; icon: LucideIcon }) {
  const theme = useUI((s) => s.theme)
  const setTheme = useUI((s) => s.setTheme)
  const active = theme === value
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      onClick={() => setTheme(value)}
      className={cx(
        'flex items-start gap-3 rounded-xl border p-4 text-left transition-colors',
        active ? 'border-accent bg-accent/8' : 'border-line bg-surface hover:border-line-strong',
      )}
    >
      <Icon className={cx('mt-0.5 size-5 shrink-0', active ? 'text-accent' : 'text-muted')} aria-hidden />
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-fg">{label}</span>
        <span className="mt-0.5 block text-xs text-muted">{text}</span>
      </span>
      {active && <Check className="size-4 shrink-0 text-accent" aria-hidden />}
    </button>
  )
}

export function AppearanceSection() {
  const collapsed = useUI((s) => s.sidebarCollapsed)
  const toggle = useUI((s) => s.toggleSidebar)
  return (
    <Card>
      <CardHeader title={t('title')} subtitle={t('subtitle')} />
      <div className="flex max-w-2xl flex-col gap-5">
        <div>
          <p className="mb-2 text-xs font-medium text-muted">{t('theme')}</p>
          <div role="radiogroup" aria-label={t('theme')} className="grid gap-3 sm:grid-cols-2">
            <ThemeOption value="dark" label={t('dark')} text={t('darkText')} icon={Moon} />
            <ThemeOption value="light" label={t('light')} text={t('lightText')} icon={Sun} />
          </div>
        </div>
        <div className="hidden items-center gap-3 rounded-xl border border-line bg-surface p-4 lg:flex">
          <PanelLeft className="size-5 shrink-0 text-muted" aria-hidden />
          <span className="min-w-0 flex-1">
            <span className="block text-sm font-medium text-fg">{t('sidebar')}</span>
            <span className="mt-0.5 block text-xs text-muted">{t('sidebarText')}</span>
          </span>
          <Switch checked={collapsed} onChange={toggle} label={t('sidebar')} />
        </div>
      </div>
    </Card>
  )
}
