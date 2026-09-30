// Formatting helpers. All output is Turkish.

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const

/** 1536 -> "1,5 KB" (binary units, Turkish decimal comma). */
export function formatBytes(bytes: number | null | undefined, digits = 1): string {
  if (bytes == null || !Number.isFinite(bytes)) return '—'
  let value = Math.abs(bytes)
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  const d = unit === 0 || value >= 100 ? 0 : digits
  const text = value.toLocaleString('tr-TR', { minimumFractionDigits: 0, maximumFractionDigits: d })
  return `${bytes < 0 ? '-' : ''}${text} ${BYTE_UNITS[unit]}`
}

/** Bytes per second -> "12,4 MB/s". */
export function formatRate(bytesPerSecond: number | null | undefined): string {
  if (bytesPerSecond == null || !Number.isFinite(bytesPerSecond)) return '—'
  return formatBytes(bytesPerSecond) + '/s'
}

export function formatPercent(value: number | null | undefined, digits = 0): string {
  if (value == null || !Number.isFinite(value)) return '—'
  return '%' + value.toLocaleString('tr-TR', { minimumFractionDigits: 0, maximumFractionDigits: digits })
}

export function formatTemperature(celsius: number | null | undefined): string {
  if (celsius == null || !Number.isFinite(celsius)) return '—'
  return `${Math.round(celsius)}°C`
}

/** Seconds -> "3 gün 4 saat 12 dakika". */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return '—'
  const s = Math.floor(seconds)
  const days = Math.floor(s / 86400)
  const hours = Math.floor((s % 86400) / 3600)
  const minutes = Math.floor((s % 3600) / 60)
  const parts: string[] = []
  if (days > 0) parts.push(`${days} gün`)
  if (hours > 0) parts.push(`${hours} saat`)
  if (minutes > 0 && days < 30) parts.push(`${minutes} dakika`)
  if (parts.length === 0) return s < 60 ? `${s} saniye` : '1 dakika'
  return parts.join(' ')
}

function toDate(value: number | string | Date): Date {
  if (value instanceof Date) return value
  // Numbers are Unix seconds, as sent by the API.
  return typeof value === 'number' ? new Date(value * 1000) : new Date(value)
}

const dateTimeFmt = new Intl.DateTimeFormat('tr-TR', {
  day: 'numeric',
  month: 'long',
  year: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
})
const shortDateTimeFmt = new Intl.DateTimeFormat('tr-TR', {
  day: '2-digit',
  month: '2-digit',
  year: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
})
const timeFmt = new Intl.DateTimeFormat('tr-TR', { hour: '2-digit', minute: '2-digit' })

/** "29 Eylül 2026 01:45" */
export function formatDateTime(value: number | string | Date | null | undefined): string {
  if (value == null) return '—'
  const d = toDate(value)
  return Number.isNaN(d.getTime()) ? '—' : dateTimeFmt.format(d)
}

/** "29.09.2026 01:45" */
export function formatShortDateTime(value: number | string | Date | null | undefined): string {
  if (value == null) return '—'
  const d = toDate(value)
  return Number.isNaN(d.getTime()) ? '—' : shortDateTimeFmt.format(d)
}

/** "01:45" */
export function formatTime(value: number | string | Date | null | undefined): string {
  if (value == null) return '—'
  const d = toDate(value)
  return Number.isNaN(d.getTime()) ? '—' : timeFmt.format(d)
}

/** "5 dakika önce" */
export function formatRelative(value: number | string | Date | null | undefined, now: Date = new Date()): string {
  if (value == null) return '—'
  const d = toDate(value)
  if (Number.isNaN(d.getTime())) return '—'
  const diff = Math.floor((now.getTime() - d.getTime()) / 1000)
  if (diff < 0) return formatShortDateTime(d)
  if (diff < 45) return 'az önce'
  if (diff < 3600) return `${Math.max(1, Math.floor(diff / 60))} dakika önce`
  if (diff < 86400) return `${Math.floor(diff / 3600)} saat önce`
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} gün önce`
  return formatShortDateTime(d)
}

export function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

/** Joins class names, skipping falsy values. */
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}
