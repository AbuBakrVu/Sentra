import { useEffect, useState } from 'react'
import { api } from '../api.js'
import Fresh from '../Fresh.jsx'
import { avText, pctText } from '../Availability.jsx'
import { plural } from '../format.js'
import { openProblems, verdict } from './Overview.jsx'
import { inetStatusText } from './Internet.jsx'
import { duration } from './Problems.jsx'

const mins = (n) => (n < 60 ? `${n} мин` : `${Math.floor(n / 60)} ч ${n % 60} мин`)

// Сводка для директора: без процессоров и графиков, только «работает или нет».
export default function Brief() {
  const [d, setD] = useState(null)
  const [error, setError] = useState(null)

  useEffect(() => {
    let alive = true
    const load = () => Promise.all([api.hosts(), api.problems('all', 30), api.inetOutages(30)])
      .then(([hosts, problems, inet]) => alive && (setD({ hosts, problems, inet, at: Date.now() }), setError(null)))
      .catch((e) => alive && setError(e.message))
    load()
    const t = setInterval(load, 60000)
    return () => { alive = false; clearInterval(t) }
  }, [])

  if (error && !d) return <p className="banner critical">{error}</p>
  if (!d) return <p className="muted">Загружаем…</p>

  const inet = d.hosts.find((h) => h.kind === 'inet' && h.status !== 'stopped')
  const devices = d.hosts.filter((h) => h.kind !== 'inet' && h.status !== 'stopped')
  const working = devices.filter((h) => h.online)
  const broken = devices.filter((h) => !h.online)
  const troubled = working.filter((h) => h.problems.length > 0)
  const open = openProblems(d.hosts)
  const v = verdict(open)
  const crit = d.problems.filter((p) => p.max_severity === 2).length
  const rows = devices.map((h) => ({ h, down: (h.availability?.days || []).reduce((s, x) => s + x.down_min, 0) }))
    .filter((r) => r.down > 0).sort((a, b) => b.down - a.down).slice(0, 8)
  const today = new Date().toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' })

  return (
    <div className="brief">
      <div className={`page-head verdict v-${v.tone}`}>
        <div>
          <p className="brief-date">Состояние ИТ на {today}</p>
          <h1 className="headline">{v.title}</h1>
          {v.text && <p className="lead">{v.text}</p>}
        </div>
        <div className="head-side no-print">
          <Fresh at={d.at} />
          <button type="button" className="btn" onClick={() => window.print()}>Распечатать</button>
        </div>
      </div>

      <div className="brief-cards">
        <section className="card">
          <h2>Серверы и машины</h2>
          <p className="brief-big">{working.length} <small>из {devices.length} на связи</small></p>
          <p className="muted">{[broken.length ? `Нет связи: ${broken.map((h) => h.name).join(', ')}.` : devices.length ? 'Все на связи.' : 'Устройства ещё не подключены.', troubled.length ? `Работают с замечаниями: ${troubled.map((h) => h.name).join(', ')}.` : ''].filter(Boolean).join(' ')}</p>
        </section>
        <section className="card">
          <h2>Интернет</h2>
          <p className="brief-big">{inet ? inetStatusText[inet.status] : 'Не проверяется'}</p>
          <p className="muted">{!inet ? 'Проверка канала выключена.' : d.inet.outages.length
            ? `За 30 дней ${d.inet.outages.length} ${plural(d.inet.outages.length, 'перерыв', 'перерыва', 'перерывов')}, всего без связи ${duration(0, d.inet.down_seconds)}.`
            : 'За 30 дней интернет не пропадал.'}</p>
        </section>
        <section className="card">
          <h2>Проблемы за 30 дней</h2>
          <p className="brief-big">{d.problems.length} <small>{plural(d.problems.length, 'проблема', 'проблемы', 'проблем')}</small></p>
          <p className="muted">{d.problems.length ? `Из них аварий: ${crit}. Сейчас не решено: ${open.length}.` : 'Сбоев не было.'}</p>
        </section>
      </div>

      {open.length > 0 && (
        <section className="brief-sec">
          <h2>Что не в порядке сейчас</h2>
          <ul className="brief-list">
            {open.map((p) => (
              <li key={p.id}><b>{p.host.name}</b>: {p.title.charAt(0).toLowerCase() + p.title.slice(1)}. Длится {duration(p.opened_at)}{p.ack_by ? `, этим занимается ${p.ack_by}` : ', пока никто не взял в работу'}.</li>
            ))}
          </ul>
        </section>
      )}

      <section className="brief-sec">
        <h2>Простои за 30 дней</h2>
        {rows.length === 0 ? <p className="muted">Ни одно устройство не было недоступно.</p> : (
          <div className="tbl-box">
            <table>
              <thead><tr><th>Устройство</th><th className="num">Было недоступно</th><th className="num">Доступность</th></tr></thead>
              <tbody>
                {rows.map(({ h, down }) => (
                  <tr key={h.id}><td><b>{h.name}</b></td><td className="num" data-label="Было недоступно">{mins(down)}</td><td className="num" data-label="Доступность">{avText(h.availability)}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {d.inet.outages.length > 0 && !d.inet.young && <p className="muted small">Доступность интернета за 30 дней: {pctText(d.inet.pct)}.</p>}
      </section>
    </div>
  )
}
