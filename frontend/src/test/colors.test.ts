// Guard for the design rule in docs/MODULE_CONTRACT.md: components use only
// the token colours, never literal hex/rgb/hsl colours or Tailwind palette
// classes, because the light theme depends on the tokens.
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { describe, expect, it } from 'vitest'

// vitest runs with the frontend directory as the working directory.
const SRC = join(process.cwd(), 'src')

/** The token definitions themselves. */
const TOKEN_FILES = new Set(['styles/index.css'])

/** Documented exceptions: [file, exact text of the match, reason]. Keep short. */
const ALLOWED: Array<[file: string, match: string, reason: string]> = [
  ['stores/ui.ts', '#070b14', '<meta name="theme-color"> cannot reference a CSS variable; checked against --ms-bg below'],
  ['stores/ui.ts', '#f3f5fa', '<meta name="theme-color"> cannot reference a CSS variable; checked against --ms-bg below'],
]

// Not scanned, by design: the theme-independent utilities `text-white`,
// `bg-white`, `bg-black` (with or without opacity) used for modal backdrops,
// the switch knob and text on solid danger/accent badges; and colours that
// code reads from the tokens at run time (e.g. the xterm theme built with
// getComputedStyle), which contain no literal.

const PALETTE =
  'slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose'
const PREFIX =
  'bg|text|border(?:-[xytrblse])?|ring(?:-offset)?|outline|divide|fill|stroke|from|via|to|shadow|decoration|placeholder|caret|accent'

const RULES: Array<[name: string, pattern: RegExp]> = [
  ['hex renk', /#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})(?![0-9a-zA-Z_-])/g],
  ['rgb()/hsl() rengi', /\b(?:rgba?|hsla?|hwb|oklch|oklab|lab|lch)\(\s*[^)]*\)/g],
  ['Tailwind palet sınıfı', new RegExp(`(?<![\\w-])(?:${PREFIX})-(?:${PALETTE})-\\d{2,3}(?:/\\d+)?(?![\\w-])`, 'g')],
  ['Tailwind keyfi renk', new RegExp(`(?<![\\w-])(?:${PREFIX})-\\[(?:#|rgb|hsl)[^\\]]*\\]`, 'g')],
]

function walk(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return walk(path)
    return /\.(ts|tsx|css)$/.test(entry.name) ? [path] : []
  })
}

interface Violation {
  file: string
  line: number
  rule: string
  match: string
}

export function scan(file: string, text: string): Violation[] {
  const found: Violation[] = []
  text.split('\n').forEach((content, i) => {
    for (const [rule, pattern] of RULES) {
      for (const m of content.matchAll(pattern)) found.push({ file, line: i + 1, rule, match: m[0] })
    }
  })
  return found
}

const files = walk(SRC)
  .map((path) => ({ path, file: relative(SRC, path).split(sep).join('/') }))
  .filter(({ file }) => !/\.test\.tsx?$/.test(file) && !file.startsWith('test/') && !TOKEN_FILES.has(file))

const violations = files
  .flatMap(({ path, file }) => scan(file, readFileSync(path, 'utf8')))
  .filter((v) => !ALLOWED.some(([file, match]) => file === v.file && match === v.match))

const describeViolation = (v: Violation) => `${v.file}:${v.line}  ${v.rule}: ${v.match}`

describe('renk kuralı denetleyicisi', () => {
  it.each([
    ['className="bg-slate-800"', 'bg-slate-800'],
    ['text-gray-400 hover:text-fg', 'text-gray-400'],
    ['border-zinc-700', 'border-zinc-700'],
    ['border-t-red-500/40', 'border-t-red-500/40'],
    ['hover:bg-blue-600', 'bg-blue-600'],
    ['ring-offset-sky-50', 'ring-offset-sky-50'],
    ['bg-[#0e1626]', 'bg-[#0e1626]'],
    ["color: '#fff'", '#fff'],
    ['fill="#3B82F6"', '#3B82F6'],
    ['background: #0e162680;', '#0e162680'],
    ["stroke: 'rgb(59 130 246 / 0.4)'", 'rgb(59 130 246 / 0.4)'],
    ['rgba(0,0,0,.5)', 'rgba(0,0,0,.5)'],
    ['hsl(220 50% 10%)', 'hsl(220 50% 10%)'],
  ])('%s içinde ihlali yakalar', (text, match) => {
    expect(scan('x.tsx', text).map((v) => v.match)).toContain(match)
  })

  it.each([
    'bg-card text-fg border-line-strong',
    'text-cyan bg-cyan/12 border-purple/25 text-purple',
    'bg-accent/12 text-accent-fg hover:bg-danger/25',
    'size-10 gap-2 rounded-xl h-1.5 w-5/6 grid-cols-2',
    'text-[11px] max-w-[1680px] z-[60]',
    'from-accent to-cyan',
    "href='#section' // issue #12",
    "document.querySelector('#root')",
    "getPropertyValue('--ms-bg')",
    'var(--ms-accent)',
    'items.length - 100',
  ])('%s içinde ihlal bulmaz', (text) => {
    expect(scan('x.tsx', text)).toEqual([])
  })
})

describe('tasarım belirteçleri dışında renk kullanılmaz', () => {
  it('kaynak dosyaları bulunur', () => {
    expect(files.length).toBeGreaterThan(50)
    expect(files.some((f) => f.file === 'components/ui/primitives.tsx')).toBe(true)
    expect(files.some((f) => f.file.startsWith('modules/'))).toBe(true)
  })

  it('çekirdek arayüzde (modüller dışında) ihlal yoktur', () => {
    expect(violations.filter((v) => !/^modules\/[^/]+\//.test(v.file)).map(describeViolation)).toEqual([])
  })

  it('özellik modüllerinde ihlal yoktur', () => {
    expect(violations.filter((v) => /^modules\/[^/]+\//.test(v.file)).map(describeViolation)).toEqual([])
  })

  it('izin listesindeki her kayıt hâlâ kullanılıyor', () => {
    for (const [file, match] of ALLOWED) {
      const text = readFileSync(join(SRC, file), 'utf8')
      expect(text.includes(match), `${file} içinde ${match} artık yok; izin listesinden çıkarın`).toBe(true)
    }
  })

  it('tarayıcı tema rengi --ms-bg belirteçleriyle aynıdır', () => {
    const css = readFileSync(join(SRC, 'styles/index.css'), 'utf8')
    const [dark, light] = [...css.matchAll(/--ms-bg:\s*(#[0-9a-f]{6})/gi)].map((m) => m[1])
    const store = readFileSync(join(SRC, 'stores/ui.ts'), 'utf8')
    expect(store).toContain(`'${dark}'`)
    expect(store).toContain(`'${light}'`)
  })
})
