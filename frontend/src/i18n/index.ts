// Minimal i18n. Turkish is the only shipped language; every user-facing
// string lives in a messages() table so another language can be added by
// filling in the same keys.
//
//   const t = messages({ tr: { title: 'Depolama', free: '{size} boş' } })
//   t('free', { size: '12 GB' })

export type Language = 'tr' | 'en'

let current: Language = 'tr'

export function setLanguage(lang: string): void {
  current = lang === 'en' ? 'en' : 'tr'
  document.documentElement.lang = current
}

export function getLanguage(): Language {
  return current
}

type Table = Record<string, string>

export function messages<T extends Table>(tables: { tr: T } & Partial<Record<Exclude<Language, 'tr'>, Partial<T>>>) {
  return function t(key: keyof T & string, vars?: Record<string, string | number>): string {
    // Own properties only: a key or variable named like an Object.prototype
    // member ("constructor", "toString") must not resolve to that member.
    const own = (table: Partial<T> | undefined): string | undefined =>
      table && Object.hasOwn(table, key) ? table[key] : undefined
    const text = own(tables[current]) ?? own(tables.tr) ?? key
    if (!vars) return text
    return text.replace(/\{(\w+)\}/g, (match, name: string) => (Object.hasOwn(vars, name) ? String(vars[name]) : match))
  }
}
