import { Suspense, useEffect } from 'react'
import { useUpdateCount } from '@/modules/updates/summary'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { ErrorBoundary, ErrorState, LoadingState, Toasts } from '@/components/ui'
import { AppLayout } from '@/layouts/AppLayout'
import { navText } from '@/layouts/navigation'
import { moduleRoutes, type ModuleRoute } from '@/modules/registry'
import { lazyNamed } from '@/modules/slot'
import LoginPage from '@/pages/Login'
import NotFoundPage, { ForbiddenPage } from '@/pages/NotFound'
import SetupPage from '@/pages/Setup'
import { useAuth } from '@/stores/auth'

function ModulePage({ route }: { route: ModuleRoute }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const location = useLocation()
  if (route.adminOnly && !isAdmin) return <ForbiddenPage />
  const Page = route.page
  return (
    <ErrorBoundary name={navText(route.id)} resetKey={location.pathname} onRetry={() => Page.reload?.()}>
      <Suspense fallback={<LoadingState className="py-24" />}>
        <Page />
      </Suspense>
    </ErrorBoundary>
  )
}

const SidebarSummary = lazyNamed<typeof import('@/modules/system/widgets'), 'SidebarSystemSummary', { collapsed?: boolean }>(
  () => import('@/modules/system/widgets'),
  'SidebarSystemSummary',
)

function renderSidebarFooter(collapsed: boolean) {
  return (
    <ErrorBoundary name="Sistem" compact onRetry={() => SidebarSummary.reload?.()}>
      <Suspense fallback={null}>
        <SidebarSummary collapsed={collapsed} />
      </Suspense>
    </ErrorBoundary>
  )
}

function Shell() {
  // Reads cached data only; it never triggers an update check.
  const updates = useUpdateCount()
  return <AppLayout badges={{ updates }} footer={renderSidebarFooter} />
}

export default function App() {
  const status = useAuth((s) => s.status)
  const loadError = useAuth((s) => s.loadError)
  const load = useAuth((s) => s.load)

  useEffect(() => {
    void load()
  }, [load])

  let content
  if (!status) {
    content = (
      <div className="flex min-h-dvh items-center justify-center bg-bg px-4">
        {loadError ? <ErrorState title="Panele bağlanılamadı" message={loadError} onRetry={() => void load()} /> : <LoadingState />}
      </div>
    )
  } else if (!status.setup_complete) {
    content = <SetupPage />
  } else if (!status.authenticated) {
    content = <LoginPage />
  } else {
    content = (
      <Routes>
        <Route element={<Shell />}>
          {moduleRoutes.map((route) => (
            <Route key={route.id} path={route.path === '/' ? undefined : route.path + '/*'} index={route.path === '/'} element={<ModulePage route={route} />} />
          ))}
          <Route path="login" element={<Navigate to="/" replace />} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Routes>
    )
  }

  return (
    <>
      {content}
      <Toasts />
    </>
  )
}
