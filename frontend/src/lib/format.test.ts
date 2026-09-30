import { describe, expect, it } from 'vitest'
import { clamp, cx, formatBytes, formatDateTime, formatDuration, formatPercent, formatRate, formatRelative, formatShortDateTime, formatTemperature, formatTime } from './format'

const DASH = '—'

describe('formatBytes', () => {
  it.each([
    [0, '0 B'],
    [1, '1 B'],
    [1023, '1.023 B'],
    [1024, '1 KB'],
    [1536, '1,5 KB'],
    [1024 * 1024, '1 MB'],
    [1.25 * 1024 ** 3, '1,3 GB'],
    [150 * 1024 ** 2, '150 MB'],
    [99.95 * 1024 ** 2, '100 MB'],
    [4 * 1024 ** 4, '4 TB'],
    [3 * 1024 ** 5, '3 PB'],
    [5000 * 1024 ** 5, '5.000 PB'],
  ])('%d -> %s', (input, expected) => {
    expect(formatBytes(input)).toBe(expected)
  })

  it('ondalık ayırıcı olarak virgül kullanır', () => {
    expect(formatBytes(1536)).toContain(',')
    expect(formatBytes(1536)).not.toContain('.')
    expect(formatBytes(1234567, 2)).toBe('1,18 MB')
  })

  it('null, undefined, NaN ve sonsuz için tire döndürür', () => {
    for (const v of [null, undefined, NaN, Infinity, -Infinity]) expect(formatBytes(v)).toBe(DASH)
  })

  it('negatif değerleri işaretiyle yazar', () => {
    expect(formatBytes(-1536)).toBe('-1,5 KB')
  })
})

describe('formatRate', () => {
  it('saniye başına birim ekler', () => {
    expect(formatRate(13002342)).toBe('12,4 MB/s')
    expect(formatRate(0)).toBe('0 B/s')
  })
  it('geçersiz girdide tire döndürür', () => {
    for (const v of [null, undefined, NaN]) expect(formatRate(v)).toBe(DASH)
  })
})

describe('formatPercent', () => {
  it('Türkçe biçimde, yüzde işareti önde yazar', () => {
    expect(formatPercent(42)).toBe('%42')
    expect(formatPercent(42.6)).toBe('%43')
    expect(formatPercent(42.64, 1)).toBe('%42,6')
    expect(formatPercent(0)).toBe('%0')
    expect(formatPercent(100)).toBe('%100')
  })
  it('geçersiz girdide tire döndürür', () => {
    for (const v of [null, undefined, NaN, Infinity]) expect(formatPercent(v)).toBe(DASH)
  })
})

describe('formatTemperature', () => {
  it('tam sayıya yuvarlar', () => {
    expect(formatTemperature(45.4)).toBe('45°C')
    expect(formatTemperature(45.5)).toBe('46°C')
    expect(formatTemperature(0)).toBe('0°C')
    expect(formatTemperature(-3.2)).toBe('-3°C')
  })
  it('sensör yoksa (null) tire döndürür', () => {
    for (const v of [null, undefined, NaN]) expect(formatTemperature(v)).toBe(DASH)
  })
})

describe('formatDuration', () => {
  const M = 60
  const H = 3600
  const D = 86400
  it.each([
    [0, '0 saniye'],
    [1, '1 saniye'],
    [59, '59 saniye'],
    [59.9, '59 saniye'],
    [60, '1 dakika'],
    [119, '1 dakika'],
    [45 * M, '45 dakika'],
    [H, '1 saat'],
    [H + 5 * M, '1 saat 5 dakika'],
    [23 * H + 59 * M + 59, '23 saat 59 dakika'],
    [D, '1 gün'],
    [D + 30, '1 gün'],
    [3 * D + 4 * H + 12 * M, '3 gün 4 saat 12 dakika'],
    [29 * D + 23 * H + 59 * M, '29 gün 23 saat 59 dakika'],
    [30 * D, '30 gün'],
    [30 * D + 5 * H + 20 * M, '30 gün 5 saat'],
    [400 * D + 10 * M, '400 gün'],
  ])('%d sn -> %s', (input, expected) => {
    expect(formatDuration(input)).toBe(expected)
  })

  it('null, NaN, sonsuz ve negatif için tire döndürür', () => {
    for (const v of [null, undefined, NaN, Infinity, -1]) expect(formatDuration(v)).toBe(DASH)
  })
})

describe('formatRelative', () => {
  const now = new Date('2026-09-29T12:00:00Z')
  const ago = (seconds: number) => Math.floor(now.getTime() / 1000) - seconds

  it.each([
    [0, 'az önce'],
    [44, 'az önce'],
    [45, '1 dakika önce'],
    [60, '1 dakika önce'],
    [59 * 60 + 59, '59 dakika önce'],
    [3600, '1 saat önce'],
    [23 * 3600 + 3599, '23 saat önce'],
    [86400, '1 gün önce'],
    [29 * 86400 + 86399, '29 gün önce'],
  ])('%d saniye önce -> %s', (seconds, expected) => {
    expect(formatRelative(ago(seconds), now)).toBe(expected)
  })

  it('30 gün ve sonrası için tarihi yazar', () => {
    const d = new Date(now.getTime() - 30 * 86400 * 1000)
    expect(formatRelative(d, now)).toBe(formatShortDateTime(d))
    expect(formatRelative(d, now)).toMatch(/^\d{2}\.\d{2}\.\d{4} \d{2}:\d{2}$/)
  })

  it('gelecekteki zaman için tarihi yazar', () => {
    const d = new Date(now.getTime() + 60_000)
    expect(formatRelative(d, now)).toBe(formatShortDateTime(d))
  })

  it('sayıları Unix saniyesi, dizgileri ISO tarih olarak yorumlar', () => {
    expect(formatRelative(now.getTime() / 1000 - 120, now)).toBe('2 dakika önce')
    expect(formatRelative('2026-09-29T11:00:00Z', now)).toBe('1 saat önce')
  })

  it('null ve geçersiz tarih için tire döndürür', () => {
    expect(formatRelative(null, now)).toBe(DASH)
    expect(formatRelative(undefined, now)).toBe(DASH)
    expect(formatRelative('tarih değil', now)).toBe(DASH)
    expect(formatRelative(NaN, now)).toBe(DASH)
  })
})

describe('tarih biçimleri', () => {
  // Built from local-time components so the result does not depend on the
  // machine's time zone.
  const d = new Date(2026, 8, 29, 1, 45)

  it('Türkçe ay adıyla uzun tarih yazar', () => {
    expect(formatDateTime(d)).toBe('29 Eylül 2026 01:45')
  })
  it('kısa tarih ve saat yazar', () => {
    expect(formatShortDateTime(d)).toBe('29.09.2026 01:45')
    expect(formatTime(d)).toBe('01:45')
  })
  it('Unix saniyesini kabul eder', () => {
    expect(formatShortDateTime(d.getTime() / 1000)).toBe('29.09.2026 01:45')
  })
  it('null ve geçersiz tarih için tire döndürür', () => {
    for (const f of [formatDateTime, formatShortDateTime, formatTime]) {
      expect(f(null)).toBe(DASH)
      expect(f(undefined)).toBe(DASH)
      expect(f('x')).toBe(DASH)
      expect(f(NaN)).toBe(DASH)
    }
  })
})

describe('yardımcılar', () => {
  it('clamp değeri aralığa sıkıştırır', () => {
    expect(clamp(5, 0, 10)).toBe(5)
    expect(clamp(-5, 0, 10)).toBe(0)
    expect(clamp(50, 0, 10)).toBe(10)
  })
  it('cx boş değerleri atlar', () => {
    expect(cx('a', false, null, undefined, '', 'b')).toBe('a b')
  })
})
