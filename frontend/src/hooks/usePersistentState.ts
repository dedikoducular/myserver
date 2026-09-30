import { useCallback, useState } from 'react'

// A per-browser UI preference (chart range, list/grid view, selected tab)
// that survives a page reload. Storage can be unavailable (private mode,
// blocked site data), so every access is guarded and the value then simply
// lasts for this page load.

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
    // Not persisted; the in-memory value still applies.
  }
}

/** Like useState, but remembered in this browser under `myserver.<key>`.
 *  `allowed` lists the valid values; anything else stored (an old or edited
 *  value) falls back to `initial`. */
export function usePersistentState<T extends string>(
  key: string,
  initial: T,
  allowed: readonly T[],
): [T, (value: T) => void] {
  const storageKey = 'myserver.' + key
  const [value, setValue] = useState<T>(() => {
    const stored = read(storageKey)
    return stored !== null && (allowed as readonly string[]).includes(stored) ? (stored as T) : initial
  })
  const set = useCallback(
    (next: T) => {
      setValue(next)
      write(storageKey, next)
    },
    [storageKey],
  )
  return [value, set]
}
