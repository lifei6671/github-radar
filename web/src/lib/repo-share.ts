import type { RepoDetail, RepoReadme } from '../api/types'
import { githubLink, repoPath } from './format'

export function repoShareUrl(repo: RepoDetail) {
  return new URL(repoPath(repo.fullName), window.location.origin).href
}

export function repoExportName(repo: RepoDetail, suffix: string) {
  return repo.fullName.replace('/', '-') + suffix
}

function text(value: string) {
  return value.replace(/[\\`*_{}\[\]<>#|~]/g, '\\$&')
}

export async function repoMarkdown(repo: RepoDetail, url: string, readme?: RepoReadme) {
  const source = githubLink(repo.htmlUrl)
  const readmeLink = githubLink(readme?.sourceUrl) ?? githubLink(repo.sourceReadmeUrl) ?? (source && new URL('#readme', source).href)
  const license = repo.license?.spdxId === 'NOASSERTION' ? undefined : repo.license?.name ?? repo.license?.spdxId
  const description = repo.summaryZh || repo.description
  const details = [
    source && `- [GitHub 原文](<${source}>)`,
    `- [雷达详情](<${url}>)`,
    readmeLink && `- [README 来源](<${readmeLink}>)`,
    `- **Star** ${repo.stars}`,
    `- **Fork** ${repo.forks}`,
    repo.language && `- **语言** ${text(repo.language)}`,
    license && `- **许可证** ${text(license)}`,
  ].filter(Boolean).join('\n')
  const sections = [
    '# ' + text(repo.fullName),
    details,
    description && `## ${repo.summaryZh ? 'AI 导读' : '项目简介'}\n\n${text(description)}`,
    repo.recommendationReason && '## 推荐理由\n\n' + text(repo.recommendationReason),
    repo.features.length > 0 && '## 主要功能\n\n' + repo.features.map((feature) => '- ' + text(feature)).join('\n'),
    repo.valueTags.length > 0 && '## 标签\n\n' + repo.valueTags.map(text).join('、'),
  ].filter(Boolean)
  if (readme?.state === 'ready') {
    const html = readme.chineseHtml
    if (html) {
      const { default: Turndown } = await import('turndown')
      const body = new Turndown({ headingStyle: 'atx', codeBlockStyle: 'fenced' }).keep(['table', 'details']).turndown(html)
      const truncated = readme.truncated ? '> 站内 README 已截断，可到 GitHub 查看全文。\n\n' : ''
      if (body) sections.push('## README\n\n' + truncated + body)
    }
  }
  return sections.join('\n\n') + '\n'
}

export function downloadFile(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = name
  document.body.append(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
