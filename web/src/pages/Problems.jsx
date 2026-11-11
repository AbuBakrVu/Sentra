import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'

const sevText = { 1: 'Внимание', 2: 'Авария' }

export function duration(from, to) {
  const s = Math.max(0, (to ?? Math.floor(Date.now() / 1000)) - from)
  if (s < 60) return 'меньше минуты'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} мин`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h} ч ${m % 60} мин` : `${h} ч`
  return `${Math.floor(h / 24)} дн ${h % 24} ч`
}

export function when(unix) {
  const d = new Date(unix * 1000), now = new Date()
  const time = d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
  if (d.toDateString() === now.toDateString()) return `сегодня ${time}`
  const y = new Date(now); y.setDate(now.getDate() - 1)
  if (d.toDateString() === y.toDateString()) return `вчера ${time}`
  return d.toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}

// Кнопка «Взять в работу» с полем для комментария.
export function AckButton({ p, onDone }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState(null)
  if (p.ack_by) {
    return <span className="acked">В работе у {p.ack_by}{p.ack_comment ? `: ${p.ack_comment}` : ''}</span>
  }
  if (!open) return <button className="btn small-btn" type="button" onClick={() => setOpen(true)}>Взять в работу</button>
  async function submit(e) {
    e.preventDefault()
    setBusy(true); setErr(null)
    try { onDone(await api.ack(p.id, text)) } catch (x) { setErr(x.message); setBusy(false) }
  }
  return (
    <form className="ack-form" onSubmit={submit}>
      <label className="sr-only" htmlFor={`ack-${p.id}`}>Комментарий</label>
      <input id={`ack-${p.id}`} value={text} onChange={(e) => setText(e.target.value)} placeholder="Что делаете, необязательно" maxLength={500} autoFocus />
      <button className="btn primary small-btn" disabled={busy} type="submit">Взять</button>
      <button className="btn small-btn" type="button" onClick={() => setOpen(false)}>Отмена</button>
      {err && <span className="inline-error">{err}</span>}
    </form>
  )
}

export function ProblemRows({ list, showHost = true, onChange }) {
  return (
    <div className="tbl-box">
      <table className="prob-table">
        <thead>
          <tr><th>Уровень</th>{showHost && <th>Устройство</th>}<th>Что случилось</th><th>Началось</th><th>Длилось</th><th>Работа</th></tr>
        </thead>
        <tbody>
          {list.map((p) => {
            const sev = p.closed_at ? p.max_severity : p.severity
            return (
              <tr key={p.id} className={p.closed_at ? 'closed' : `open-${sev}`}>
                <td><span className={`badge s-${p.closed_at ? 'ok' : sev === 2 ? 'critical' : 'warning'}`}>{p.closed_at ? 'Решено' : sevText[sev]}</span>{p.closed_at && <span className="muted small"> было: {sevText[sev]?.toLowerCase()}</span>}</td>
                {showHost && <td><Link to={`/hosts/${p.host_id}`} className="nm">{p.host_name || p.host_id}</Link></td>}
                <td className="wrap">{p.title}</td>
                <td className="nowrap">{when(p.opened_at)}</td>
                <td className="nowrap">{duration(p.opened_at, p.closed_at)}{!p.closed_at && <span className="muted"> и идёт</span>}</td>
                <td>{p.closed_at
                  ? (p.ack_by ? <span className="muted">{p.ack_by}{p.ack_comment ? `: ${p.ack_comment}` : ''}</span> : <span className="muted">—</span>)
                  : <AckButton p={p} onDone={onChange} />}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

export default function Problems() {
  const [tab, setTab] = useState('open')
  const [list, setList] = useState(null)
  const [error, setError] = useState(null)
  const [tick, setTick] = useState(0)

  useEffect(() => {
    let alive = true
    const load = () => api.problems(tab === 'open' ? 'open' : 'all', 30)
      .then((l) => alive && (setList(l), setError(null))).catch((e) => alive && setError(e.message))
    load()
    const t = setInterval(load, 15000)
    return () => { alive = false; clearInterval(t) }
  }, [tab, tick])

  const crit = (list || []).filter((p) => !p.closed_at && p.severity === 2).length
  const warn = (list || []).filter((p) => !p.closed_at && p.severity === 1).length

  return (
    <>
      <h1 className="headline">Проблемы</h1>
      <p className="lead" style={{ marginBottom: 18 }}>
        {tab === 'open'
          ? (list ? (list.length ? `Сейчас ${crit} ${crit === 1 ? 'авария' : crit > 1 && crit < 5 ? 'аварии' : 'аварий'} и ${warn} ${warn === 1 ? 'предупреждение' : warn > 1 && warn < 5 ? 'предупреждения' : 'предупреждений'}.` : 'Сейчас всё в порядке.') : '')
          : 'Все проблемы за 30 дней, новые сверху. Журнал хранится 90 дней.'}
      </p>
      <div className="toolbar" role="group" aria-label="Что показать">
        <button className={tab === 'open' ? 'seg active' : 'seg'} onClick={() => { setTab('open'); setList(null) }}>Сейчас</button>
        <button className={tab === 'all' ? 'seg active' : 'seg'} onClick={() => { setTab('all'); setList(null) }}>За 30 дней</button>
      </div>
      {error && <p className="banner critical">{error}</p>}
      {!list ? <p className="muted">Загружаем…</p>
        : list.length === 0 ? <p className="muted">{tab === 'open' ? 'Открытых проблем нет.' : 'За 30 дней проблем не было.'}</p>
          : <ProblemRows list={list} onChange={() => setTick((n) => n + 1)} />}
    </>
  )
}
