import { useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago, plural, statusText } from '../format.js'
import Icon from '../Icons.jsx'
import AvailabilityBar, { hadProblems, pctText, pctTone } from '../Availability.jsx'

const order = { critical: 0, offline: 1, warning: 2, waiting: 3, ok: 4, stopped: 5 }
const sevText = { 1: 'Внимание', 2: 'Авария' }
const kindOrder = { 'pve-node': 0, 'pve-qemu': 1, 'pve-lxc': 1 }

export const isPVE = (h) => h.kind?.startsWith('pve-')

// Подпись под именем: что это за устройство и где оно.
export function subtitle(h) {
  const m = h.meta || {}
  if (h.kind === 'pve-node') return ['Узел Proxmox', m.pve_version && `PVE ${m.pve_version}`].filter(Boolean).join(', ')
  if (h.kind === 'pve-qemu') return `ВМ ${m.vmid}, ${m.maxcpu} vCPU, ${bytesShort(m.maxmem)}`
  if (h.kind === 'pve-lxc') return `Контейнер ${m.vmid}, ${m.maxcpu} vCPU, ${bytesShort(m.maxmem)}`
  return [h.inventory?.os_name, h.inventory?.ips?.[0]].filter(Boolean).join(', ')
}

function bytesShort(b) {
  if (!b) return '—'
  const g = b / 1024 ** 3
  return g >= 1 ? `${Math.round(g * 10) / 10} ГБ`.replace('.', ',') : `${Math.round(b / 1024 ** 2)} МБ`
}

// Группы на обзоре: серверы с агентом, затем каждый узел Proxmox со своими машинами.
function groups(list) {
  const out = new Map()
  for (const h of list) {
    let key, title, src
    if (isPVE(h)) {
      key = `pve:${h.source_id}:${h.meta?.node}`
      title = `Proxmox: ${h.meta?.node}`
      src = h.meta?.source_name
    } else {
      key = 'agents'
      title = 'Серверы'
    }
    if (!out.has(key)) out.set(key, { key, title, src, items: [] })
    out.get(key).items.push(h)
  }
  const gs = [...out.values()]
  gs.sort((a, b) => (a.key === 'agents' ? -1 : b.key === 'agents' ? 1 : a.title.localeCompare(b.title, 'ru')))
  for (const g of gs) {
    if (g.key !== 'agents') g.items.sort((a, b) => (kindOrder[a.kind] ?? 2) - (kindOrder[b.kind] ?? 2))
    const vms = g.items.filter((h) => h.kind === 'pve-qemu' || h.kind === 'pve-lxc')
    const running = vms.filter((h) => h.status !== 'stopped' && h.status !== 'offline').length
    g.note = g.key === 'agents'
      ? `${g.items.length} ${plural(g.items.length, 'сервер', 'сервера', 'серверов')} с агентом`
      : `${[g.src, `${vms.length} ${plural(vms.length, 'машина', 'машины', 'машин')}, запущено ${running}`].filter((x) => x && x !== g.title).join(', ')}`
  }
  return gs
}

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

// Средняя доступность по устройствам, у которых есть данные.
function avgPct(hosts) {
  const ps = hosts.map((h) => h.availability?.pct).filter((p) => p != null)
  if (!ps.length) return null
  return Math.floor((ps.reduce((a, b) => a + b, 0) / ps.length) * 100) / 100
}

function greeting() {
  const h = new Date().getHours()
  return h < 5 ? 'Доброй ночи' : h < 12 ? 'Доброе утро' : h < 18 ? 'Добрый день' : 'Добрый вечер'
}

// Худшее состояние за каждый день по всем устройствам.
const rank = { down: 4, warn: 3, ok: 2, off: 1, none: 0 }
function fleetDays(hosts) {
  const out = Array(30).fill('none')
  for (const h of hosts) {
    (h.availability?.days || []).forEach((d, i) => { if (rank[d.state] > rank[out[i]]) out[i] = d.state })
  }
  return out
}

function Stat({ title, value, sub, featured, tone = '', to, onClick }) {
  const arrow = <Icon name="arrow" size={18} />
  return (
    <div className={featured ? 'stat featured' : 'stat'}>
      <div className="stat-head">
        <h2>{title}</h2>
        {to ? <Link to={to} className="arrow-btn" aria-label={`Открыть: ${title}`}>{arrow}</Link>
          : <button className="arrow-btn" onClick={onClick} aria-label={`Показать: ${title}`}>{arrow}</button>}
      </div>
      <div className={`stat-val ${tone}`}>{value}</div>
      <p>{sub}</p>
    </div>
  )
}

function since(unix) {
  const s = Math.max(0, Math.round(Date.now() / 1000 - unix))
  if (s < 3600) return `${Math.max(1, Math.floor(s / 60))} мин`
  if (s < 86400) return `${Math.floor(s / 3600)} ч ${Math.floor((s % 3600) / 60)} мин`
  return `${Math.floor(s / 86400)} дн ${Math.floor((s % 86400) / 3600)} ч`
}

function maintUntil(h) {
  return new Date(h.maint_until * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
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
  return (
    <Link to={`/hosts/${h.id}`} className={`tile s-${h.status}`}>
      <div className="tile-head">
        <span className="tile-name">{h.name}</span>
        <span className={`badge s-${h.status}`}>{statusText[h.status]}</span>
      </div>
      <div className="tile-sub">{subtitle(h)}</div>
      {h.status === 'stopped' ? (
        <p className="tile-note">Выключена в Proxmox</p>
      ) : h.status === 'waiting' || !h.online ? (
        <p className="tile-note">{h.status === 'waiting' ? 'Агент зарегистрирован, ждём первые метрики' : `Последние данные ${ago(h.last_seen)}`}</p>
      ) : (
        <div className="meters">
          <Meter label="CPU" value={m.cpu_usage_percent} />
          <Meter label="Память" value={m.mem_used_percent} />
          {h.kind === 'pve-qemu' && !m.disk_max_mount
            ? <div className="meter"><span className="meter-label">Диск</span><span className="meter-hint">видно с гостевым агентом</span></div>
            : <Meter label="Диск" value={m.disk_max_percent} title={m.disk_max_mount ? `Самый заполненный раздел: ${m.disk_max_mount}` : undefined} />}
        </div>
      )}
      {h.issues.length > 0 && <p className={h.status === 'critical' || h.status === 'offline' ? 'tile-issues' : 'tile-issues warn'}>{h.issues.join(', ')}</p>}
      {h.maintenance && <p className="tile-maint">Обслуживание до {maintUntil(h)}</p>}
      <div className="tile-av"><AvailabilityBar av={h.availability} /></div>
    </Link>
  )
}

function nowText(h) {
  const m = h.last_metrics || {}
  if (h.status === 'stopped') return 'Выключена'
  if (h.status === 'waiting') return 'Ждём первые данные'
  if (h.issues.length) return h.issues[0]
  if (h.kind === 'pve-node' && m.disk_max_mount) return `В порядке, больше всего занято ${m.disk_max_mount}`
  return 'В порядке'
}

function HostTable({ g }) {
  const pct = (v) => (v == null ? '—' : `${Math.round(v)}%`)
  return (
    <div className="tbl-box ov-table">
      <table>
        <colgroup>
          <col style={{ width: '20%' }} /><col style={{ width: '15%' }} /><col style={{ width: '6%' }} /><col style={{ width: '7%' }} />
          <col style={{ width: '6%' }} /><col style={{ width: '20%' }} /><col style={{ width: '7%' }} /><col style={{ width: '19%' }} />
        </colgroup>
        <thead>
          <tr><th className="grp-th">{g.title}</th><th>Тип</th><th className="num">CPU</th><th className="num">Память</th><th className="num">Диск</th><th>Доступность, 30 дней</th><th className="num">%</th><th>Сейчас</th></tr>
        </thead>
        <tbody>
          {g.items.map((h) => {
            const m = h.last_metrics || {}
            const live = h.online && h.status !== 'stopped'
            return (
              <tr key={h.id} className={`row-${h.status}`}>
                <td><span className={`dot s-${h.status}`} aria-hidden="true" /><Link to={`/hosts/${h.id}`} className="nm">{h.name}</Link></td>
                <td className="muted">{subtitle(h)}</td>
                <td className="num">{live ? pct(m.cpu_usage_percent) : '—'}</td>
                <td className="num">{live ? pct(m.mem_used_percent) : '—'}</td>
                <td className={m.disk_max_percent > 85 && live ? 'num hot' : 'num'} title={m.disk_max_mount || undefined}>{live && m.disk_max_mount ? pct(m.disk_max_percent) : '—'}</td>
                <td><AvailabilityBar av={h.availability} compact labels={false} /></td>
                <td className={`num pct ${pctTone(h.availability?.pct)}`}>{pctText(h.availability?.pct)}</td>
                <td className={`now-${h.status}`}>{nowText(h)}{h.maintenance && <span className="maint-chip">обслуживание</span>}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function readView() {
  try { return localStorage.getItem('sentra.view') || 'tiles' } catch { return 'tiles' }
}

export default function Overview({ user }) {
  const { hosts, error, checkedAt } = useHosts()
  const [filter, setFilter] = useState('all')
  const [viewMode, setViewMode] = useState(readView)
  const pickView = (v) => { setViewMode(v); try { localStorage.setItem('sentra.view', v) } catch { /* приватный режим */ } }

  const sorted = useMemo(() => (hosts || []).slice().sort((a, b) =>
    order[a.status] - order[b.status] || a.name.localeCompare(b.name, 'ru')), [hosts])

  if (error && !hosts) return <p className="banner critical">{error}</p>
  if (!hosts) return <p className="muted">Загружаем список устройств…</p>

  if (hosts.length === 0) {
    return (
      <div className="empty">
        <h1 className="headline">Пока нечего охранять</h1>
        <p className="lead">Подключите первый сервер: панель выдаст одну команду, её нужно выполнить на сервере. Через минуту здесь появятся его графики. Или подключите Proxmox: все узлы и виртуальные машины появятся сами.</p>
        <div className="row-btns">
          <Link to="/add" className="btn primary">Добавить сервер</Link>
          <Link to="/add/proxmox" className="btn">Подключить Proxmox</Link>
        </div>
      </div>
    )
  }

  const now = sorted.filter((h) => h.problems.length > 0)
  const past = sorted.filter((h) => hadProblems(h.availability))
  const shown = filter === 'now' ? now : filter === 'past' ? past : sorted
  const stopped = hosts.filter((h) => h.status === 'stopped').length
  const offline = hosts.filter((h) => h.status === 'offline').length
  const online = hosts.filter((h) => h.online && h.status !== 'stopped').length
  const active = hosts.length - stopped
  const avg = avgPct(hosts)
  const open = hosts.flatMap((h) => h.problems.map((p) => ({ ...p, host: h })))
    .sort((a, b) => b.severity - a.severity || a.opened_at - b.opened_at)
  const crit = open.filter((p) => p.severity === 2).length
  const warn = open.length - crit
  const days = fleetDays(hosts)
  const badDays = days.filter((d) => d === 'down').length
  const showDevices = (f) => { setFilter(f); document.getElementById('devices')?.scrollIntoView({ behavior: 'smooth' }) }

  return (
    <>
      <div className="page-head">
        <div>
          <h1 className="headline">{greeting()}, {user?.login}</h1>
          <p className="lead">
            Что требует внимания сегодня
            {checkedAt && <span className="muted">. Обновлено в {checkedAt.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })}</span>}
            {error && <span className="inline-error">. Не удалось обновить: {error}</span>}
          </p>
        </div>
        <div className="head-btns">
          <Link to="/add/proxmox" className="btn pill"><Icon name="layers" size={18} />Proxmox</Link>
          <Link to="/add" className="btn primary pill"><Icon name="plus" size={18} />Сервер</Link>
        </div>
      </div>

      <div className="stats">
        <Stat featured title="Проблемы сейчас" value={open.length} to="/problems"
          sub={open.length ? `${crit} ${plural(crit, 'авария', 'аварии', 'аварий')} · ${warn} ${plural(warn, 'предупреждение', 'предупреждения', 'предупреждений')}` : 'аварий и предупреждений нет'} />
        <Stat title="Нет связи" value={offline} tone={offline ? 'bad' : ''} onClick={() => showDevices('now')}
          sub={offline ? 'данных нет дольше 3 минут' : 'все устройства отвечают'} />
        <Stat title="На связи" value={<>{online}<small> из {active}</small></>} onClick={() => showDevices('all')}
          sub={stopped ? `ещё ${stopped} ${plural(stopped, 'машина выключена', 'машины выключены', 'машин выключено')}` : 'выключенных машин нет'} />
        <Stat title="Доступность" value={pctText(avg)} tone={pctTone(avg)} onClick={() => showDevices('past')}
          sub={`среднее за 30 дней, ${past.length} ${plural(past.length, 'устройство', 'устройства', 'устройств')} с проблемами`} />
      </div>

      <div className="row2">
        <section className="card attn" aria-labelledby="attn-h">
          <div className="card-head">
            <h2 id="attn-h">Требуют внимания</h2>
            {open.length > 0 ? <Link to="/problems" className="more-link">Все проблемы →</Link> : <span className="muted">0</span>}
          </div>
          {open.length === 0 ? <p className="all-good"><Icon name="check" size={18} />Всё под контролем</p> : (
            <div className="probs">
              {open.slice(0, 5).map((p) => (
                <Link key={p.id} to={`/hosts/${p.host.id}`} className="prob">
                  <span className={`badge s-${p.severity === 2 ? 'critical' : 'warning'}`}>{sevText[p.severity]}</span>
                  <span><b>{p.host.name}</b>: {p.title}{p.ack_by && <span className="muted">. В работе у {p.ack_by}{p.ack_comment ? `: ${p.ack_comment}` : ''}</span>}</span>
                  <span className="muted nowrap">{since(p.opened_at)}</span>
                </Link>
              ))}
              {open.length > 5 && <Link to="/problems" className="prob more">Ещё {open.length - 5}</Link>}
            </div>
          )}
        </section>
        <section className="card dark av-card" aria-labelledby="av-h">
          <h2 id="av-h">Доступность за 30 дней</h2>
          <p className="sub">{badDays ? `${badDays} ${plural(badDays, 'день', 'дня', 'дней')} с простоем хотя бы одного устройства` : 'Ни одного дня с простоем'}</p>
          <div className="big-num">{pctText(avg)}</div>
          <div className="av-cells" role="img" aria-label={`Сводка по дням: ${badDays} дней с простоем`}>
            {days.map((d, i) => <span key={i} className={`av-cell av-${d}`} />)}
          </div>
          <div className="av-labels"><span>30 дней назад</span><span>сегодня</span></div>
        </section>
      </div>

      <div className="dev-head" id="devices">
        <h2>Устройства</h2>
        <div className="toolbar" role="group" aria-label="Фильтр">
          <button className={filter === 'all' ? 'seg active' : 'seg'} onClick={() => setFilter('all')}>Все ({hosts.length})</button>
          <button className={filter === 'now' ? 'seg active' : 'seg'} onClick={() => setFilter('now')}>Проблемы сейчас ({now.length})</button>
          <button className={filter === 'past' ? 'seg active' : 'seg'} onClick={() => setFilter('past')}>Были за 30 дней ({past.length})</button>
        </div>
        <div className="toolbar" role="group" aria-label="Вид">
          <button className={viewMode === 'tiles' ? 'seg active' : 'seg'} onClick={() => pickView('tiles')}>Плитки</button>
          <button className={viewMode === 'table' ? 'seg active' : 'seg'} onClick={() => pickView('table')}>Таблица</button>
        </div>
      </div>

      {shown.length === 0 ? <p className="muted">Таких устройств нет.</p> : groups(shown).map((g) => (
        viewMode === 'table'
          ? <section key={g.key} className="group"><HostTable g={g} /></section>
          : (
            <section key={g.key} className="group" aria-labelledby={`g-${g.key}`}>
              <div className="group-head">
                <h2 id={`g-${g.key}`}>{g.title}</h2>
                <p>{g.note}</p>
              </div>
              <div className="grid">{g.items.map((h) => <Tile key={h.id} h={h} />)}</div>
            </section>
          )
      ))}
      <p className="av-legend">
        <span><span className="av-cell av-ok" />всё работало</span>
        <span><span className="av-cell av-warn" />были предупреждения</span>
        <span><span className="av-cell av-down" />был простой</span>
        <span><span className="av-cell av-off" />выключено</span>
        <span>Квадрат — один день, справа сегодня. Наведите, чтобы увидеть, что случилось.</span>
      </p>
    </>
  )
}
