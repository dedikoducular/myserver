import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery } from '@/hooks/useApi'
import { api, errorMessage } from '@/services/api'
import { toast } from '@/stores/ui'
import type { SettingsMap } from '@/types/api'

export interface SettingsForm {
  /** Server values overlaid with unsaved edits. */
  values: SettingsMap
  loading: boolean
  error: string | null
  reload: () => Promise<void>
  set: (key: string, value: string) => void
  /** True when any of the given keys (or any key at all) has unsaved edits. */
  dirty: (keys?: string[]) => boolean
  saving: boolean
  /** Saves the edited values of the given keys; resolves to true on success. */
  save: (keys: string[], successMessage?: string) => Promise<boolean>
  reset: (keys?: string[]) => void
}

const EMPTY: SettingsMap = {}

/** Loads the settings and tracks edits for a settings section. */
export function useSettings(): SettingsForm {
  const { data, error, loading, reload } = useQuery<SettingsMap>('/settings')
  // Values returned by the last save; until then (and after each reload) the
  // loaded data is used directly, so no render ever sees empty settings
  // between "loaded" and "copied into state".
  const [saved, setSaved] = useState<{ from: SettingsMap | undefined; values: SettingsMap } | null>(null)
  const [edits, setEdits] = useState<SettingsMap>({})
  const [saving, setSaving] = useState(false)
  const server = (saved && saved.from === data ? saved.values : data) ?? EMPTY
  const setServer = useCallback((values: SettingsMap) => setSaved({ from: dataRef.current, values }), [])
  const dataRef = useRef(data)
  useEffect(() => {
    dataRef.current = data
  }, [data])

  const set = useCallback((key: string, value: string) => setEdits((e) => ({ ...e, [key]: value })), [])

  const dirty = useCallback(
    (keys?: string[]) => (keys ?? Object.keys(edits)).some((k) => k in edits && edits[k] !== server[k]),
    [edits, server],
  )

  const reset = useCallback((keys?: string[]) => {
    setEdits((e) => {
      if (!keys) return {}
      const next = { ...e }
      for (const k of keys) delete next[k]
      return next
    })
  }, [])

  const save = useCallback(
    async (keys: string[], successMessage = 'Ayarlar kaydedildi.') => {
      const body: SettingsMap = {}
      for (const k of keys) {
        const v = edits[k]
        if (v !== undefined && v !== server[k]) body[k] = v
      }
      if (Object.keys(body).length === 0) return true
      setSaving(true)
      try {
        const updated = await api.put<SettingsMap>('/settings', body)
        setServer(updated)
        reset(keys)
        toast.success(successMessage)
        return true
      } catch (e) {
        toast.error(errorMessage(e, 'Ayarlar kaydedilemedi.'))
        return false
      } finally {
        setSaving(false)
      }
    },
    [edits, server, reset, setServer],
  )

  return { values: { ...server, ...edits }, loading, error, reload, set, dirty, saving, save, reset }
}
