import { useEffect } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { useUI } from '@/stores/ui'
import { BottomNav, MobileDrawer, Sidebar, type SidebarProps } from './Sidebar'
import { SearchPalette } from './SearchPalette'
import { Topbar } from './Topbar'

/** Application shell: sidebar, top bar and the routed page. */
export function AppLayout({ badges, footer }: SidebarProps) {
  const location = useLocation()
  const setMobileNav = useUI((s) => s.setMobileNav)

  useEffect(() => {
    setMobileNav(false)
    window.scrollTo({ top: 0 })
  }, [location.pathname, setMobileNav])

  return (
    <div className="flex min-h-dvh bg-bg text-fg">
      <Sidebar badges={badges} footer={footer} />
      <MobileDrawer badges={badges} footer={footer} />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar />
        <main className="mx-auto w-full max-w-[1680px] min-w-0 flex-1 px-3 pt-4 pb-24 sm:px-5 sm:pt-5 md:pb-8">
          <Outlet />
        </main>
      </div>
      <BottomNav />
      <SearchPalette />
    </div>
  )
}
