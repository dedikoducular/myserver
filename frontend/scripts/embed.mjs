// Copies the Vite build into the Go embed directory, keeping the files that
// are committed there.
import { cpSync, existsSync, readdirSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const src = join(root, 'dist')
const dest = join(root, '..', 'backend', 'internal', 'webui', 'dist')
const keep = new Set(['README.txt'])

if (!existsSync(join(src, 'index.html'))) {
  console.error('embed: frontend/dist/index.html bulunamadı; önce vite build çalıştırın')
  process.exit(1)
}
for (const name of readdirSync(dest)) {
  if (!keep.has(name)) rmSync(join(dest, name), { recursive: true, force: true })
}
cpSync(src, dest, { recursive: true })
console.log(`embed: ${src} -> ${dest}`)
