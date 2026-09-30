import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { LayoutGrid, PackageOpen, Plus, RefreshCw, Search, Store } from 'lucide-react'
import {
  Alert,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  LoadingState,
  PageHeader,
  Skeleton,
  Tabs,
  type TabItem,
} from '@/components/ui'
import { useAction } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { AppDialog } from './AppDialog'
import { InstalledApps } from './InstalledApps'
import { JobDialog } from './JobProgress'
import { CatalogCard, ViewToggle, gridClass } from './parts'
import { t } from './strings'
import type { CatalogApp, Category, Job, ReloadResponse } from './types'
import { matches, useCatalog, useInstalledApps, useViewMode } from './util'

const STORE_LINK = '/apps?view=store'

function categoryTabs(categories: Category[]): TabItem<string>[] {
  return [{ id: 'all', label: t('all') }, ...categories.map((c) => ({ id: c.id, label: c.label }))]
}

const installLinkClass =
  'inline-flex h-10 items-center justify-center gap-2 rounded-xl border border-transparent bg-accent-strong px-4 text-sm font-medium whitespace-nowrap text-accent-fg transition-colors hover:bg-accent'

function InstallLink() {
  return (
    <Link to={STORE_LINK} className={installLinkClass}>
      <Plus className="size-4 shrink-0" aria-hidden />
      {t('installApp')}
    </Link>
  )
}

function DockerAlert() {
  return (
    <Alert tone="danger" title={t('dockerDown')} className="mb-4">
      {t('dockerDownText')}
    </Alert>
  )
}

/** The "Uygulamalar" section of the dashboard: installed applications only. */
export function AppsWidget() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const installed = useInstalledApps()
  const [view, setView] = useViewMode()
  const [category, setCategory] = useState('all')

  const apps = installed.data?.apps ?? []
  const categories = installed.data?.categories ?? []
  const active = category === 'all' || categories.some((c) => c.id === category) ? category : 'all'
  const shown = apps.filter((a) => active === 'all' || a.category === active)
  const reload = () => void installed.reload()

  return (
    <Card>
      <div className="mb-4 flex flex-wrap items-center gap-x-4 gap-y-3">
        <h2 className="text-lg font-semibold text-fg">
          <Link to="/apps" className="hover:text-accent">
            {t('title')}
          </Link>
        </h2>
        {apps.length > 0 && <Tabs items={categoryTabs(categories)} value={active} onChange={setCategory} label={t('categoriesLabel')} className="min-w-0" />}
        <div className="ml-auto flex items-center gap-2">
          {isAdmin && <InstallLink />}
          <ViewToggle value={view} onChange={setView} />
        </div>
      </div>

      {installed.data && !installed.data.docker_available && apps.length > 0 && <DockerAlert />}

      {installed.loading ? (
        <div className={gridClass('grid')} aria-busy="true">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-[92px] rounded-2xl" />
          ))}
        </div>
      ) : installed.error && !installed.data ? (
        <ErrorState message={installed.error} onRetry={reload} className="py-6" />
      ) : apps.length === 0 ? (
        <EmptyState
          icon={PackageOpen}
          title={t('emptyInstalledTitle')}
          description={isAdmin ? t('emptyInstalledText') : t('emptyInstalledUser')}
          action={isAdmin ? <InstallLink /> : undefined}
          className="py-8"
        />
      ) : (
        <InstalledApps apps={shown} view={view} onChanged={reload} />
      )}
    </Card>
  )
}

type PageTab = 'installed' | 'store'

export default function AppsPage() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const [params, setParams] = useSearchParams()
  const tab: PageTab = params.get('view') === 'store' ? 'store' : 'installed'
  const [view, setView] = useViewMode()
  const [category, setCategory] = useState('all')
  const [search, setSearch] = useState('')
  const [installSlug, setInstallSlug] = useState<string | null>(null)
  const [job, setJob] = useState<Pick<Job, 'id' | 'name' | 'kind'> | null>(null)

  const catalog = useCatalog(true)
  const reloadCatalog = catalog.reload
  const installed = useInstalledApps(() => void reloadCatalog())

  const refresh = () => {
    void installed.reload()
    void catalog.reload()
  }

  const reloadManifests = useAction(() => api.post<ReloadResponse>('/apps/reload'), {
    onSuccess: (res) => {
      if (res.invalid.length > 0) toast.warning(t('reloadInvalid', { valid: res.valid, invalid: res.invalid.length }))
      else toast.success(t('reloadDone', { valid: res.valid }))
      refresh()
    },
    onError: (message) => toast.error(message),
  })

  const setTab = (next: PageTab) => {
    setCategory('all')
    setParams(next === 'store' ? { view: 'store' } : {}, { replace: true })
  }

  const source = tab === 'store' ? catalog : installed
  const categories = (tab === 'store' ? catalog.data?.categories : installed.data?.categories) ?? []
  const active = category === 'all' || categories.some((c) => c.id === category) ? category : 'all'

  const installedApps = useMemo(
    () => (installed.data?.apps ?? []).filter((a) => (active === 'all' || a.category === active) && matches(a, search)),
    [installed.data, active, search],
  )
  const storeApps = useMemo(
    () => (catalog.data?.apps ?? []).filter((a) => (active === 'all' || a.category === active) && matches(a, search)),
    [catalog.data, active, search],
  )

  const tabs: TabItem<PageTab>[] = [
    { id: 'installed', label: t('tabInstalled'), count: installed.data?.apps.length },
    { id: 'store', label: t('tabStore'), count: catalog.data?.apps.length },
  ]

  const selectCatalogApp = (app: CatalogApp) => {
    if (app.job_id && app.operation === 'install') {
      setJob({ id: app.job_id, name: app.name, kind: 'install' })
      return
    }
    setInstallSlug(app.slug)
  }

  let content
  if (source.loading) {
    content = <LoadingState label={t('loadingApps')} />
  } else if (source.error && !source.data) {
    content = <ErrorState message={source.error} onRetry={refresh} />
  } else if (tab === 'installed') {
    const total = installed.data?.apps.length ?? 0
    if (total === 0) {
      content = (
        <EmptyState
          icon={PackageOpen}
          title={t('emptyInstalledTitle')}
          description={isAdmin ? t('emptyInstalledText') : t('emptyInstalledUser')}
          action={
            <Button variant="primary" icon={Store} onClick={() => setTab('store')}>
              {t('openStore')}
            </Button>
          }
        />
      )
    } else if (installedApps.length === 0) {
      content = <EmptyState icon={Search} title={t('emptyFilterTitle')} description={t('emptyFilterText')} />
    } else {
      content = <InstalledApps apps={installedApps} view={view} onChanged={refresh} />
    }
  } else {
    const total = catalog.data?.apps.length ?? 0
    if (total === 0) {
      content = <EmptyState icon={Store} title={t('emptyStoreTitle')} description={catalog.data?.load_error || t('emptyStoreText')} />
    } else if (storeApps.length === 0) {
      content = <EmptyState icon={Search} title={t('emptyFilterTitle')} description={t('emptyFilterText')} />
    } else {
      content = (
        <div className={gridClass(view)}>
          {storeApps.map((app) => (
            <CatalogCard key={app.slug} app={app} view={view} isAdmin={isAdmin} onSelect={selectCatalogApp} />
          ))}
        </div>
      )
    }
  }

  const invalid = catalog.data?.invalid ?? []

  return (
    <div>
      <PageHeader
        title={t('title')}
        description={t('pageDescription')}
        icon={LayoutGrid}
        actions={
          <>
            {isAdmin && (
              <Button icon={RefreshCw} loading={reloadManifests.pending} onClick={() => void reloadManifests.run()}>
                {t('reload')}
              </Button>
            )}
            {isAdmin && tab === 'installed' && (
              <Button variant="primary" icon={Plus} onClick={() => setTab('store')}>
                {t('installApp')}
              </Button>
            )}
          </>
        }
      />

      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Tabs items={tabs} value={tab} onChange={setTab} label={t('tabsLabel')} />
        <div className="relative min-w-0 flex-1 basis-48 sm:max-w-xs">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-faint" aria-hidden />
          <Input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('search')}
            aria-label={t('searchLabel')}
            className="pl-9"
          />
        </div>
        <div className="ml-auto">
          <ViewToggle value={view} onChange={setView} />
        </div>
      </div>

      {categories.length > 0 && (
        <Tabs items={categoryTabs(categories)} value={active} onChange={setCategory} label={t('categoriesLabel')} className="mb-4 w-fit" />
      )}

      {tab === 'installed' && installed.data && !installed.data.docker_available && installed.data.apps.length > 0 && <DockerAlert />}

      {tab === 'store' && isAdmin && invalid.length > 0 && (
        <Alert tone="warning" title={t('invalidTitle')} className="mb-4">
          <p>{t('invalidText')}</p>
          <ul className="mt-1.5 flex flex-col gap-1">
            {invalid.map((m) => (
              <li key={m.file} className="break-words">
                <span className="font-mono text-fg">{m.file}</span>: {m.error}
              </li>
            ))}
          </ul>
        </Alert>
      )}

      {content}

      <AppDialog slug={installSlug} mode="install" onClose={() => setInstallSlug(null)} onChanged={refresh} />
      <JobDialog job={job} onClose={() => setJob(null)} onFinished={refresh} />
    </div>
  )
}
