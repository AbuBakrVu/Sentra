import { useEffect, useState } from 'react'
import { Link } from '../App.jsx'
import Fresh from '../Fresh.jsx'
import { plural, statusText } from '../format.js'
import { DotMap, groups, openProblems, useHosts, verdict } from './Overview.jsx'
import { inetStatusText, lossText, ms } from './Internet.jsx'
import { duration } from './Problems.jsx'

const order = { critical: 0, offline: 1, warning: 2, waiting: 3, ok: 4 }
const num = (v) => (v == null ? '—' : `${Math.round(v)}%`)

// Экран на стену: тёмный, крупный, без меню. Читается с нескольких метров.
export default function Wall() {
  const { hosts: all, error, checkedAt } = useHosts(15000)
  const [now, setNow] = useState(new Date())
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 10000)
    return () => clearInterval(t)
  }, [])

  if (!all) return <div className="wall"><p className="wall-note">{error || 'Загружаем…'}</p></div>

  const inet = all.find((h) => h.kind === 'inet' && h.status !== 'stopped')
  const devices = all.filter((h) => h.kind !== 'inet')
  const live = devices.filter((h) => h.status !== 'stopped')
    .sort((a, b) => order[a.status] - order[b.status] || a.name.localeCompare(b.name, 'ru'))
  const stopped = devices.length - live.length
  const open = openProblems(all)
  const v = verdict(open)
  // много устройств: вместо плиток карта по группам, крупно только проблемы
  const dense = live.length > 24
  const full = () => (document.fullscreenElement ? document.exitFullscreen() : document.documentElement.requestFullscreen?.())

  return (
    <div className={`wall w-${v.tone}`}>
      <header className="wall-head">
        <h1>{v.title}</h1>
        <div className="wall-side">
          <span className="wall-clock">{now.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })}</span>
          <button type="button" className="wall-btn" onClick={full}>Во весь экран</button>
          <Link to="/" className="wall-btn">Выйти</Link>
        </div>
      </header>

      {open.length > 0 && (
        <ul className="wall-probs">
          {open.slice(0, 6).map((p) => (
            <li key={p.id} className={p.severity === 2 ? 'crit' : 'warn'}>
              <b>{p.host.name}</b><span>{p.title}</span><i>{duration(p.opened_at)}{p.ack_by ? `, в работе у ${p.ack_by}` : ''}</i>
            </li>
          ))}
          {open.length > 6 && <li className="more">и ещё {open.length - 6}</li>}
        </ul>
      )}

      {dense && (
        <div className="wall-maps">
          {inet && (
            <div className={`wall-tile s-${inet.status}`}>
              <b>Интернет</b>
              <span className="wall-state">{inetStatusText[inet.status]}</span>
              <small>{inet.last_metrics?.inet_up === 1 ? `потери ${lossText(inet.last_metrics.loss5)}, задержка ${ms(inet.last_metrics.rtt5)}` : inet.issues[0] || ''}</small>
            </div>
          )}
          {groups(devices).map((g) => {
            const bad = g.items.filter((h) => ['critical', 'offline', 'warning'].includes(h.status)).length
            const on = g.items.filter((h) => h.status !== 'stopped').length
            return (
              <div key={g.key} className={`wall-map${bad ? ' has-bad' : ''}`}>
                <div className="wall-map-head"><b>{g.title.replace('Proxmox: ', '')}</b><span>{bad ? `${bad} с проблемой · ` : ''}работает {on - bad} из {g.items.length}</span></div>
                <DotMap items={g.items} big />
              </div>
            )
          })}
        </div>
      )}

      {!dense && <div className="wall-grid">
        {inet && (
          <div className={`wall-tile s-${inet.status}`}>
            <b>Интернет</b>
            <span className="wall-state">{inetStatusText[inet.status]}</span>
            <small>{inet.last_metrics?.inet_up === 1 ? `потери ${lossText(inet.last_metrics.loss5)}, задержка ${ms(inet.last_metrics.rtt5)}` : inet.issues[0] || ''}</small>
          </div>
        )}
        {live.map((h) => {
          const m = h.last_metrics || {}
          return (
            <div key={h.id} className={`wall-tile s-${h.status}`}>
              <b>{h.name}</b>
              <span className="wall-state">{h.issues[0] || statusText[h.status]}</span>
              <small>{h.online ? `CPU ${num(m.cpu_usage_percent)} · память ${num(m.mem_used_percent)}${m.disk_max_mount ? ` · диск ${num(m.disk_max_percent)}` : ''}` : ''}</small>
            </div>
          )
        })}
      </div>}

      <footer className="wall-foot">
        <span>Sentra</span>
        <span>{live.length} {plural(live.length, 'устройство', 'устройства', 'устройств')}{stopped ? `, ещё ${stopped} ${plural(stopped, 'выключено', 'выключены', 'выключено')}` : ''}</span>
        <Fresh at={checkedAt?.getTime()} stale={60} />
        {error && <span className="wall-err">Нет связи с Sentra: {error}</span>}
      </footer>
    </div>
  )
}
