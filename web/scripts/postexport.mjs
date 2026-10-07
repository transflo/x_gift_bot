// Post-process the Next.js static export:
//  1. Every inline <script> receives the CSP nonce placeholder that the Go
//     server replaces with a per-request nonce.
//  2. The export is copied into internal/site/web so go:embed picks it up.
import { cp, mkdir, readdir, readFile, rm, stat, writeFile } from "node:fs/promises"
import { existsSync } from "node:fs"
import path from "node:path"

const root = path.resolve(import.meta.dirname, "..")
const outDir = path.join(root, "out")
const target = path.resolve(root, "..", "internal", "site", "web")

async function walk(dir) {
  const entries = await readdir(dir)
  const files = []
  for (const entry of entries) {
    const full = path.join(dir, entry)
    const info = await stat(full)
    if (info.isDirectory()) files.push(...(await walk(full)))
    else files.push(full)
  }
  return files
}

if (!existsSync(outDir)) {
  console.error("out/ is missing; run `next build` first")
  process.exit(1)
}

let patched = 0
for (const file of await walk(outDir)) {
  if (!file.endsWith(".html")) continue
  const html = await readFile(file, "utf8")
  const replaced = html.replace(
    /<script(?![^>]*\bsrc=)(?![^>]*\bnonce=)([^>]*)>/g,
    (match, attrs) => {
      patched++
      return `<script${attrs} nonce="__XGIFT_NONCE__">`
    },
  )
  if (replaced !== html) await writeFile(file, replaced)
}

await rm(target, { recursive: true, force: true })
await mkdir(target, { recursive: true })
await cp(outDir, target, { recursive: true })
console.log(`Static export copied to ${path.relative(root, target)} (${patched} inline scripts nonced)`)
