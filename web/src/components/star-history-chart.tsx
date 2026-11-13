import { useEffect, useRef } from 'react'
import { BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { init, use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import type { StarHistoryDay } from '../api/types'
import { number } from '../lib/format'

use([BarChart, GridComponent, TooltipComponent, CanvasRenderer])

// 日计数用独立的柱状图，保留缺测空位，并以浅色标出未结束的一天。
export default function StarHistoryChart({ days }: { days: StarHistoryDay[] }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!ref.current) return
    const chart = init(ref.current)
    function render() {
      const css = getComputedStyle(document.documentElement)
      const token = (name: string) => css.getPropertyValue(name).trim()
      chart.setOption({
        animation: false,
        grid: { left: 12, right: 14, top: 14, bottom: 10, containLabel: true },
        xAxis: { type: 'category', data: days.map(({ date }) => date), axisTick: { show: false },
          axisLine: { lineStyle: { color: token('--line'), width: 0.5 } },
          axisLabel: { color: token('--ink-muted'), hideOverlap: true, formatter: (date: string) => date.slice(5) } },
        yAxis: { type: 'value', minInterval: 1, splitNumber: 4, axisLabel: { color: token('--ink-muted') },
          splitLine: { lineStyle: { color: token('--line'), width: 0.5 } } },
        tooltip: { trigger: 'axis', renderMode: 'richText', confine: true,
          backgroundColor: token('--chart-surface'), borderColor: token('--control-line'), borderWidth: 0.5,
          textStyle: { color: token('--ink'), fontSize: 12 },
          formatter: (params: unknown) => {
            const selected = (Array.isArray(params) ? params[0] : params) as { dataIndex?: number } | undefined
            const day = selected?.dataIndex == null ? undefined : days[selected.dataIndex]
            if (!day) return ''
            const label = day.state === 'missing' ? '缺测' : number(day.count) + ' Star' + (day.state === 'partial' ? '（当天未结束）' : '')
            return day.date + '\n' + label
          } },
        series: [{ type: 'bar', barMaxWidth: 16, barMinHeight: 1, data: days.map((day) => ({
          value: day.count, itemStyle: { color: token('--chart-accent'), opacity: day.state === 'partial' ? 0.4 : 1 },
        })) }],
      })
    }
    render()
    const resize = new ResizeObserver(() => chart.resize())
    resize.observe(ref.current)
    const theme = new MutationObserver(render)
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => { theme.disconnect(); resize.disconnect(); chart.dispose() }
  }, [days])
  return <div ref={ref} className="trend-chart" role="img" aria-label="GitHub 历史日 Star 柱状图" />
}
