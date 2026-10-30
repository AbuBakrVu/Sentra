import { useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago, plural, statusText } from '../format.js'

const order = { critical: 0, offline: 1, warning: 2, waiting: 3, ok: 4 }

export function useHosts(intervalMs = 15000) {
  const [hosts, setHosts] = useState(null)
  const [error, setError] = useState(null)
  const [checkedAt, setCheckedAt] = useState(null)
  useEffect(() => {
    let alive = true
    const load = () => api.hosts()
      .then((h) => { if (alive) { setHosts(h); setError(null); setCheckedAt(new Date()) } })
      .catch((e) => alive && setError(e.message))
    load()
    const t = setInterval(load, intervalMs)
    return () => { alive = false; clearInterval(t) }
  }, [intervalMs])
  return { hosts, error, checkedAt }
}

function headline(hosts) {
  const bad = hosts.filter((h) => h.status === 'critical' || h.status === 'offline').length
  const warn = hosts.filter((h) => h.status === 'warning').length
  if (bad) return { tone: 'critical', text: `${bad} ${plural(bad, 'авария', 'аварии', 'аварий')} ${bad === 1 ? 'требует' : 'требуют'} внимания` }
  if (warn) return { tone: 'warning', text: `Всё работает, но ${warn} ${plural(warn, 'устройство', 'устройства', 'устройств')} на пределе` }
  return { tone: 'ok', text: 'Всё в порядке' }
}

function Meter({ label, value, title }) {
  const v = value ?? 0
  const tone = v > 95 ? 'critical' : v > 85 ? 'warning' : 'ok'
  return (
    <div className="meter" title={title}>
      <span className="meter-label">{label}</span>
      <span className="meter-track"><span className={`meter-fill ${tone}`} style={{ width: `${Math.min(100, v)}%` }} /></span>
      <span className="meter-value">{value == null ? '—' : `${Math.round(v)}%`}</span>
    </div>
  )
}

function Tile({ h }) {
  const m = h.last_metrics || {}
  const ip = h.inventory.ips?.[0]
  return (
    <Link to={`/hosts/${h.id}`} className={`tile s-${h.status}`}>
      <div className="tile-head">
        <span className="tile-name">{h.name}</span>
        <span className={`badge s-${h.status}`}>{statusText[h.status]}</span>
      </div>
      <div className="tile-sub">{[h.inventory.os_name, ip].filter(Boolean).join(', ')}</div>
      {h.status === 'waiting' || h.status === 'offline' ? (
        <p className="tile-note">{h.status === 'offline' ? `Последние данные ${ago(h.last_seen)}` : 'Агент зарегистрирован, ждём первые метрики'}</p>
      ) : (
        <div className="meters">
          <Meter label="CPU" value={m.cpu_usage_percent} />
          <Meter label="Память" value={m.mem_used_percent} />
          <Meter label="Диск" value={m.disk_max_percent} title={m.disk_max_mount ? `Самый заполненный раздел: ${m.disk_max_mount}` : undefined} />
        </div>
      )}
      {h.issues.length > 0 && h.status !== 'offline' && <p className="tile-issues">{h.issues.join(', ')}</p>}
    </Link>
  )
}

export default function Overview() {
  const { hosts, error, checkedAt } = useHosts()
  const [filter, setFilter] = useState('all')

  const sorted = useMemo(() => (hosts || []).slice().sort((a, b) =>
    order[a.status] - order[b.status] || a.name.localeCompare(b.name, 'ru')), [hosts])

  if (error && !hosts) return <p className="banner critical">{error}</p>
  if (!hosts) return <p className="muted">Загружаем список устройств…</p>

  if (hosts.length === 0) {
    return (
      <div className="empty">
        <h1 className="headline">Пока нечего охранять</h1>
        <p className="lead">Подключите первый сервер: панель выдаст одну команду, её нужно выполнить на сервере. Через минуту здесь появятся его графики.</p>
        <Link to="/add" className="btn primary">Добавить сервер</Link>
      </div>
    )
  }

  const hl = headline(hosts)
  const problems = sorted.filter((h) => h.status !== 'ok' && h.status !== 'waiting')
  const shown = filter === 'problems' ? problems : sorted
  const online = hosts.filter((h) => h.online).length

  return (
    <>
      <div className={`summary tone-${hl.tone}`}>
        <h1 className="headline">{hl.text}</h1>
        <p className="lead">
          На связи {online} из {hosts.length} {plural(hosts.length, 'устройства', 'устройств', 'устройств')}.
          {checkedAt && ` Обновлено в ${checkedAt.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}.`}
          {error && <span className="inline-error"> Не удалось обновить: {error}</span>}
        </p>
      </div>
      <div className="toolbar" role="group" aria-label="Фильтр">
        <button className={filter === 'all' ? 'seg active' : 'seg'} onClick={() => setFilter('all')}>Все ({hosts.length})</button>
        <button className={filter === 'problems' ? 'seg active' : 'seg'} onClick={() => setFilter('problems')}>С проблемами ({problems.length})</button>
      </div>
      {shown.length === 0 ? <p className="muted">Проблем нет.</p> : (
        <div className="grid">{shown.map((h) => <Tile key={h.id} h={h} />)}</div>
      )}
    </>
  )
}
