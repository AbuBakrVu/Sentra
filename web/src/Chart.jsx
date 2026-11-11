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

const tint = { warning: '168, 111, 0', critical: '194, 57, 47' }

export default function Chart({ hostId, metric, range, title, refreshKey, marks }) {
  const box = useRef(null)
  const plot = useRef(null)
  const mk = useRef(marks)
  mk.current = marks
  useEffect(() => { plot.current?.redraw(false) }, [marks])
  const [state, setState] = useState({ loading: true, error: null, empty: false, t0: 0, t1: 0 })

  useEffect(() => {
    let alive = true
    api.chart(hostId, metric, range).then((d) => {
      if (!alive) return
      plot.current?.destroy()
      plot.current = null
      const hasData = d.series.some((s) => s.values.some((v) => v != null))
      setState({ loading: false, error: null, empty: !hasData, t0: d.timestamps[0], t1: d.timestamps[d.timestamps.length - 1] })
      if (!hasData || !box.current) return
      const unit = d.unit
      const opts = {
        width: box.current.clientWidth,
        height: 200,
        cursor: { points: { size: 6 } },
        legend: { live: true },
        hooks: {
          // заливка: когда по этому показателю была проблема
          drawClear: [(u) => {
            const spans = mk.current?.spans
            if (!spans?.length) return
            const { ctx, bbox } = u
            ctx.save()
            for (const s of spans) {
              const x0 = Math.max(bbox.left, u.valToPos(s.a, 'x', true))
              const x1 = Math.min(bbox.left + bbox.width, u.valToPos(s.b, 'x', true))
              if (x1 < bbox.left || x0 > bbox.left + bbox.width) continue
              ctx.fillStyle = `rgba(${tint[s.tone]}, 0.12)`
              ctx.fillRect(x0, bbox.top, Math.max(2 * devicePixelRatio, x1 - x0), bbox.height)
            }
            ctx.restore()
          }],
          // пунктир: пороги, с которых начинаются предупреждение и авария
          draw: [(u) => {
            const lines = mk.current?.lines
            if (!lines?.length) return
            const { ctx, bbox } = u
            ctx.save()
            ctx.lineWidth = devicePixelRatio
            ctx.setLineDash([5 * devicePixelRatio, 4 * devicePixelRatio])
            for (const l of lines) {
              const y = Math.round(u.valToPos(l.v, 'y', true))
              if (!(y > bbox.top && y < bbox.top + bbox.height)) continue
              ctx.strokeStyle = `rgba(${tint[l.tone]}, 0.75)`
              ctx.beginPath(); ctx.moveTo(bbox.left, y); ctx.lineTo(bbox.left + bbox.width, y); ctx.stroke()
            }
            ctx.restore()
          }],
        },
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
    }).catch((e) => alive && setState({ loading: false, error: e.message, empty: false, t0: 0, t1: 0 }))
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

  // заливка попала в выбранный период?
  const shaded = !!marks?.spans.some((s) => s.b >= state.t0 && s.a <= state.t1)

  return (
    <section className="chart">
      <h3>{title}</h3>
      {state.error && <p className="chart-note error">{state.error}</p>}
      {state.empty && <p className="chart-note">За выбранный период данных нет. Первые точки появятся через минуту после подключения.</p>}
      <div ref={box} className="chart-canvas" />
      {marks && !state.empty && !state.error && (marks.lines.length > 0 || shaded) && (
        <p className="chart-key">{marks.lines.length > 0 && <span><i className="k-line" />пороги предупреждения и аварии</span>}{shaded && <span><i className="k-span" />время, когда была проблема</span>}</p>
      )}
    </section>
  )
}
