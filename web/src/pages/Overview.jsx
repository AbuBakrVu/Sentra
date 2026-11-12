import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago, plural, statusText } from '../format.js'
import Icon from '../Icons.jsx'
import AvailabilityBar, { avText, avTone, hadProblems, pctText, pctTone } from '../Availability.jsx'
import Fresh from '../Fresh.jsx'
import { inetStatusText, lossText, ms } from './Internet.jsx'

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
export function groups(list) {
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
  // несколько подключений Proxmox: узлы с одинаковыми именами различаем по кластеру
  const many = new Set(gs.filter((g) => g.key !== 'agents').map((g) => g.src)).size > 1
  if (many) for (const g of gs) if (g.key !== 'agents') g.title = `${g.src}: ${g.title.replace('Proxmox: ', '')}`
  // по кластеру, внутри по номеру узла: pve2 раньше pve10
  gs.sort((a, b) => (a.key === 'agents' ? -1 : b.key === 'agents' ? 1
    : (a.src || '').localeCompare(b.src || '', 'ru') || a.title.localeCompare(b.title, 'ru', { numeric: true })))
  for (const g of gs) {
    if (g.key !== 'agents') g.items.sort((a, b) => (kindOrder[a.kind] ?? 2) - (kindOrder[b.kind] ?? 2))
    const vms = g.items.filter((h) => h.kind === 'pve-qemu' || h.kind === 'pve-lxc')
    const running = vms.filter((h) => h.status !== 'stopped' && h.status !== 'offline').length
    g.note = g.key === 'agents'
      ? `${g.items.length} ${plural(g.items.length, 'сервер', 'сервера', 'серверов')} с агентом`
      : `${[!many && g.src, `${vms.length} ${plural(vms.length, 'машина', 'машины', 'машин')}, запущено ${running}`].filter((x) => x && x !== g.title).join(', ')}`
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

// Средняя доступность по устройствам, за которыми наблюдаем дольше суток.
function avgPct(hosts) {
  const ps = hosts.filter((h) => !h.availability?.young).map((h) => h.availability?.pct).filter((p) => p != null)
  if (!ps.length) return null
  return Math.floor((ps.reduce((a, b) => a + b, 0) / ps.length) * 100) / 100
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

function Stat({ title, value, sub, featured, tone = '', to, onClick, children }) {
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
      {children}
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

const sorters = {
  name: (h) => h.name.toLowerCase(),
  cpu: (h) => (h.online && h.status !== 'stopped' ? h.last_metrics?.cpu_usage_percent ?? -1 : -1),
  mem: (h) => (h.online && h.status !== 'stopped' ? h.last_metrics?.mem_used_percent ?? -1 : -1),
  disk: (h) => (h.online && h.status !== 'stopped' ? h.last_metrics?.disk_max_percent ?? -1 : -1),
  av: (h) => (h.availability?.young ? 101 : h.availability?.pct ?? 101),
}

function sortItems(items, sort) {
  if (!sort) return items
  const f = sorters[sort.key]
  return items.slice().sort((a, b) => {
    const x = f(a), y = f(b)
    const c = typeof x === 'string' ? x.localeCompare(y, 'ru') : x - y
    return sort.dir * c || a.name.localeCompare(b.name, 'ru')
  })
}

function useSort(k, sort, setSort) {
  const on = sort?.key === k
  // числа сначала по убыванию: интереснее, кто занял больше всего
  const first = k === 'name' || k === 'av' ? 1 : -1
  const next = () => setSort(on ? (sort.dir === first ? { key: k, dir: -first } : null) : { key: k, dir: first })
  return { on, next, arrow: on ? (sort.dir > 0 ? '↑' : '↓') : '↕' }
}

function SortTh({ k, sort, setSort, className = '', grip, children }) {
  const { on, next, arrow } = useSort(k, sort, setSort)
  return (
    <th className={className} aria-sort={on ? (sort.dir > 0 ? 'ascending' : 'descending') : undefined}>
      <button type="button" className={on ? 'th-sort on' : 'th-sort'} onClick={next}>
        {children}<i aria-hidden="true">{arrow}</i>
      </button>
      {grip}
    </th>
  )
}

function SortIcon({ k, sort, setSort, label }) {
  const { on, next, arrow } = useSort(k, sort, setSort)
  return (
    <button type="button" className={on ? 'th-sort on' : 'th-sort'} onClick={next} aria-label={`Сортировать ${label}`} title={`Сортировать ${label}`}>
      <i aria-hidden="true">{arrow}</i>
    </button>
  )
}

// Ширины столбцов таблицы в процентах. Сумма всегда 100, и таблица не вылезает вбок:
// расширяем столбец — место забирается у столбцов справа по очереди, пока они не
// упрутся в минимум; сужаем — освободившееся место получает правый сосед.
export const COLS_DEFAULT = [17, 15, 6, 7, 6, 19, 9, 21]
// меньше этого столбец не сужается: в нём должно помещаться значение, например «100%»
const COL_MIN = [9, 7, 5, 6, 5, 8, 7, 8]

function resizeCol(start, i, d) {
  const w = start.slice()
  if (d < 0) {
    d = Math.max(COL_MIN[i] - w[i], d)
    w[i] += d
    w[i + 1] -= d
    return w
  }
  let left = d
  for (let k = i + 1; k < w.length && left > 0; k++) {
    const take = Math.max(0, Math.min(left, w[k] - COL_MIN[k]))
    w[k] -= take
    left -= take
  }
  w[i] += d - left
  return w
}

export function readCols() {
  try {
    const w = JSON.parse(localStorage.getItem('sentra.cols') || 'null')
    if (Array.isArray(w) && w.length === COLS_DEFAULT.length && Math.abs(w.reduce((a, b) => a + b, 0) - 100) < 0.5) return w
  } catch { /* нет сохранённых */ }
  return COLS_DEFAULT
}

export function saveCols(w) {
  try {
    if (w === COLS_DEFAULT) localStorage.removeItem('sentra.cols')
    else localStorage.setItem('sentra.cols', JSON.stringify(w))
  } catch { /* приватный режим */ }
}

// Ручка на правой границе заголовка. Двойной щелчок возвращает ширины по умолчанию.
function ColGrip({ i, cols, setCols, tableRef }) {
  const down = (e) => {
    e.preventDefault()
    e.stopPropagation()
    const tw = tableRef.current?.getBoundingClientRect().width || 1
    const x0 = e.clientX
    const start = cols
    let last = start
    const move = (ev) => {
      last = resizeCol(start, i, ((ev.clientX - x0) / tw) * 100)
      setCols(last)
    }
    const up = () => {
      document.removeEventListener('pointermove', move)
      document.removeEventListener('pointerup', up)
      document.body.classList.remove('col-dragging')
      saveCols(last)
    }
    document.addEventListener('pointermove', move)
    document.addEventListener('pointerup', up)
    document.body.classList.add('col-dragging')
  }
  // с клавиатуры: стрелки двигают границу на 1%
  const key = (e) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    const next = resizeCol(cols, i, e.key === 'ArrowRight' ? 1 : -1)
    setCols(next)
    saveCols(next)
  }
  return (
    <span className="col-grip" role="separator" aria-orientation="vertical" aria-label="Изменить ширину столбца" tabIndex={0}
      title="Потяните, чтобы изменить ширину. Двойной щелчок — ширина по умолчанию"
      onPointerDown={down} onKeyDown={key} onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => { e.stopPropagation(); setCols(COLS_DEFAULT); saveCols(COLS_DEFAULT) }} />
  )
}

function HostTable({ g, sort, setSort, closed, onToggle, cols, setCols }) {
  const pct = (v) => (v == null ? '—' : `${Math.round(v)}%`)
  const tableRef = useRef(null)
  const th = { sort, setSort }
  const grip = (i) => <ColGrip i={i} cols={cols} setCols={setCols} tableRef={tableRef} />
  const c = counts(g.items)
  return (
    <div className={`tbl-box ov-table${closed ? ' closed' : ''}`}>
      <table ref={tableRef}>
        <colgroup>{cols.map((w, i) => <col key={i} style={{ width: `${w}%` }} />)}</colgroup>
        <thead>
          <tr>
            <th className="grp-th">
              <span className="grp-cell">
                <button type="button" className="grp-toggle" aria-expanded={!closed} onClick={onToggle}
                  title={closed ? 'Развернуть' : 'Свернуть'}>
                  <Icon name="chevron" size={16} /><span className="grp-name">{g.title}</span>
                  {closed && <span className="grp-count">{g.items.length}</span>}
                </button>
                {!closed && <SortIcon k="name" {...th} label="по имени" />}
              </span>
              {grip(0)}
            </th>
            <th>Тип{grip(1)}</th>
            <SortTh k="cpu" {...th} className="num" grip={grip(2)}>CPU</SortTh>
            <SortTh k="mem" {...th} className="num" grip={grip(3)}>Память</SortTh>
            <SortTh k="disk" {...th} className="num" grip={grip(4)}>Диск</SortTh>
            <th>Доступность, 30 дней{grip(5)}</th>
            <SortTh k="av" {...th} className="num" grip={grip(6)}>%</SortTh>
            <th>Сейчас</th>
          </tr>
        </thead>
        <tbody>
          {closed ? (
            <tr className="grp-folded">
              <td colSpan={8}>
                <div className="grp-sum">
                  <span className="gh-counts">
                    {c.bad > 0 && <span className="gc bad">{c.bad} {plural(c.bad, 'авария', 'аварии', 'аварий')}</span>}
                    {c.warn > 0 && <span className="gc warn">{c.warn} с предупр.</span>}
                    <span className="gc">{c.ok} в порядке</span>
                    {c.stopped > 0 && <span className="gc off">{c.stopped} выкл.</span>}
                  </span>
                  <DotMap items={g.items} />
                </div>
              </td>
            </tr>
          ) : sortItems(g.items, sort).map((h) => {
            const m = h.last_metrics || {}
            const live = h.online && h.status !== 'stopped'
            const now = nowText(h)
            return (
              <tr key={h.id} className={`row-${h.status}`}>
                <td title={h.name}><span className={`dot s-${h.status}`} aria-hidden="true" /><Link to={`/hosts/${h.id}`} className="nm">{h.name}</Link></td>
                <td className="muted" data-label="Тип" title={subtitle(h)}>{subtitle(h)}</td>
                <td className="num" data-label="CPU">{live ? pct(m.cpu_usage_percent) : '—'}</td>
                <td className="num" data-label="Память">{live ? pct(m.mem_used_percent) : '—'}</td>
                <td className={m.disk_max_percent > 85 && live ? 'num hot' : 'num'} data-label="Диск" title={m.disk_max_mount || undefined}>{live && m.disk_max_mount ? pct(m.disk_max_percent) : '—'}</td>
                <td data-label="30 дней"><AvailabilityBar av={h.availability} compact labels={false} /></td>
                <td className={`num pct ${avTone(h.availability)}`} data-label="Доступность" title={avText(h.availability)}>{avText(h.availability)}</td>
                <td className={`now-${h.status}`} data-label="Сейчас" title={now}>{now}{h.maintenance && <span className="maint-chip">обслуживание</span>}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

// ---------- большие группы ----------

// С какого размера группа сворачивается: до этого все плитки помещаются на один-два ряда.
export const COMPACT_FROM = 9

const dotText = { ok: 'в порядке', warning: 'предупреждение', critical: 'авария', offline: 'нет связи', waiting: 'ждём данные', stopped: 'выключена' }
export const needsEye = (h) => h.problems.length > 0 || ['critical', 'offline', 'warning'].includes(h.status)

// Карта группы: один квадратик на машину. Порядок постоянный (по имени),
// чтобы квадратик машины не прыгал по карте при смене состояния.
export function DotMap({ items, big = false }) {
  const list = items.slice().sort((a, b) => (kindOrder[a.kind] ?? 2) - (kindOrder[b.kind] ?? 2) || a.name.localeCompare(b.name, 'ru', { numeric: true }))
  return (
    <div className={big ? 'dotmap big' : 'dotmap'}>
      {list.map((h) => {
        const label = `${h.name}: ${h.issues[0] ? h.issues[0].toLowerCase() : dotText[h.status]}`
        return big
          ? <span key={h.id} className={`dm s-${h.status}${h.kind === 'pve-node' ? ' node' : ''}`} title={label} />
          : <Link key={h.id} to={`/hosts/${h.id}`} className={`dm s-${h.status}${h.kind === 'pve-node' ? ' node' : ''}`} title={label} aria-label={label} />
      })}
    </div>
  )
}

function counts(items) {
  const c = { bad: 0, warn: 0, ok: 0, stopped: 0 }
  for (const h of items) {
    if (h.status === 'stopped') c.stopped++
    else if (h.status === 'critical' || h.status === 'offline') c.bad++
    else if (h.status === 'warning' || h.problems.length) c.warn++
    else c.ok++
  }
  return c
}

// Группа плитками. Маленькая группа — как раньше. Большая — карточка: сводка узла,
// карта всех машин и строки только про машины с проблемами. По кнопке карточка
// раскрывается на всю ширину и показывает все машины плитками.
function GroupTiles({ g, shown, open, toggle }) {
  const compact = g.items.length >= COMPACT_FROM
  if (!compact) {
    return (
      <section className="group" aria-labelledby={`g-${g.key}`}>
        <div className="group-head"><h2 id={`g-${g.key}`}>{g.title}</h2><p>{g.note}</p></div>
        <div className="grid">{shown.map((h) => <Tile key={h.id} h={h} />)}</div>
      </section>
    )
  }
  const c = counts(g.items)
  const node = g.items.find((h) => h.kind === 'pve-node')
  const nm = node?.last_metrics || {}
  const pct = (v) => (v == null ? '—' : `${Math.round(v)}%`)
  const eye = shown.filter((h) => h.kind !== 'pve-node' && needsEye(h))
    .sort((a, b) => order[a.status] - order[b.status] || a.name.localeCompare(b.name, 'ru'))
  const tone = c.bad ? 'bad' : c.warn ? 'warn' : 'ok'
  // на телефоне спокойный узел свёрнут в одну строку, узел с проблемами раскрыт
  const [peek, setPeek] = useState(false)
  const folded = tone === 'ok' && !peek && !open
  return (
    <section className={`gcard t-${tone}${open ? ' open' : ''}${folded ? ' folded' : ''}`} aria-labelledby={`g-${g.key}`}>
      <div className="gcard-head">
        <h2 id={`g-${g.key}`}>{node ? <Link to={`/hosts/${node.id}`}>{g.title}</Link> : g.title}</h2>
        {tone === 'ok' && !open && (
          <button type="button" className="gcard-toggle" aria-expanded={!folded} onClick={() => setPeek((v) => !v)}>
            <span className="sr-only">{folded ? 'Показать машины узла' : 'Свернуть'}</span>
            <Icon name="arrow" size={16} />
          </button>
        )}
        <p className="gh-counts">
          {c.bad > 0 && <span className="gc bad">{c.bad} {plural(c.bad, 'авария', 'аварии', 'аварий')}</span>}
          {c.warn > 0 && <span className="gc warn">{c.warn} с предупр.</span>}
          <span className="gc">{c.ok} в порядке</span>
          {c.stopped > 0 && <span className="gc off">{c.stopped} выкл.</span>}
        </p>
      </div>
      <p className="gcard-sub">
        {g.note}
        {node && node.online && <> · узел: CPU <b>{pct(nm.cpu_usage_percent)}</b>, память <b>{pct(nm.mem_used_percent)}</b>{nm.disk_max_mount && <>, диск <b className={nm.disk_max_percent > 85 ? 'hot' : ''}>{pct(nm.disk_max_percent)}</b></>}</>}
        {node && node.issues.length > 0 && <span className="gcard-node-bad"> · {node.issues[0]}</span>}
      </p>
      <DotMap items={g.items} />
      {!open && (eye.length > 0 ? (
        <ul className="gcard-probs">
          {eye.slice(0, 4).map((h) => (
            <li key={h.id}>
              <Link to={`/hosts/${h.id}`} className={`gp s-${h.status}`}>
                <span className={`dot s-${h.status}`} aria-hidden="true" /><b>{h.name}</b>
                <span className="gp-what">{h.issues[0] || statusText[h.status]}</span>
              </Link>
            </li>
          ))}
          {eye.length > 4 && <li className="muted gp-more">и ещё {eye.length - 4} с проблемами</li>}
        </ul>
      ) : <p className="gcard-calm">Все запущенные машины в порядке.</p>)}
      {open && <div className="grid">{shown.map((h) => <Tile key={h.id} h={h} />)}</div>}
      <button type="button" className="link-btn group-more" onClick={toggle} aria-expanded={!!open}>
        {open ? 'Свернуть' : `Все машины плитками (${shown.length})`}
      </button>
    </section>
  )
}

function readOpenGroups() {
  try { return JSON.parse(localStorage.getItem('sentra.openGroups') || '{}') } catch { return {} }
}

// Строка про интернет-канал под счётчиками.
function InetStrip({ h }) {
  const m = h.last_metrics || {}
  const up = m.inet_up === 1
  return (
    <Link to="/internet" className={`inet-strip s-${h.status}`}>
      <Icon name="globe" size={20} />
      <b>Интернет</b>
      <span className={`badge s-${h.status}`}>{inetStatusText[h.status]}</span>
      <span className="inet-now">
        {h.issues.length ? h.issues[0] : h.status === 'waiting' ? 'первая проверка займёт до минуты' : up ? `потери ${lossText(m.loss5)}, задержка ${ms(m.rtt5)}` : ''}
      </span>
      <span className="muted inet-av">{h.availability?.young ? 'доступность: мало данных' : <>за 30 дней <b className={`pct ${pctTone(h.availability?.pct)}`}>{pctText(h.availability?.pct)}</b></>}</span>
      <Icon name="arrow" size={18} />
    </Link>
  )
}

function readView() {
  try { return localStorage.getItem('sentra.view') || 'tiles' } catch { return 'tiles' }
}

// Главная фраза экрана: всё ли в порядке и, если нет, что именно.
export function verdict(open) {
  if (open.length === 0) return { tone: 'ok', title: 'Всё работает', text: '' }
  const crit = open.some((p) => p.severity === 2)
  const n = open.length
  const first = open.slice(0, 2).map((p) => `${p.host.name}: ${p.title.charAt(0).toLowerCase()}${p.title.slice(1)}`).join('; ')
  return {
    tone: crit ? 'critical' : 'warning',
    title: `${n} ${plural(n, 'проблема требует', 'проблемы требуют', 'проблем требуют')} внимания`,
    text: n > 2 ? `${first} и ещё ${n - 2}` : first,
  }
}

export function openProblems(all) {
  return all.flatMap((h) => h.problems.map((p) => ({ ...p, host: h })))
    .sort((a, b) => b.severity - a.severity || a.opened_at - b.opened_at)
}

export default function Overview() {
  const { hosts: all, error, checkedAt } = useHosts()
  // интернет-канал — не устройство: у него своя строка и своя страница
  const hosts = useMemo(() => all?.filter((h) => h.kind !== 'inet') ?? null, [all])
  const inet = all?.find((h) => h.kind === 'inet' && h.status !== 'stopped')
  const [filter, setFilter] = useState('all')
  const [viewMode, setViewMode] = useState(readView)
  const [showStopped, setShowStopped] = useState(() => { try { return localStorage.getItem('sentra.stopped') === '1' } catch { return false } })
  const [openGroups, setOpenGroups] = useState(readOpenGroups)
  const [sort, setSort] = useState(null)
  const [cols, setCols] = useState(readCols)
  const [closedT, setClosedT] = useState(() => { try { return JSON.parse(localStorage.getItem('sentra.tableClosed') || '{}') } catch { return {} } })
  const saveClosed = (n) => { setClosedT(n); try { localStorage.setItem('sentra.tableClosed', JSON.stringify(n)) } catch { /* приватный режим */ } }
  const toggleT = (k) => { const n = { ...closedT, [k]: !closedT[k] }; if (!n[k]) delete n[k]; saveClosed(n) }
  const pickStopped = (v) => { setShowStopped(v); try { localStorage.setItem('sentra.stopped', v ? '1' : '0') } catch { /* приватный режим */ } }
  const toggleGroup = (k) => setOpenGroups((o) => {
    const n = { ...o, [k]: !o[k] }
    if (!n[k]) delete n[k]
    try { localStorage.setItem('sentra.openGroups', JSON.stringify(n)) } catch { /* приватный режим */ }
    return n
  })
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
        {inet && <div className="empty-inet"><InetStrip h={inet} /></div>}
      </div>
    )
  }

  const now = sorted.filter((h) => h.problems.length > 0)
  const past = sorted.filter((h) => hadProblems(h.availability))
  const byFilter = filter === 'now' ? now : filter === 'past' ? past : sorted
  // выключенные машины в списке по умолчанию не показываем: на карте группы они видны серыми
  const visible = (h) => showStopped || h.status !== 'stopped' || filter !== 'all'
  const inFilter = new Set(byFilter.filter(visible).map((h) => h.id))
  const allGroups = groups(sorted)
    .map((g) => ({ g, shown: g.items.filter((h) => inFilter.has(h.id)) }))
    .filter(({ g, shown }) => shown.length > 0 || (filter === 'all' && g.items.length >= COMPACT_FROM))
  const stopped = hosts.filter((h) => h.status === 'stopped').length
  const offline = hosts.filter((h) => h.status === 'offline').length
  const online = hosts.filter((h) => h.online && h.status !== 'stopped').length
  const active = hosts.length - stopped
  const avg = avgPct(hosts)
  const young = avg == null && hosts.some((h) => h.availability?.young)
  const open = openProblems(all)
  const crit = open.filter((p) => p.severity === 2).length
  const warn = open.length - crit
  const days = fleetDays(hosts)
  const badDays = days.filter((d) => d === 'down').length
  const v = verdict(open)
  // в больших группах проблемы и так видны в карточке узла
  const hasCards = allGroups.some(({ g }) => g.items.length >= COMPACT_FROM)
  const showDevices = (f) => { setFilter(f); document.getElementById('devices')?.scrollIntoView({ behavior: 'smooth' }) }

  return (
    <>
      <div className={`page-head verdict v-${v.tone}`}>
        <div>
          <h1 className="headline">{v.title}</h1>
          <p className="lead">
            {v.text || `На связи ${online} из ${active} ${plural(active, 'устройства', 'устройств', 'устройств')}${inet ? (inet.status === 'ok' ? ', интернет работает' : inet.status === 'waiting' ? ', интернет проверяется' : '') : ''}.`}
          </p>
        </div>
        <div className="head-side">
          <Fresh at={checkedAt?.getTime()} />
          {error && <span className="inline-error">Не удалось обновить: {error}</span>}
        </div>
      </div>

      <div className="stats">
        <Stat featured title="Проблемы сейчас" value={open.length} to="/problems"
          sub={open.length ? `${crit} ${plural(crit, 'авария', 'аварии', 'аварий')} · ${warn} ${plural(warn, 'предупреждение', 'предупреждения', 'предупреждений')}` : 'аварий и предупреждений нет'} />
        <Stat title="Нет связи" value={offline} tone={offline ? 'bad' : ''} onClick={() => showDevices('now')}
          sub={offline ? 'данных нет дольше 3 минут' : 'все устройства отвечают'} />
        <Stat title="На связи" value={<>{online}<small> из {active}</small></>} onClick={() => showDevices('all')}
          sub={stopped ? `ещё ${stopped} ${plural(stopped, 'машина выключена', 'машины выключены', 'машин выключено')}` : 'выключенных машин нет'} />
        <Stat title="Доступность за 30 дней" value={young ? <span className="few">мало данных</span> : pctText(avg)} tone={pctTone(avg)} onClick={() => showDevices('past')}
          sub={young ? 'процент появится через сутки наблюдения' : badDays ? `${badDays} ${plural(badDays, 'день', 'дня', 'дней')} с простоем хотя бы одного устройства` : 'ни одного дня с простоем'}>
          <div className="av-cells stat-days" role="img" aria-label={`Сводка по дням: дней с простоем ${badDays}`}>
            {days.map((d, i) => <span key={i} className={`av-cell av-${d}`} />)}
          </div>
        </Stat>
      </div>

      {inet && <InetStrip h={inet} />}

      {open.length > 0 && !(viewMode === 'tiles' && hasCards) && (
        <section className="card attn" aria-labelledby="attn-h">
          <div className="card-head">
            <h2 id="attn-h">Требуют внимания</h2>
            <Link to="/problems" className="more-link">Все проблемы →</Link>
          </div>
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
        </section>
      )}

      <div className="dev-head" id="devices">
        <h2>Устройства</h2>
        <div className="toolbar" role="group" aria-label="Фильтр">
          <button className={filter === 'all' ? 'seg active' : 'seg'} onClick={() => setFilter('all')}>Все ({hosts.length})</button>
          <button className={filter === 'now' ? 'seg active' : 'seg'} onClick={() => setFilter('now')}>Проблемы сейчас ({now.length})</button>
          <button className={filter === 'past' ? 'seg active' : 'seg'} onClick={() => setFilter('past')}>Были за 30 дней ({past.length})</button>
        </div>
        {stopped > 0 && filter === 'all' && (
          <label className="chk-pill"><input type="checkbox" checked={showStopped} onChange={(e) => pickStopped(e.target.checked)} />Выключенные ({stopped})</label>
        )}
        <div className="toolbar" role="group" aria-label="Вид">
          <button className={viewMode === 'tiles' ? 'seg active' : 'seg'} onClick={() => pickView('tiles')}>Плитки</button>
          <button className={viewMode === 'table' ? 'seg active' : 'seg'} onClick={() => pickView('table')}>Таблица</button>
        </div>
      </div>

      {allGroups.length === 0 && <p className="muted">Таких устройств нет.</p>}
      {viewMode === 'table' && allGroups.length > 1 && (
        <div className="tbl-tools">
          <button type="button" className="link-btn" onClick={() => saveClosed(Object.fromEntries(allGroups.map(({ g }) => [g.key, true])))}>Свернуть все</button>
          <button type="button" className="link-btn" onClick={() => saveClosed({})}>Развернуть все</button>
          {cols !== COLS_DEFAULT && <button type="button" className="link-btn" onClick={() => { setCols(COLS_DEFAULT); saveCols(COLS_DEFAULT) }}>Ширина столбцов по умолчанию</button>}
          <span className="muted">Ширину столбца меняйте, потянув за границу в заголовке.</span>
        </div>
      )}
      <div className={viewMode === 'table' ? '' : 'groups'}>
      {allGroups.map(({ g, shown }) => (
        viewMode === 'table'
          ? (shown.length > 0 && <section key={g.key} className="group"><HostTable g={{ ...g, items: shown }} sort={sort} setSort={setSort}
              closed={!!closedT[g.key]} onToggle={() => toggleT(g.key)} cols={cols} setCols={setCols} /></section>)
          : <GroupTiles key={g.key} g={g} shown={shown} open={openGroups[g.key]} toggle={() => toggleGroup(g.key)} />
      ))}
      </div>
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
