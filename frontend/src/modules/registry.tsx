import type { ComponentType } from 'react'
import type { NavId } from '@/layouts/navigation'
import { lazyNamed, type Reloadable } from './slot'

// The single place where feature modules are attached to the shell. Each
// page is loaded lazily and rendered inside its own error boundary, so a
// module that fails to load or crashes does not affect the others.

export interface ModuleRoute {
  id: NavId
  path: string
  /** Reloadable, so "Tekrar Dene" can recover a page whose code failed to
   *  load (for example during a brief network loss). */
  page: ComponentType & Reloadable
  adminOnly?: boolean
}

export const moduleRoutes: ModuleRoute[] = [
  { id: 'home', path: '/', page: lazyNamed(() => import('@/pages/dashboard'), 'default') },
  { id: 'apps', path: '/apps', page: lazyNamed(() => import('@/modules/apps'), 'default') },
  { id: 'docker', path: '/docker', page: lazyNamed(() => import('@/modules/docker'), 'default') },
  { id: 'files', path: '/files', page: lazyNamed(() => import('@/modules/files'), 'default') },
  { id: 'storage', path: '/storage', page: lazyNamed(() => import('@/modules/storage'), 'default') },
  { id: 'network', path: '/network', page: lazyNamed(() => import('@/modules/network'), 'default') },
  { id: 'services', path: '/services', page: lazyNamed(() => import('@/modules/services'), 'default') },
  { id: 'terminal', path: '/terminal', page: lazyNamed(() => import('@/modules/terminal'), 'default'), adminOnly: true },
  { id: 'backup', path: '/backup', page: lazyNamed(() => import('@/modules/backup'), 'default'), adminOnly: true },
  { id: 'updates', path: '/updates', page: lazyNamed(() => import('@/modules/updates'), 'default') },
  { id: 'settings', path: '/settings', page: lazyNamed(() => import('@/pages/settings'), 'default'), adminOnly: true },
]
