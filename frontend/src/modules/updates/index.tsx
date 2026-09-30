import { RefreshCw } from 'lucide-react'
import { PageHeader } from '@/components/ui'
import { AptSection } from './AptSection'
import { DockerSection } from './DockerSection'
import { SelfSection } from './SelfSection'
import { t } from './strings'

export { UpdatesWidget } from './Widget'
export { UpdatesSettingsSection } from './SettingsSection'
export { useUpdateCount } from './summary'

export default function UpdatesPage() {
  return (
    <div className="flex flex-col">
      <PageHeader title={t('pageTitle')} description={t('pageDescription')} icon={RefreshCw} />
      <div className="flex flex-col gap-5">
        <AptSection />
        <DockerSection />
        <SelfSection />
      </div>
    </div>
  )
}
