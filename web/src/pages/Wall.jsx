import { useEffect, useState } from 'react'
import { Link } from '../App.jsx'
import Fresh from '../Fresh.jsx'
import { bitrate, plural } from '../format.js'
import { openProblems, useHosts, useWanReadings, verdict } from './Overview.jsx'
import { lossText, ms, wanTitle } from './Internet.jsx'
import { duration } from './Problems.jsx'

const shownProblems = 4
const timeFormat = new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' })
const dateFormat = new Intl.DateTimeFormat('ru-RU', { weekday: 'short', day: 'numeric', month: 'long' })
const deviceRows = [
  ['ok', 'В норме'],
  ['warning', 'Предупреждения'],
  ['offline', 'Нет связи'],
  ['critical', 'Аварии'],
  ['waiting', 'Ждём данные'],
]

function summaryTitle(counts, total, problems) {
  const { offline, critical, warning, ok } = counts
  if (offline) return `${offline} ${plural(offline, 'устройство', 'устройства', 'устройств')} без связи`
  if (problems.length) return verdict(problems).title
  if (critical) return `${critical} ${plural(critical, 'устройство', 'устройства', 'устройств')} в аварии`
  if (warning) return `${warning} ${plural(warning, 'устройство требует', 'устройства требуют', 'устройств требуют')} внимания`
  if (!total) return 'Нет устройств под наблюдением'
  return ok === total ? 'Все устройства в норме' : 'Ждём данные от устройств'
}

function WallChannels({ channels }) {
  const readings = useWanReadings(channels)
  return readings.map((channel) => (
    <div className="wall-channel" key={JSON.stringify([channel.host, channel.iface])}>
      <b>{wanTitle(channel)}</b>
      <div className="wall-rates">
        {channel.rx == null && channel.tx == null ? <span>Нет замера</span> : (
          <>
            <span><span className="sr-only">Приём </span><span aria-hidden="true">↓ </span>{bitrate(channel.rx)}</span>
            <span><span className="sr-only">Передача </span><span aria-hidden="true">↑ </span>{bitrate(channel.tx)}</span>
          </>
        )}
      </div>
    </div>
  ))
}

const internetTitles = {
  ok: 'Интернет работает',
  warning: 'Интернет с перебоями',
  critical: 'Интернет недоступен',
  offline: 'Интернет недоступен',
  waiting: 'Проверяем интернет',
  stopped: 'Проверка интернета выключена',
}

function WallInternet({ inet, onOpen }) {
  const m = inet?.last_metrics
  const status = inet?.status || 'stopped'
  const details = (status === 'ok' || status === 'warning') && m?.inet_up === 1
    ? `${ms(m.rtt5)} · потери ${lossText(m.loss5)}`
    : status !== 'stopped' && inet?.issues[0]

  return (
    <section className="wall-internet" aria-labelledby="wall-internet-heading">
      <div className="wall-inet-status">
        <h2 id="wall-internet-heading">
          <Link to="/internet" onClick={onOpen}><i className={`wall-dot s-${status}`} aria-hidden="true" />{inet ? internetTitles[status] || 'Интернет' : 'Интернет не настроен'}</Link>
        </h2>
        {details && <p>{details}</p>}
      </div>
      {inet?.wan_channels?.length > 0 && <WallChannels channels={inet.wan_channels} />}
    </section>
  )
}

export default function Wall() {
  const { hosts: all, error, checkedAt } = useHosts(15000)
  const [now, setNow] = useState(() => new Date())
  const [fullscreen, setFullscreen] = useState(() => Boolean(document.fullscreenElement))

  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 5000)
    const sync = () => setFullscreen(Boolean(document.fullscreenElement))
    document.addEventListener('fullscreenchange', sync)
    return () => {
      clearInterval(timer)
      document.removeEventListener('fullscreenchange', sync)
    }
  }, [])

  const toggleFullscreen = () => {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
    else document.documentElement.requestFullscreen?.().catch(() => {})
  }
  const leave = () => {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
  }

  const inet = all?.find((h) => h.kind === 'inet')
  const devices = all?.filter((h) => h.kind !== 'inet') || []
  const active = devices.filter((h) => h.status !== 'stopped')
  const stopped = devices.length - active.length
  const counts = { ok: 0, warning: 0, offline: 0, critical: 0, waiting: 0 }
  for (const device of active) counts[Object.hasOwn(counts, device.status) ? device.status : 'waiting']++
  const problems = openProblems(all?.filter((h) => h.status !== 'stopped') || [])
  const more = problems.length - shownProblems
  const title = error ? 'Нет связи с Sentra' : all ? summaryTitle(counts, active.length, problems) : 'Загружаем сводку…'

  return (
    <main className="wall">
      <header className="wall-head">
        <div className="wall-heading">
          <Link to="/" onClick={leave} className="brand wall-brand" aria-label="Sentra, обзор"><span aria-hidden="true" />Sentra</Link>
          <span>Экран на стену</span>
        </div>
        <div className="wall-side">
          <time dateTime={now.toISOString()} className="wall-clock">
            <span>{dateFormat.format(now)}</span><b>{timeFormat.format(now)}</b>
          </time>
          {document.fullscreenEnabled && <button type="button" className="wall-btn" onClick={toggleFullscreen}>
            {fullscreen ? 'Свернуть' : 'Во весь экран'}
          </button>}
          <Link to="/" className="wall-btn" onClick={leave}>Выйти</Link>
        </div>
      </header>

      <h1><span role="status">{title}</span></h1>
      {all && (
        <>
          <div className="wall-summary">
            <section className="wall-panel" aria-labelledby="wall-problem-heading">
              <h2 id="wall-problem-heading">Активные проблемы <span>{problems.length}</span></h2>
              {problems.length > 0 ? (
                <>
                  <ul className="wall-probs">
                    {problems.slice(0, shownProblems).map((p) => (
                      <li key={p.id} className={p.severity === 2 ? 'crit' : 'warn'}>
                        <Link className="wall-prob-host" onClick={leave} to={p.host.kind === 'inet' ? '/internet' : `/hosts/${p.host.id}`}>
                          <i className="wall-dot" aria-hidden="true" /><b>{p.host.name}</b>
                        </Link>
                        <span className="wall-prob-title">{p.title}</span>
                        <span className="wall-prob-age">{duration(p.opened_at)}{p.ack_by && <small>В работе у {p.ack_by}</small>}</span>
                      </li>
                    ))}
                  </ul>
                  {more > 0 && <Link className="wall-more" to="/problems" onClick={leave}>Ещё {more} в журнале проблем</Link>}
                </>
              ) : <p className="wall-empty">Нет зарегистрированных активных проблем.</p>}
            </section>

            <section className="wall-panel" aria-labelledby="wall-device-heading">
              <h2 id="wall-device-heading">Устройства</h2>
              <dl className="wall-counts">
                {deviceRows.map(([status, label]) => (counts[status] > 0 || status === 'ok' || status === 'warning' || status === 'offline') && (
                  <div key={status} className={`s-${status}`}>
                    <dt>{label}</dt><dd>{counts[status]}</dd>
                  </div>
                ))}
              </dl>
            </section>
          </div>

          <WallInternet inet={inet} onOpen={leave} />
        </>
      )}

      <footer className="wall-foot">
        {all && <span>{active.length} {plural(active.length, 'устройство', 'устройства', 'устройств')} под наблюдением
          {stopped > 0 && ` · выключено ${stopped}`}
        </span>}
        {error && <span className="wall-err" role="alert">{all && 'Показаны последние полученные данные · '}{error}</span>}
        <Fresh at={checkedAt?.getTime()} stale={60} />
      </footer>
    </main>
  )
}
