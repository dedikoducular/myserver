import { afterEach, describe, expect, it } from 'vitest'
import { getLanguage, messages, setLanguage } from './index'

afterEach(() => setLanguage('tr'))

describe('messages', () => {
  const t = messages({
    tr: { title: 'Depolama', free: '{size} boş', pair: '{a} / {b} / {a}', step: 'Adım {n} / {total}' },
    en: { title: 'Storage' },
  })

  it('anahtarın metnini döndürür', () => {
    expect(t('title')).toBe('Depolama')
  })

  it('değişkenleri yerleştirir', () => {
    expect(t('free', { size: '12 GB' })).toBe('12 GB boş')
    expect(t('step', { n: 2, total: 6 })).toBe('Adım 2 / 6')
  })

  it('aynı değişkenin her geçtiği yeri doldurur', () => {
    expect(t('pair', { a: 'x', b: 'y' })).toBe('x / y / x')
  })

  it('0 ve boş dizgi değerlerini de yerleştirir', () => {
    expect(t('step', { n: 0, total: 0 })).toBe('Adım 0 / 0')
    expect(t('free', { size: '' })).toBe(' boş')
  })

  it('bilinmeyen değişkeni olduğu gibi bırakır', () => {
    expect(t('free', { other: 'x' })).toBe('{size} boş')
    expect(t('pair', { a: '1' })).toBe('1 / {b} / 1')
    expect(t('free')).toBe('{size} boş')
  })

  it('yerleştirilen değerin içindeki süslü parantezleri yeniden yorumlamaz', () => {
    expect(t('pair', { a: '{b}', b: 'y' })).toBe('{b} / y / {b}')
  })

  it('eksik anahtarda anahtarın kendisini döndürür', () => {
    const loose = t as (key: string, vars?: Record<string, string | number>) => string
    expect(loose('yok.boyle.anahtar')).toBe('yok.boyle.anahtar')
  })

  it('Object.prototype üyelerini çeviri ya da değişken sanmaz', () => {
    const loose = t as (key: string, vars?: Record<string, string | number>) => string
    expect(loose('constructor')).toBe('constructor')
    expect(loose('toString')).toBe('toString')
    const p = messages({ tr: { x: 'a {toString} b' } })
    expect(p('x', {})).toBe('a {toString} b')
  })

  it('başka dilde eksik anahtarlar Türkçeye düşer', () => {
    setLanguage('en')
    expect(t('title')).toBe('Storage')
    expect(t('free', { size: '1 GB' })).toBe('1 GB boş')
  })
})

describe('setLanguage', () => {
  it('belgenin dilini ayarlar; bilinmeyen dil Türkçe sayılır', () => {
    setLanguage('en')
    expect(getLanguage()).toBe('en')
    expect(document.documentElement.lang).toBe('en')
    setLanguage('de')
    expect(getLanguage()).toBe('tr')
    expect(document.documentElement.lang).toBe('tr')
  })
})
