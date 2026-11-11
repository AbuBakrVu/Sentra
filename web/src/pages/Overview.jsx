import { useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago, plural, statusText } from '../format.js'
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

function headline(hosts) {
  const bad = hosts.filter((h) => h.status === 'critical' || h.status === 'offline').length
  const warn = hosts.filter((h) => h.status === 'warning').length
  if (bad) return { tone: 'critical', text: `${bad} ${plural(bad, 'авария', 'аварии', 'аварий')} ${bad === 1 ? 'требует' : 'требуют'} внимания` }
  if (warn) return { tone: 'warning', text: `Всё работает, но ${warn} ${plural(warn, 'устройство', 'устройства', 'устройств')} на пределе` }
  return { tone: 'ok', text: 'Всё в порядке' }
}

// Средняя доступность по устройствам, у которых есть данные.
function avgPct(hosts) {
  const ps = hosts.map((h) => h.availability?.pct).filter((p) => p != null)
  if (!ps.length) return null
  return Math.floor((ps.reduce((a, b) => a + b, 0) / ps.length) * 100) / 100
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

export default function Overview() {
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

  const hl = headline(hosts)
  const now = sorted.filter((h) => h.problems.length > 0)
  const past = sorted.filter((h) => hadProblems(h.availability))
  const shown = filter === 'now' ? now : filter === 'past' ? past : sorted
  const stopped = hosts.filter((h) => h.status === 'stopped').length
  const online = hosts.filter((h) => h.online && h.status !== 'stopped').length
  const active = hosts.length - stopped
  const avg = avgPct(hosts)
  const open = hosts.flatMap((h) => h.problems.map((p) => ({ ...p, host: h })))
    .sort((a, b) => b.severity - a.severity || a.opened_at - b.opened_at)

  return (
    <>
      <div className="ov-head">
        <div className={`summary tone-${hl.tone}`}>
          <h1 className="headline">{hl.text}</h1>
          <p className="lead">
            На связи {online} из {active}{stopped > 0 && `, ещё ${stopped} ${plural(stopped, 'машина выключена', 'машины выключены', 'машин выключено')}`}.
            {avg != null && ` Доступность за 30 дней ${pctText(avg)}.`}
            {checkedAt && ` Обновлено в ${checkedAt.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}.`}
            {error && <span className="inline-error"> Не удалось обновить: {error}</span>}
          </p>
        </div>
        <div className="toolbar" role="group" aria-label="Вид">
          <button className={viewMode === 'tiles' ? 'seg active' : 'seg'} onClick={() => pickView('tiles')}>Плитки</button>
          <button className={viewMode === 'table' ? 'seg active' : 'seg'} onClick={() => pickView('table')}>Таблица</button>
        </div>
      </div>

      {open.length > 0 && (
        <div className="probs" aria-label="Активные проблемы">
          {open.slice(0, 6).map((p) => (
            <div key={p.id} className="prob">
              <span className={`badge s-${p.severity === 2 ? 'critical' : 'warning'}`}>{sevText[p.severity]}</span>
              <span><b>{p.host.name}</b>: {p.title}{p.ack_by && <span className="muted">. В работе у {p.ack_by}{p.ack_comment ? `: ${p.ack_comment}` : ''}</span>}</span>
              <span className="muted nowrap">{since(p.opened_at)}</span>
              <Link to={`/hosts/${p.host.id}`}>Открыть</Link>
            </div>
          ))}
          {open.length > 6 && <div className="prob more"><Link to="/problems">Ещё {open.length - 6} — все проблемы</Link></div>}
        </div>
      )}

      <div className="toolbar" role="group" aria-label="Фильтр">
        <button className={filter === 'all' ? 'seg active' : 'seg'} onClick={() => setFilter('all')}>Все ({hosts.length})</button>
        <button className={filter === 'now' ? 'seg active' : 'seg'} onClick={() => setFilter('now')}>Проблемы сейчас ({now.length})</button>
        <button className={filter === 'past' ? 'seg active' : 'seg'} onClick={() => setFilter('past')}>Были за 30 дней ({past.length})</button>
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
