import type { ComponentType } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  Bell,
  Container,
  HardDrive,
  History,
  Network,
  Palette,
  RefreshCw,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
  Users,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { PageHeader } from '@/components/ui'
import { cx } from '@/lib/format'
import { messages } from '@/i18n'
import { lazyNamed, Slot } from '@/modules/slot'
import { AdvancedSection } from './Advanced'
import { AppearanceSection } from './Appearance'
import { GeneralSection } from './General'
import { NotificationsSection } from './Notifications'
import { SecuritySection } from './Security'
import { UsersSection } from './Users'

const t = messages({
  tr: {
    title: 'Ayarlar',
    description: 'Panel ve sunucu yapılandırması',
    sections: 'Ayar bölümleri',
    general: 'Genel',
    users: 'Kullanıcılar',
    security: 'Güvenlik',
    storage: 'Depolama',
    docker: 'Docker',
    network: 'Ağ',
    notifications: 'Bildirimler',
    backup: 'Yedekleme',
    updates: 'Güncellemeler',
    appearance: 'Görünüm',
    advanced: 'Gelişmiş',
    firewall: 'Güvenlik Duvarı',
    terminal: 'Terminal',
    files: 'Dosya Yöneticisi',
    disks: 'Diskler',
    thresholds: 'Sağlık Eşikleri',
    services: 'Servisler',
  },
})

const FirewallSettings = lazyNamed(() => import('@/modules/network'), 'FirewallSettingsSection')
const NetworkSettings = lazyNamed(() => import('@/modules/network'), 'NetworkSettingsSection')
const TerminalSettings = lazyNamed(() => import('@/modules/terminal'), 'TerminalSettingsSection')
const FilesSettings = lazyNamed(() => import('@/modules/files'), 'FilesSettingsSection')
const StorageSettings = lazyNamed(() => import('@/modules/storage'), 'StorageSettingsSection')
const DockerSettings = lazyNamed(() => import('@/modules/docker'), 'DockerSettingsSection')
const UpdatesSettings = lazyNamed(() => import('@/modules/updates'), 'UpdatesSettingsSection')
const BackupSettings = lazyNamed(() => import('@/modules/backup'), 'BackupSettingsSection')
const SystemSettings = lazyNamed(() => import('@/modules/system'), 'SystemSettingsSection')
const ServicesSettings = lazyNamed(() => import('@/modules/services'), 'ServicesSettingsSection')

type SectionId =
  | 'general'
  | 'users'
  | 'security'
  | 'storage'
  | 'docker'
  | 'network'
  | 'notifications'
  | 'backup'
  | 'updates'
  | 'appearance'
  | 'advanced'

interface Part {
  name: string
  component: ComponentType
}

interface Section {
  id: SectionId
  label: string
  icon: LucideIcon
  /** Core sections rendered directly. */
  core?: ComponentType[]
  /** Module-provided sections, each isolated. */
  parts?: Part[]
}

export const settingsSections: Section[] = [
  { id: 'general', label: t('general'), icon: SlidersHorizontal, core: [GeneralSection] },
  { id: 'users', label: t('users'), icon: Users, core: [UsersSection] },
  {
    id: 'security',
    label: t('security'),
    icon: ShieldCheck,
    parts: [
      { name: t('firewall'), component: FirewallSettings },
      { name: t('terminal'), component: TerminalSettings },
    ],
    core: [SecuritySection],
  },
  {
    id: 'storage',
    label: t('storage'),
    icon: HardDrive,
    parts: [
      { name: t('files'), component: FilesSettings },
      { name: t('disks'), component: StorageSettings },
    ],
  },
  { id: 'docker', label: t('docker'), icon: Container, parts: [{ name: t('docker'), component: DockerSettings }] },
  { id: 'network', label: t('network'), icon: Network, parts: [{ name: t('network'), component: NetworkSettings }] },
  {
    id: 'notifications',
    label: t('notifications'),
    icon: Bell,
    parts: [{ name: t('thresholds'), component: SystemSettings }],
    core: [NotificationsSection],
  },
  { id: 'backup', label: t('backup'), icon: History, parts: [{ name: t('backup'), component: BackupSettings }] },
  { id: 'updates', label: t('updates'), icon: RefreshCw, parts: [{ name: t('updates'), component: UpdatesSettings }] },
  { id: 'appearance', label: t('appearance'), icon: Palette, core: [AppearanceSection] },
  {
    id: 'advanced',
    label: t('advanced'),
    icon: Wrench,
    parts: [{ name: t('services'), component: ServicesSettings }],
    core: [AdvancedSection],
  },
]

export default function SettingsPage() {
  const [params, setParams] = useSearchParams()
  const requested = params.get('section')
  const current = settingsSections.find((s) => s.id === requested) ?? settingsSections[0]!

  return (
    <div>
      <PageHeader title={t('title')} description={t('description')} icon={Settings} />
      <div className="grid gap-4 lg:grid-cols-[220px_minmax(0,1fr)]">
        <nav aria-label={t('sections')} className="-mx-3 flex gap-1 overflow-x-auto px-3 pb-1 lg:mx-0 lg:flex-col lg:overflow-visible lg:px-0 lg:pb-0">
          {settingsSections.map((s) => {
            const active = s.id === current.id
            return (
              <button
                key={s.id}
                type="button"
                aria-current={active ? 'page' : undefined}
                onClick={() => setParams({ section: s.id }, { replace: true })}
                className={cx(
                  'flex h-10 shrink-0 items-center gap-2.5 rounded-xl px-3 text-left text-sm font-medium whitespace-nowrap transition-colors',
                  active ? 'bg-accent-strong text-accent-fg' : 'text-muted hover:bg-raised hover:text-fg',
                )}
              >
                <s.icon className="size-4 shrink-0" aria-hidden />
                {s.label}
              </button>
            )
          })}
        </nav>
        <div key={current.id} className="ms-fade-in flex min-w-0 flex-col gap-4">
          {current.parts?.map((p) => <Slot key={p.name} name={p.name} component={p.component} />)}
          {current.core?.map((C, i) => <C key={i} />)}
        </div>
      </div>
    </div>
  )
}
