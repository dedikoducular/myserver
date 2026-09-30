import { create } from 'zustand'
import { api, setCsrfToken, setUnauthorizedHandler } from '@/services/api'
import { setLanguage } from '@/i18n'
import type { AuthStatus, SetupResult, User } from '@/types/api'

interface SetupInput {
  username: string
  password: string
  hostname: string
  timezone: string
}

interface AuthState {
  /** null until the first status request finishes. */
  status: AuthStatus | null
  loadError: string | null
  user: User | null
  isAdmin: boolean
  load: () => Promise<void>
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  setup: (input: SetupInput) => Promise<string[]>
  /** Applies a status the caller obtained itself (the setup wizard holds
   *  its result back until the user leaves the result screen). */
  adopt: (status: AuthStatus) => void
}

function apply(status: AuthStatus) {
  setCsrfToken(status.csrf_token)
  setLanguage(status.language)
  return {
    status,
    loadError: null,
    user: status.user,
    isAdmin: status.user?.role === 'admin',
  }
}

export const useAuth = create<AuthState>((set, get) => ({
  status: null,
  loadError: null,
  user: null,
  isAdmin: false,

  load: async () => {
    try {
      set(apply(await api.get<AuthStatus>('/auth/status')))
    } catch (e) {
      set({ loadError: e instanceof Error ? e.message : 'Sunucuya ulaşılamıyor.' })
    }
  },

  login: async (username, password) => {
    set(apply(await api.post<AuthStatus>('/auth/login', { username, password })))
  },

  logout: async () => {
    try {
      await api.post('/auth/logout')
    } finally {
      const prev = get().status
      if (prev) set(apply({ ...prev, authenticated: false, user: null, csrf_token: '' }))
    }
  },

  setup: async (input) => {
    const res = await api.post<SetupResult>('/auth/setup', input)
    const { warnings, ...status } = res
    set(apply(status))
    return warnings
  },

  adopt: (status) => set(apply(status)),
}))

// When any request reports the session is gone, fall back to the login page.
setUnauthorizedHandler(() => {
  const prev = useAuth.getState().status
  if (prev?.authenticated) {
    useAuth.setState(apply({ ...prev, authenticated: false, user: null, csrf_token: '' }))
  }
})
