import { cyan, sand, slate } from '@radix-ui/colors'
import { encode } from 'uqr'
import type { RepoDetail } from '../api/types'
import { githubLink, number } from './format'

const font = '"DM Sans Variable", "Microsoft YaHei", "PingFang SC", sans-serif'

function writeLines(ctx: CanvasRenderingContext2D, value: string, x: number, y: number, width: number, limit: number, lineHeight: number) {
  const lines: string[] = []
  let current = ''
  const segments = new Intl.Segmenter('zh', { granularity: 'word' }).segment(value.trim().replace(/\s+/g, ' '))
  for (const { segment } of segments) {
    const parts = ctx.measureText(segment).width <= width ? [segment] : Array.from(segment)
    for (const part of parts) {
      if (ctx.measureText(current + part).width > width && current) {
        lines.push(current.trimEnd())
        current = ''
      }
      current += current ? part : part.trimStart()
      if (lines.length === limit) break
    }
    if (lines.length === limit) break
  }
  if (lines.length < limit) {
    if (current) lines.push(current)
  } else {
    const last = Array.from(lines[limit - 1])
    while (last.length && ctx.measureText(last.join('') + '…').width > width) last.pop()
    lines[limit - 1] = last.join('').trimEnd() + '…'
  }
  lines.forEach((line, index) => ctx.fillText(line, x, y + index * lineHeight))
  return lines.length
}

export async function createRepoPoster(repo: RepoDetail, link: string): Promise<Blob> {
  await document.fonts.ready
  const canvas = document.createElement('canvas')
  canvas.width = 900
  canvas.height = 1120
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('无法绘制分享海报')

  ctx.fillStyle = sand.sand2
  ctx.fillRect(0, 0, canvas.width, canvas.height)
  ctx.fillStyle = sand.sand1
  ctx.strokeStyle = sand.sand7
  ctx.beginPath()
  ctx.roundRect(38, 38, 824, 1044, 26)
  ctx.fill()
  ctx.stroke()

  ctx.fillStyle = cyan.cyan3
  ctx.beginPath()
  ctx.roundRect(39, 39, 822, 324, [25, 25, 0, 0])
  ctx.fill()
  ctx.strokeStyle = cyan.cyan8
  for (const radius of [28, 51, 75, 99]) {
    ctx.beginPath()
    ctx.arc(722, 151, radius, 0, Math.PI * 2)
    ctx.stroke()
  }
  ctx.fillStyle = cyan.cyan5
  ctx.beginPath()
  ctx.moveTo(722, 151)
  ctx.arc(722, 151, 99, -1.34, -.44)
  ctx.closePath()
  ctx.fill()

  ctx.textBaseline = 'top'
  ctx.fillStyle = cyan.cyan12
  ctx.font = '600 24px ' + font
  ctx.fillText('GitHub 雷达', 76, 79)
  ctx.fillStyle = slate.slate12
  ctx.font = '720 48px ' + font
  const heading = repo.titleZh || repo.fullName
  const titleLines = writeLines(ctx, heading, 76, 144, 552, 3, 61)
  if (repo.titleZh && repo.titleZh !== repo.fullName) {
    ctx.fillStyle = slate.slate11
    ctx.font = '500 22px ' + font
    writeLines(ctx, repo.fullName, 76, 150 + titleLines * 61, 716, 1, 28)
  }

  ctx.fillStyle = slate.slate12
  ctx.font = '700 40px ' + font
  ctx.fillText(number(repo.stars), 76, 394)
  ctx.fillText(number(repo.forks), 372, 394)
  ctx.fillStyle = slate.slate11
  ctx.font = '500 23px ' + font
  ctx.fillText('Star', 76, 448)
  ctx.fillText('Fork', 372, 448)
  if (repo.language) writeLines(ctx, repo.language, 638, 415, 168, 1, 28)

  ctx.strokeStyle = sand.sand7
  for (const y of [500, 885]) {
    ctx.beginPath()
    ctx.moveTo(76, y)
    ctx.lineTo(824, y)
    ctx.stroke()
  }
  ctx.fillStyle = slate.slate12
  ctx.font = '660 26px ' + font
  ctx.fillText(repo.summaryZh ? 'AI 导读' : '项目简介', 76, 535)
  ctx.fillStyle = slate.slate11
  ctx.font = '450 30px ' + font
  writeLines(ctx, repo.summaryZh || repo.description || '暂无项目简介', 76, 588, 748, 6, 44)

  const host = new URL(link).hostname
  const local = ['127.0.0.1', 'localhost', '[::1]'].includes(host)
  const destination = (local && githubLink(repo.htmlUrl)) || link
  const { data, size } = encode(destination, { ecc: 'M', border: 4 })
  const cell = Math.floor(140 / size)
  const side = size * cell
  const left = 824 - side
  const top = 916
  ctx.fillStyle = sand.sand1
  ctx.fillRect(left, top, side, side)
  ctx.fillStyle = slate.slate12
  data.forEach((row, y) => row.forEach((on, x) => {
    if (on) ctx.fillRect(left + x * cell, top + y * cell, cell, cell)
  }))
  ctx.font = '650 25px ' + font
  ctx.fillText(destination === link ? '扫码查看项目' : '扫码打开 GitHub 仓库', 76, 952)
  ctx.fillStyle = slate.slate11
  ctx.font = '500 22px ' + font
  writeLines(ctx, new URL(destination).hostname, 76, 991, 520, 1, 28)

  return new Promise((resolve, reject) => canvas.toBlob((blob) =>
    blob ? resolve(blob) : reject(new Error('无法生成 PNG 海报')), 'image/png'))
}
