import { messages } from '@/i18n'
import { lazyNamed, Slot } from '@/modules/slot'
import { useAuth } from '@/stores/auth'
import { QuickActionsWidget, RecentLogsWidget } from './CoreWidgets'

const t = messages({
  tr: {
    status: 'Sunucu Durumu',
    metrics: 'Sistem Ölçümleri',
    apps: 'Uygulamalar',
    storage: 'Depolama',
    info: 'Sistem Bilgileri',
    traffic: 'Ağ Trafiği',
    services: 'Servis Durumları',
    containers: 'Docker Konteynerleri',
    updates: 'Güncellemeler',
  },
})

const system = () => import('@/modules/system')
const ServerStatusHeader = lazyNamed(system, 'ServerStatusHeader')
const MetricCards = lazyNamed(system, 'MetricCards')
const StorageUsageWidget = lazyNamed(system, 'StorageUsageWidget')
const SystemInfoWidget = lazyNamed(system, 'SystemInfoWidget')
const TrafficWidget = lazyNamed(system, 'TrafficWidget')
const AppsWidget = lazyNamed(() => import('@/modules/apps'), 'AppsWidget')
const ServicesWidget = lazyNamed(() => import('@/modules/services'), 'ServicesWidget')
const ContainersWidget = lazyNamed(() => import('@/modules/docker'), 'ContainersWidget')
const UpdatesWidget = lazyNamed(() => import('@/modules/updates'), 'UpdatesWidget')

export default function DashboardPage() {
  const isAdmin = useAuth((s) => s.isAdmin)
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_300px]">
        <div className="flex min-w-0 flex-col gap-4">
          <Slot name={t('status')} component={ServerStatusHeader} bare />
          <Slot name={t('metrics')} component={MetricCards} />
          <Slot name={t('apps')} component={AppsWidget} />
        </div>
        <QuickActionsWidget />
      </div>

      <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-4">
        <Slot name={t('storage')} component={StorageUsageWidget} />
        <Slot name={t('info')} component={SystemInfoWidget} />
        <Slot name={t('traffic')} component={TrafficWidget} />
        <Slot name={t('services')} component={ServicesWidget} />
      </div>

      <div className="grid gap-4 lg:grid-cols-2 2xl:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,0.8fr)]">
        <div className="min-w-0 lg:col-span-2 2xl:col-span-1">
          <Slot name={t('containers')} component={ContainersWidget} />
        </div>
        {isAdmin && <RecentLogsWidget />}
        <Slot name={t('updates')} component={UpdatesWidget} />
      </div>
    </div>
  )
}
