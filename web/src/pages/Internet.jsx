import { useEffect, useState } from 'react'
import { api } from '../api.js'
import Chart from '../Chart.jsx'
import AvailabilityBar, { pctText, pctTone } from '../Availability.jsx'
import { plural } from '../format.js'
import { duration, when } from './Problems.jsx'

const rangeList = [['1h', '1 час'], ['6h', '6 часов'], ['24h', 'Сутки'], ['7d', 'Неделя'], ['30d', 'Месяц']]
const dayList = [[7, '7 дней'], [30, '30 дней'], [90, '90 дней']]

export const inetStatusText = {
  ok: 'Работает',
  warning: 'Работает с перебоями',
  critical: 'Недоступен',
  offline: 'Недоступен',
  waiting: 'Проверяем',
  stopped: 'Проверка выключена',
}

const causeText = {
  provider: 'Канал провайдера: роутер отвечал, внешние адреса нет',
  unknown: 'Не определена: адрес роутера не был указан',
  local: 'Локальная сеть: не отвечал роутер',
}

export const ms = (v) => (v == null ? '—' : `${v < 10 ? v.toLocaleString('ru-RU', { maximumFractionDigits: 1 }) : Math.round(v)} мс`)
export const lossText = (v) => (v == null ? '—' : `${v.toLocaleString('ru-RU', { maximumFractionDigits: 1 })}%`)

function span(sec) {
  if (!sec) return '0 мин'
  return duration(0, sec)
}

export default function Internet() {
  const [d, setD] = useState(null)
  const [rep, setRep] = useState(null)
  const [error, setError] = useState(null)
  const [days, setDays] = useState(30)
  const [range, setRange] = useState(() => sessionStorage.getItem('sentra.range') || '1h')
  const [tick, setTick] = useState(0)

  useEffect(() => {
    let alive = true
    const load = () => {
      api.inet().then((x) => alive && (setD(x), setError(null))).catch((e) => alive && setError(e.message))
      api.inetOutages(days).then((x) => alive && setRep(x)).catch(() => {})
    }
    load()
    const t = setInterval(() => { load(); setTick((n) => n + 1) }, 30000)
    return () => { alive = false; clearInterval(t) }
  }, [days])

  const pickRange = (r) => { setRange(r); try { sessionStorage.setItem('sentra.range', r) } catch { /* приватный режим */ } }

  if (error && !d) return <p className="banner critical">{error}</p>
  if (!d) return <p className="muted">Загружаем…</p>

  const h = d.host
  const on = d.config.enabled && h && h.status !== 'stopped'
  const m = h?.last_metrics || {}
  const live = on && h.status !== 'waiting'
  const up = m.inet_up === 1
  const targets = m.targets || []

  return (
    <>
      <div className={`host-head tone-${on ? h.status : 'waiting'}`}>
        <h1 className="headline">Интернет</h1>
        <p className="lead">
          <span className={`badge s-${on ? h.status : 'stopped'}`}>{inetStatusText[on ? h.status : 'stopped']}</span>
          {on && h.issues.length > 0 && <span> {h.issues.join(', ')}</span>}
          {on && h.status === 'ok' && <span> Коробка каждые 30 секунд проверяет {d.config.router ? 'роутер и ' : ''}внешние адреса.</span>}
          {on && h.status === 'waiting' && <span> Первая проверка займёт до минуты.</span>}
          {!on && <span> Включите её в настройках ниже.</span>}
        </p>
      </div>

      {on && !d.config.router && (
        <p className="banner info">Укажите адрес роутера в настройках ниже. Тогда при сбое будет видно, виноват провайдер или сеть в офисе, и это попадёт в отчёт.</p>
      )}

      {on && (
        <>
          <div className="stats">
            <div className="stat">
              <h2>Сейчас</h2>
              <div className={`stat-val ${live && !up ? 'bad' : ''}`}>{!live ? '—' : up ? 'Работает' : 'Нет связи'}</div>
              <p>{d.method === 'tcp' ? 'проверка TCP-соединением: ping в этой системе недоступен' : 'проверка командой ping'}</p>
            </div>
            <div className="stat">
              <h2>Потери пакетов</h2>
              <div className={`stat-val ${m.loss5 > 15 ? 'bad' : m.loss5 > 3 ? 'lo' : ''}`}>{live && up ? lossText(m.loss5) : '—'}</div>
              <p>среднее за 5 минут, норма до 3%</p>
            </div>
            <div className="stat">
              <h2>Задержка</h2>
              <div className={`stat-val ${m.rtt5 > 400 ? 'bad' : m.rtt5 > 150 ? 'lo' : ''}`}>{live && up ? ms(m.rtt5) : '—'}</div>
              <p>среднее за 5 минут, норма до 150 мс</p>
            </div>
            <div className="stat">
              <h2>Перерывы за {days} {plural(days, 'день', 'дня', 'дней')}</h2>
              <div className={`stat-val ${rep?.outages.length ? 'lo' : ''}`}>{rep ? rep.outages.length : '—'}</div>
              <p>{rep?.outages.length ? `всего без интернета ${span(rep.down_seconds)}` : 'интернет не пропадал'}</p>
            </div>
          </div>

          {targets.length > 0 && (
            <section className="storages" aria-labelledby="tg-h">
              <h2 id="tg-h">Что проверяем</h2>
              <div className="tbl-box">
                <table>
                  <thead><tr><th>Адрес</th><th>Что это</th><th>Сейчас</th><th className="num">Потери</th><th className="num">Задержка</th></tr></thead>
                  <tbody>
                    {targets.map((t) => (
                      <tr key={t.name} className={t.up ? '' : 'row-critical'}>
                        <td><span className={`dot s-${t.up ? 'ok' : 'critical'}`} aria-hidden="true" /><b>{t.name}</b> <span className="muted">{t.addr}</span></td>
                        <td className="muted">{t.role === 'router' ? 'роутер в офисе' : 'адрес в интернете'}</td>
                        <td>{t.up ? 'отвечает' : 'не отвечает'}{t.up && t.via === 'tcp' && d.method !== 'tcp' && <span className="muted"> (на ping молчит, проверен по TCP)</span>}</td>
                        <td className="num">{t.up && t.via === 'icmp' ? lossText(t.loss) : '—'}</td>
                        <td className="num">{ms(t.rtt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          )}

          <section className="host-av" aria-labelledby="iav-h">
            <div className="host-av-head"><h2 id="iav-h">Доступность за 30 дней</h2></div>
            <AvailabilityBar av={h.availability} />
          </section>

          <div className="toolbar" role="group" aria-label="Период графиков">
            {rangeList.map(([r, label]) => (
              <button key={r} className={range === r ? 'seg active' : 'seg'} onClick={() => pickRange(r)}>{label}</button>
            ))}
          </div>
          <div className="charts">
            <Chart hostId="internet" metric="inet_rtt" range={range} refreshKey={tick} title="Задержка" />
            <Chart hostId="internet" metric="inet_loss" range={range} refreshKey={tick} title="Потери пакетов" />
          </div>
        </>
      )}

      {rep && (on || rep.outages.length > 0) && (
        <section className="inet-outages" aria-labelledby="out-h">
          <div className="dev-head">
            <h2 id="out-h">Журнал перерывов</h2>
            <div className="toolbar" role="group" aria-label="Период журнала">
              {dayList.map(([n, label]) => (
                <button key={n} className={days === n ? 'seg active' : 'seg'} onClick={() => { setRep(null); setDays(n) }}>{label}</button>
              ))}
            </div>
            <a className="btn" href={`/api/inet/report.csv?days=${days}`} download>Скачать отчёт для провайдера</a>
          </div>
          {rep.outages.length === 0 ? (
            <p className="muted">За этот период интернет не пропадал. Перерыв попадает в журнал, если две проверки подряд не получили ответа, то есть длится от 30 секунд.</p>
          ) : (
            <>
              <p className="inet-sum">
                Без интернета <b>{span(rep.down_seconds)}</b>
                {rep.provider_seconds > 0 && <>, из них <b>{span(rep.provider_seconds)}</b> роутер отвечал, а внешние адреса нет</>}
                {rep.local_seconds > 0 && <>, <b>{span(rep.local_seconds)}</b> не отвечал сам роутер</>}
                . Доступность <b className={`pct ${pctTone(rep.pct)}`}>{pctText(rep.pct)}</b>.
              </p>
              <div className="tbl-box">
                <table className="prob-table">
                  <thead><tr><th>Началось</th><th>Закончилось</th><th>Длилось</th><th>Причина</th></tr></thead>
                  <tbody>
                    {rep.outages.map((o) => (
                      <tr key={o.id} className={o.closed_at ? '' : 'open-2'}>
                        <td className="nowrap">{when(o.opened_at)}</td>
                        <td className="nowrap">{o.closed_at ? when(o.closed_at) : 'продолжается'}</td>
                        <td className="nowrap">{duration(o.opened_at, o.closed_at)}</td>
                        <td className="wrap">{causeText[o.cause]}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
          <p className="muted small">Отчёт открывается в Excel. Время, когда сама коробка была выключена, в журнал не попадает: в эти минуты проверок не было.</p>
        </section>
      )}

      <InetSettings config={d.config} onSaved={(config) => { setD({ ...d, config }); api.inet().then(setD).catch(() => {}) }} />
    </>
  )
}

function InetSettings({ config, onSaved }) {
  const [enabled, setEnabled] = useState(config.enabled)
  const [router, setRouter] = useState(config.router || '')
  const [targets, setTargets] = useState(config.targets)
  const [msg, setMsg] = useState(null)
  const [busy, setBusy] = useState(false)

  const edit = (i, k, v) => setTargets(targets.map((t, j) => (j === i ? { ...t, [k]: v } : t)))

  async function submit(e) {
    e.preventDefault()
    setBusy(true); setMsg(null)
    try {
      const r = await api.inetSave({ enabled, router, targets })
      setTargets(r.config.targets)
      setMsg({ tone: 'ok', text: enabled ? 'Сохранено. Новая проверка уже идёт, результат появится в течение минуты.' : 'Проверка интернета выключена.' })
      onSaved(r.config)
    } catch (err) { setMsg({ tone: 'critical', text: err.message }) }
    setBusy(false)
  }

  return (
    <form className="form wide-form inet-form" onSubmit={submit}>
      <h2>Настройки проверки</h2>
      <label className="check"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />Проверять интернет-канал</label>
      <label>Адрес роутера в офисе
        <input value={router} onChange={(e) => setRouter(e.target.value)} placeholder="например 192.168.1.1" maxLength={253} inputMode="url" autoComplete="off" />
        <span className="hint">Необязательно. С ним видно, где сбой: у провайдера или внутри офиса.</span>
      </label>
      <fieldset className="tgts">
        <legend>Внешние адреса</legend>
        {targets.map((t, i) => (
          <div className="tgt-row" key={i}>
            <input aria-label="Название" value={t.name} onChange={(e) => edit(i, 'name', e.target.value)} placeholder="Название" maxLength={30} />
            <input aria-label="Адрес" value={t.addr} onChange={(e) => edit(i, 'addr', e.target.value)} placeholder="IP или имя сайта" maxLength={253} required autoComplete="off" />
            <button type="button" className="btn small-btn" disabled={targets.length === 1} onClick={() => setTargets(targets.filter((_, j) => j !== i))}>Убрать</button>
          </div>
        ))}
        {targets.length < 5 && <button type="button" className="link-btn" onClick={() => setTargets([...targets, { name: '', addr: '' }])}>Добавить адрес</button>}
        <span className="hint">Интернет считается недоступным, только когда не отвечает ни один из них. Один заблокированный адрес картину не портит.</span>
      </fieldset>
      {msg && <p className={`banner ${msg.tone}`}>{msg.text}</p>}
      <button className="btn primary" type="submit" disabled={busy}>Сохранить</button>
    </form>
  )
}
