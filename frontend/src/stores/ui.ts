import { create } from 'zustand'

export type Theme = 'dark' | 'light'
export type ToastKind = 'success' | 'error' | 'info' | 'warning'

export interface Toast {
  id: number
  kind: ToastKind
  message: string
}

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    // Storage may be unavailable (private mode); the preference is then
    // kept for this page load only.
  }
}

function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#070b14' : '#f3f5fa')
}

interface UIState {
  theme: Theme
  sidebarCollapsed: boolean
  mobileNavOpen: boolean
  searchOpen: boolean
  toasts: Toast[]
  setTheme: (theme: Theme) => void
  toggleTheme: () => void
  toggleSidebar: () => void
  setMobileNav: (open: boolean) => void
  setSearch: (open: boolean) => void
  dismissToast: (id: number) => void
}

const initialTheme: Theme = read('myserver.theme') === 'light' ? 'light' : 'dark'
applyTheme(initialTheme)

let nextToastId = 1

export const useUI = create<UIState>((set, get) => ({
  theme: initialTheme,
  sidebarCollapsed: read('myserver.sidebar') === 'collapsed',
  mobileNavOpen: false,
  searchOpen: false,
  toasts: [],

  setTheme: (theme) => {
    applyTheme(theme)
    write('myserver.theme', theme)
    set({ theme })
  },
  toggleTheme: () => get().setTheme(get().theme === 'dark' ? 'light' : 'dark'),
  toggleSidebar: () => {
    const collapsed = !get().sidebarCollapsed
    write('myserver.sidebar', collapsed ? 'collapsed' : 'open')
    set({ sidebarCollapsed: collapsed })
  },
  setMobileNav: (open) => set({ mobileNavOpen: open }),
  setSearch: (open) => set({ searchOpen: open }),
  dismissToast: (id) => set({ toasts: get().toasts.filter((t) => t.id !== id) }),
}))

function push(kind: ToastKind, message: string): void {
  const id = nextToastId++
  useUI.setState((s) => ({ toasts: [...s.toasts.slice(-4), { id, kind, message }] }))
  window.setTimeout(() => useUI.getState().dismissToast(id), kind === 'error' ? 8000 : 4500)
}

/** Transient feedback for the result of a user action. */
export const toast = {
  success: (message: string) => push('success', message),
  error: (message: string) => push('error', message),
  info: (message: string) => push('info', message),
  warning: (message: string) => push('warning', message),
}
