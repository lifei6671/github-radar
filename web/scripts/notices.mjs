import { readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// 把运行时库及自托管字体的许可和声明随构建产物分发。
const modules = [
  'react', 'react-dom', 'react-router', 'scheduler',
  '@tanstack/react-query', '@tanstack/query-core',
  '@dnd-kit/accessibility', '@dnd-kit/core', '@dnd-kit/sortable', '@dnd-kit/utilities',
  '@radix-ui/colors', 'lucide-react', 'echarts', 'zrender', 'tslib', 'tailwindcss',
  '@fontsource-variable/dm-sans', '@fontsource/ibm-plex-mono',
]
const lines = ['GitHub 雷达前端第三方许可', '']
for (const name of modules) {
  const root = join(import.meta.dirname, '..', 'node_modules', name)
  const { version } = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))
  lines.push('='.repeat(72), name + ' ' + version)
  const documents = readdirSync(root).filter((file) => /^(licen[cs]e|notice|ofl|copying)(\.|$)/i.test(file)).sort()
  if (documents.length === 0) throw new Error(name + ' 缺少可分发的许可文件')
  for (const document of documents) lines.push('', document, readFileSync(join(root, document), 'utf8').trim())
  lines.push('')
}
writeFileSync(join(import.meta.dirname, '..', 'dist', 'third-party-licenses.txt'), lines.join('\n') + '\n')
