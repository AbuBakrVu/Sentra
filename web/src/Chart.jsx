import { useEffect, useRef, useState } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { api } from './api.js'
import { bitrate, pct } from './format.js'

// Цвета рядов: различимы между собой и не совпадают со статусными.
const palette = ['#2E7A50', '#3F6FB8', '#C2772A', '#8A5BB8', '#1E9AA0', '#B8476A', '#6A6D67', '#7C8B2E']

function fmt(unit, v) {
  if (v == null) return '—'
  if (unit === '%') return pct(v)
  if (unit === 'B/s') return bitrate(v)
  if (unit === 'ms') return `${v.toLocaleString('ru-RU', { maximumFractionDigits: v < 10 ? 1 : 0 })} мс`
  return v.toLocaleString('ru-RU', { maximumFractionDigits: 2 })
}

export default function Chart({ hostId, metric, range, title, refreshKey }) {
  const box = useRef(null)
  const plot = useRef(null)
  const [state, setState] = useState({ loading: true, error: null, empty: false })

  useEffect(() => {
    let alive = true
    api.chart(hostId, metric, range).then((d) => {
      if (!alive) return
      plot.current?.destroy()
      plot.current = null
      const hasData = d.series.some((s) => s.values.some((v) => v != null))
      setState({ loading: false, error: null, empty: !hasData })
      if (!hasData || !box.current) return
      const unit = d.unit
      const opts = {
        width: box.current.clientWidth,
        height: 200,
        cursor: { points: { size: 6 } },
        legend: { live: true },
        scales: {
          x: { time: true },
          y: unit === '%' ? { range: (u, min, max) => [0, Math.max(100, max)] } : { range: (u, min, max) => [0, max > 0 ? max * 1.15 : 1] },
        },
        axes: [
          { stroke: '#6A6D67', grid: { stroke: '#E6E6E1' }, ticks: { stroke: '#E6E6E1' }, font: '12px Onest, system-ui',
            values: (u, splits) => {
              const span = u.scales.x.max - u.scales.x.min
              return splits.map((ts) => new Date(ts * 1000).toLocaleString('ru-RU', span > 2 * 86400
                ? { day: 'numeric', month: 'short' }
                : { hour: '2-digit', minute: '2-digit' }))
            } },
          { stroke: '#6A6D67', grid: { stroke: '#E6E6E1' }, ticks: { show: false }, size: unit === 'B/s' ? 84 : unit === 'ms' ? 64 : 52,
            font: '12px Onest, system-ui', values: (u, vals) => vals.map((v) => fmt(unit, v)) },
        ],
        series: [
          { label: 'Время', value: (u, ts) => (ts == null ? '' : new Date(ts * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })) },
          ...d.series.map((s, i) => ({
            label: s.label,
            stroke: palette[i % palette.length],
            width: 1.75,
            fill: d.series.length === 1 ? palette[0] + '14' : undefined,
            spanGaps: false,
            value: (u, v) => fmt(unit, v),
          })),
        ],
      }
      plot.current = new uPlot(opts, [d.timestamps, ...d.series.map((s) => s.values)], box.current)
    }).catch((e) => alive && setState({ loading: false, error: e.message, empty: false }))
    return () => { alive = false }
  }, [hostId, metric, range, refreshKey])

  useEffect(() => {
    if (!box.current) return
    const ro = new ResizeObserver(() => {
      if (plot.current && box.current) plot.current.setSize({ width: box.current.clientWidth, height: 200 })
    })
    ro.observe(box.current)
    return () => { ro.disconnect(); plot.current?.destroy(); plot.current = null }
  }, [])

  return (
    <section className="chart">
      <h3>{title}</h3>
      {state.error && <p className="chart-note error">{state.error}</p>}
      {state.empty && <p className="chart-note">За выбранный период данных нет. Первые точки появятся через минуту после подключения.</p>}
      <div ref={box} className="chart-canvas" />
    </section>
  )
}
