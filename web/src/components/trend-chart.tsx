import { useEffect, useRef } from 'react'
import { LineChart } from 'echarts/charts'
import { DataZoomComponent, GridComponent, TooltipComponent } from 'echarts/components'
import { init, use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import type { Trend } from '../api/types'
import { number, qualityLabel, signed, timestamp } from '../lib/format'

use([LineChart, DataZoomComponent, GridComponent, TooltipComponent, CanvasRenderer])

export default function TrendChart({ data }: { data: Trend }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!ref.current) return
    const element = ref.current
    const chart = init(element, undefined, { renderer: 'canvas' })
    function render() {
      const css = getComputedStyle(document.documentElement)
      const color = (key: string) => css.getPropertyValue(key).trim()
      chart.setOption({
        animation: false,
        textStyle: { color: color('--ink-muted'), fontFamily: 'DM Sans Variable, Microsoft YaHei, sans-serif', fontSize: 12 },
        grid: { left: 10, right: 20, top: 16, bottom: 16, containLabel: true },
        xAxis: {
          type: 'time',
          splitNumber: element.clientWidth < 500 ? 3 : 6,
          axisLine: { lineStyle: { color: color('--line'), width: 0.5 } },
          axisLabel: { color: color('--ink-muted'), hideOverlap: true },
        },
        yAxis: {
          type: 'value', scale: true, splitNumber: 4,
          axisLabel: { color: color('--ink-muted'), formatter: (value: number) => number(value) },
          splitLine: { lineStyle: { color: color('--line'), width: 0.5 } },
        },
        dataZoom: [{ type: 'inside', zoomOnMouseWheel: 'ctrl', moveOnMouseWheel: false }],
        tooltip: {
          trigger: 'axis', renderMode: 'richText', confine: true,
          backgroundColor: color('--surface'), borderColor: color('--control-line'), borderWidth: 0.5,
          textStyle: { color: color('--ink'), fontSize: 12 },
          axisPointer: { lineStyle: { color: color('--control-line') } },
          formatter: (params: unknown) => {
            const selected = (Array.isArray(params) ? params[0] : params) as { dataIndex?: number } | undefined
            const point = selected?.dataIndex == null ? undefined : data.points[selected.dataIndex]
            if (!point) return ''
            const lines = [timestamp(point.at), point.stars === null ? '缺少观测' : '累计 Star  ' + number(point.stars)]
            if (point.delta !== null) lines.push('净增  ' + signed(point.delta) + '  ' + qualityLabel[point.quality])
            if (point.startAt && point.endAt) lines.push(timestamp(point.startAt) + ' 至 ' + timestamp(point.endAt))
            else if (point.endAt && point.endAt !== point.at) lines.push('观测于 ' + timestamp(point.endAt))
            return lines.join('\n')
          },
        },
        series: [{
          type: 'line',
          data: data.points.map((point) => [Date.parse(point.at), point.stars]),
          connectNulls: false,
          showSymbol: true,
          // 长窗口的孤立观测没有相邻线段，仍需显示点位。
          symbolSize: (_value: unknown, { dataIndex }: { dataIndex: number }) => {
            if (data.points.length < 100) return 5
            return data.points[dataIndex - 1]?.stars == null && data.points[dataIndex + 1]?.stars == null ? 5 : 0
          },
          lineStyle: { color: color('--accent-text'), width: 2 },
          itemStyle: { color: color('--accent-text') },
        }],
      })
    }
    render()
    const resize = new ResizeObserver(() => chart.resize())
    resize.observe(element)
    const theme = new MutationObserver(render)
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => {
      resize.disconnect()
      theme.disconnect()
      chart.dispose()
    }
  }, [data])
  return <div ref={ref} className="trend-chart" role="img" aria-label="累计 Star 趋势，缺测点断开" />
}
