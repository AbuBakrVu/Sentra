import { useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago, plural, statusText } from '../format.js'

const order = { critical: 0, offline: 1, warning: 2, waiting: 3, ok: 4, stopped: 5 }
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
      ) : h.status === 'waiting' || h.status === 'offline' ? (
        <p className="tile-note">{h.status === 'offline' ? `Последние данные ${ago(h.last_seen)}` : 'Агент зарегистрирован, ждём первые метрики'}</p>
      ) : (
        <div className="meters">
          <Meter label="CPU" value={m.cpu_usage_percent} />
          <Meter label="Память" value={m.mem_used_percent} />
          {h.kind === 'pve-qemu' && !m.disk_max_mount
            ? <div className="meter"><span className="meter-label">Диск</span><span className="meter-hint">видно с гостевым агентом</span></div>
            : <Meter label="Диск" value={m.disk_max_percent} title={m.disk_max_mount ? `Самый заполненный раздел: ${m.disk_max_mount}` : undefined} />}
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
        <p className="lead">Подключите первый сервер: панель выдаст одну команду, её нужно выполнить на сервере. Через минуту здесь появятся его графики. Или подключите Proxmox: все узлы и виртуальные машины появятся сами.</p>
        <div className="row-btns">
          <Link to="/add" className="btn primary">Добавить сервер</Link>
          <Link to="/add/proxmox" className="btn">Подключить Proxmox</Link>
        </div>
      </div>
    )
  }

  const hl = headline(hosts)
  const problems = sorted.filter((h) => h.status !== 'ok' && h.status !== 'waiting' && h.status !== 'stopped')
  const shown = filter === 'problems' ? problems : sorted
  const stopped = hosts.filter((h) => h.status === 'stopped').length
  const online = hosts.filter((h) => h.online && h.status !== 'stopped').length
  const active = hosts.length - stopped

  return (
    <>
      <div className={`summary tone-${hl.tone}`}>
        <h1 className="headline">{hl.text}</h1>
        <p className="lead">
          На связи {online} из {active} {plural(active, 'устройства', 'устройств', 'устройств')}.
          {stopped > 0 && ` Выключено ${stopped} ${plural(stopped, 'машина', 'машины', 'машин')}.`}
          {checkedAt && ` Обновлено в ${checkedAt.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}.`}
          {error && <span className="inline-error"> Не удалось обновить: {error}</span>}
        </p>
      </div>
      <div className="toolbar" role="group" aria-label="Фильтр">
        <button className={filter === 'all' ? 'seg active' : 'seg'} onClick={() => setFilter('all')}>Все ({hosts.length})</button>
        <button className={filter === 'problems' ? 'seg active' : 'seg'} onClick={() => setFilter('problems')}>С проблемами ({problems.length})</button>
      </div>
      {shown.length === 0 ? <p className="muted">Проблем нет.</p> : groups(shown).map((g) => (
        <section key={g.key} className="group" aria-labelledby={`g-${g.key}`}>
          <div className="group-head">
            <h2 id={`g-${g.key}`}>{g.title}</h2>
            <p>{g.note}</p>
          </div>
          <div className="grid">{g.items.map((h) => <Tile key={h.id} h={h} />)}</div>
        </section>
      ))}
    </>
  )
}
