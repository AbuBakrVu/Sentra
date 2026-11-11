import { useCallback, useEffect, useRef, useState } from 'react'
import Icon from './Icons.jsx'
import { api } from './api.js'
import Overview from './pages/Overview.jsx'
import HostPage from './pages/HostPage.jsx'
import AddServer from './pages/AddServer.jsx'
import AddProxmox from './pages/AddProxmox.jsx'
import Settings from './pages/Settings.jsx'
import Login from './pages/Login.jsx'
import Problems from './pages/Problems.jsx'

// Минимальный роутер на History API: нам хватает четырёх экранов.
export function navigate(to) {
  window.history.pushState({}, '', to)
  window.dispatchEvent(new Event('sentra:nav'))
  window.scrollTo(0, 0)
}

export function Link({ to, children, ...rest }) {
  return (
    <a href={to} {...rest} onClick={(e) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return
      e.preventDefault()
      navigate(to)
    }}>{children}</a>
  )
}

function usePath() {
  const [path, setPath] = useState(window.location.pathname)
  useEffect(() => {
    const on = () => setPath(window.location.pathname)
    window.addEventListener('popstate', on)
    window.addEventListener('sentra:nav', on)
    return () => { window.removeEventListener('popstate', on); window.removeEventListener('sentra:nav', on) }
  }, [])
  return path
}

export default function App() {
  const [user, setUser] = useState(undefined) // undefined — проверяем сессию
  const path = usePath()

  useEffect(() => {
    api.me().then(setUser).catch(() => setUser(null))
    const out = () => setUser(null)
    window.addEventListener('sentra:logout', out)
    return () => window.removeEventListener('sentra:logout', out)
  }, [])

  const logout = useCallback(async () => {
    try { await api.logout() } catch { /* всё равно выходим */ }
    setUser(null)
    navigate('/')
  }, [])

  if (user === undefined) return null
  if (!user) return <Login onLogin={setUser} />

  let page
  const host = path.match(/^\/hosts\/([a-z0-9_-]+)$/)
  if (host) page = <HostPage id={host[1]} />
  else if (path === '/add') page = <AddServer />
  else if (path === '/add/proxmox') page = <AddProxmox />
  else if (path === '/settings') page = <Settings />
  else if (path === '/problems') page = <Problems />
  else page = <Overview user={user} />

  const title = host ? 'Устройство' : path === '/problems' ? 'Проблемы' : path.startsWith('/add') ? 'Подключение' : path === '/settings' ? 'Настройки' : 'Обзор'

  return (
    <div className="shell">
      <Sidebar path={path} user={user} logout={logout} />
      <div className="content">
        <header className="topbar">
          <span className="crumb">{title}</span>
          <HostSearch />
          <Link to="/add" className="btn dark pill"><Icon name="plus" size={18} />Добавить</Link>
        </header>
        <main className="main">{page}</main>
      </div>
    </div>
  )
}

function useOpenCount() {
  const [n, setN] = useState(0)
  useEffect(() => {
    let alive = true
    const load = () => api.problems('open').then((l) => alive && setN(l.length)).catch(() => {})
    load()
    const t = setInterval(load, 30000)
    return () => { alive = false; clearInterval(t) }
  }, [])
  return n
}

function Sidebar({ path, user, logout }) {
  const open = useOpenCount()
  const item = (to, icon, label, extra) => {
    const on = to === '/' ? path === '/' || path.startsWith('/hosts/') : path === to
    return (
      <Link to={to} className={on ? 'side-link active' : 'side-link'} aria-current={on ? 'page' : undefined}>
        <Icon name={icon} size={22} />{label}{extra}
      </Link>
    )
  }
  return (
    <aside className="side">
      <div className="side-user">
        <span className="avatar" aria-hidden="true">{user.login.slice(0, 1).toUpperCase()}<i /></span>
        <span><b>{user.login}</b><small>Администратор</small></span>
        <Link to="/" className="side-brand" aria-label="Sentra, обзор">
          <svg width="18" height="18" viewBox="0 0 32 32" aria-hidden="true"><circle cx="16" cy="16" r="11" fill="none" stroke="currentColor" strokeWidth="3.5" /><circle cx="16" cy="16" r="4" fill="currentColor" /></svg>
        </Link>
      </div>
      <nav className="side-nav" aria-label="Разделы">
        {item('/', 'grid', 'Обзор')}
        {item('/problems', 'alert', 'Проблемы', open > 0 && <span className="side-count">{open}</span>)}
        <hr />
        {item('/add', 'server', 'Сервер с агентом')}
        {item('/add/proxmox', 'layers', 'Proxmox')}
        <hr />
        {item('/settings', 'gear', 'Настройки')}
      </nav>
      <div className="side-foot">
        <Link to="/settings" className="icon-btn" aria-label="Настройки" title="Настройки"><Icon name="gear" /></Link>
        <Link to="/add" className="icon-btn" aria-label="Добавить устройство" title="Добавить устройство"><Icon name="plus" /></Link>
        <button className="icon-btn" onClick={logout} aria-label="Выйти" title="Выйти"><Icon name="logout" /></button>
      </div>
    </aside>
  )
}

// Поиск по устройствам в шапке, Ctrl+K или ⌘K.
function HostSearch() {
  const [q, setQ] = useState('')
  const [list, setList] = useState(null)
  const [open, setOpen] = useState(false)
  const [sel, setSel] = useState(0)
  const ref = useRef(null)

  useEffect(() => {
    const on = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); ref.current?.focus() }
    }
    window.addEventListener('keydown', on)
    return () => window.removeEventListener('keydown', on)
  }, [])

  const load = () => { if (!list) api.hosts().then(setList).catch(() => setList([])) }
  const needle = q.trim().toLowerCase()
  const found = !needle || !list ? [] : list.filter((h) =>
    [h.name, h.inventory?.hostname, ...(h.inventory?.ips || []), h.meta?.node].filter(Boolean).some((x) => String(x).toLowerCase().includes(needle))).slice(0, 8)

  const go = (h) => { setQ(''); setOpen(false); setList(null); ref.current?.blur(); navigate(`/hosts/${h.id}`) }
  const mac = /Mac|iPhone|iPad/.test(navigator.platform)

  return (
    <div className="search">
      <Icon name="search" size={18} />
      <label className="sr-only" htmlFor="host-search">Поиск устройства</label>
      <input id="host-search" ref={ref} value={q} placeholder="Поиск по устройствам…" autoComplete="off"
        onFocus={() => { load(); setOpen(true) }} onBlur={() => setTimeout(() => setOpen(false), 150)}
        onChange={(e) => { setQ(e.target.value); setSel(0); setOpen(true) }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(s + 1, found.length - 1)) }
          if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(s - 1, 0)) }
          if (e.key === 'Enter' && found[sel]) go(found[sel])
          if (e.key === 'Escape') { setQ(''); ref.current?.blur() }
        }} />
      <kbd>{mac ? '⌘K' : 'Ctrl K'}</kbd>
      {open && needle && list && (
        <div className="search-pop" role="listbox">
          {found.length === 0 ? <p className="muted">Ничего не нашлось</p> : found.map((h, i) => (
            <button key={h.id} role="option" aria-selected={i === sel} className={i === sel ? 'sel' : ''} onMouseDown={(e) => e.preventDefault()} onClick={() => go(h)}>
              <span className={`dot s-${h.status}`} aria-hidden="true" />{h.name}<small>{h.inventory?.ips?.[0] || h.meta?.node || ''}</small>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
