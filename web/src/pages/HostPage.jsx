import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link, navigate } from '../App.jsx'
import Chart from '../Chart.jsx'
import { ago, bitrate, bytes, duration, statusText } from '../format.js'

const rangeList = [['1h', '1 час'], ['6h', '6 часов'], ['24h', 'Сутки'], ['7d', 'Неделя'], ['30d', 'Месяц']]

export default function HostPage({ id }) {
  const [h, setH] = useState(null)
  const [error, setError] = useState(null)
  const [range, setRange] = useState(() => sessionStorage.getItem('sentra.range') || '1h')
  const [tick, setTick] = useState(0)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')

  useEffect(() => {
    let alive = true
    const load = () => api.host(id).then((x) => alive && (setH(x), setError(null))).catch((e) => alive && setError(e.message))
    load()
    const t = setInterval(() => { load(); setTick((n) => n + 1) }, 30000)
    return () => { alive = false; clearInterval(t) }
  }, [id])

  const pickRange = (r) => { setRange(r); try { sessionStorage.setItem('sentra.range', r) } catch { /* приватный режим */ } }

  async function saveName(e) {
    e.preventDefault()
    try {
      await api.rename(id, name)
      setH({ ...h, name: name.trim() })
      setEditing(false)
    } catch (err) { alert(err.message) }
  }

  async function remove() {
    if (!confirm(`Удалить «${h.name}» из мониторинга? История графиков сохранится в хранилище, но устройство пропадёт из панели. Агент на сервере нужно будет остановить отдельно.`)) return
    try { await api.remove(id); navigate('/') } catch (err) { alert(err.message) }
  }

  if (error && !h) return <><Link to="/" className="back">Все устройства</Link><p className="banner critical">{error}</p></>
  if (!h) return <p className="muted">Загружаем…</p>

  const inv = h.inventory, m = h.last_metrics || {}
  const facts = [
    ['Система', inv.os_name || inv.os],
    ['Ядро', inv.kernel],
    ['Процессор', inv.cpu_cores ? `${inv.cpu_cores} ${inv.cpu_cores === 1 ? 'ядро' : inv.cpu_cores < 5 ? 'ядра' : 'ядер'}${inv.cpu_model ? `, ${inv.cpu_model}` : ''}` : null],
    ['Память', inv.mem_total ? bytes(inv.mem_total) : null],
    ['IP-адреса', inv.ips?.join(', ')],
    ['Работает без перезагрузки', m.uptime_seconds != null ? duration(m.uptime_seconds) : null],
    ['Сеть сейчас', m.net_rx != null ? `приём ${bitrate(m.net_rx)}, передача ${bitrate(m.net_tx)}` : null],
    ['Агент', inv.agent_version ? `версия ${inv.agent_version}, данные ${ago(h.last_seen)}` : null],
  ].filter(([, v]) => v)

  return (
    <>
      <Link to="/" className="back">Все устройства</Link>
      <div className={`host-head tone-${h.status}`}>
        {editing ? (
          <form className="rename" onSubmit={saveName}>
            <label className="sr-only" htmlFor="hn">Название</label>
            <input id="hn" value={name} onChange={(e) => setName(e.target.value)} autoFocus maxLength={100} />
            <button className="btn primary" type="submit">Сохранить</button>
            <button className="btn" type="button" onClick={() => setEditing(false)}>Отмена</button>
          </form>
        ) : (
          <h1 className="headline host-title">
            {h.name}
            <button className="link-btn small" onClick={() => { setName(h.name); setEditing(true) }}>Переименовать</button>
          </h1>
        )}
        <p className="lead">
          <span className={`badge s-${h.status}`}>{statusText[h.status]}</span>
          {h.issues.length > 0 && <span> {h.issues.join(', ')}</span>}
        </p>
      </div>

      <dl className="facts">
        {facts.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
      </dl>

      <div className="toolbar" role="group" aria-label="Период графиков">
        {rangeList.map(([r, label]) => (
          <button key={r} className={range === r ? 'seg active' : 'seg'} onClick={() => pickRange(r)}>{label}</button>
        ))}
      </div>

      <div className="charts">
        <Chart hostId={id} metric="cpu" range={range} refreshKey={tick} title="Процессор" />
        <Chart hostId={id} metric="mem" range={range} refreshKey={tick} title="Память" />
        <Chart hostId={id} metric="disk" range={range} refreshKey={tick} title="Заполнение дисков" />
        <Chart hostId={id} metric="net" range={range} refreshKey={tick} title="Сеть" />
        <Chart hostId={id} metric="load" range={range} refreshKey={tick} title="Средняя нагрузка (load average)" />
      </div>

      <div className="danger">
        <button className="btn danger-btn" onClick={remove}>Удалить из мониторинга</button>
      </div>
    </>
  )
}
