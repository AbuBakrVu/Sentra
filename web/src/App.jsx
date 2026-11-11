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

  return (
    <div className="shell">
      <TopNav path={path} user={user} logout={logout} />
      <main className="main">{page}</main>
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

const sections = [
  ['/', 'grid', 'Обзор'],
  ['/problems', 'alert', 'Проблемы'],
  ['/add', 'server', 'Сервер с агентом'],
  ['/add/proxmox', 'layers', 'Proxmox'],
  ['/settings', 'gear', 'Настройки'],
]

// Верхнее меню: активный раздел подписан, остальные — иконки с подсказкой.
function TopNav({ path, user, logout }) {
  const open = useOpenCount()
  const [menu, setMenu] = useState(false)
  const isOn = (to) => (to === '/' ? path === '/' || path.startsWith('/hosts/') : path === to)
  useEffect(() => {
    if (!menu) return
    const close = (e) => { if (!e.target.closest?.('.me')) setMenu(false) }
    const esc = (e) => e.key === 'Escape' && setMenu(false)
    document.addEventListener('click', close)
    document.addEventListener('keydown', esc)
    return () => { document.removeEventListener('click', close); document.removeEventListener('keydown', esc) }
  }, [menu])

  return (
    <header className="topnav">
      <Link to="/" className="brand" aria-label="Sentra, обзор">
        <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden="true"><circle cx="16" cy="16" r="11" fill="none" stroke="currentColor" strokeWidth="3.5" /><circle cx="16" cy="16" r="4" fill="currentColor" /></svg>
        Sentra
      </Link>
      <nav className="pillnav" aria-label="Разделы">
        {sections.map(([to, icon, label]) => {
          const on = isOn(to)
          return (
            <Link key={to} to={to} className={on ? 'pn active' : 'pn'} aria-current={on ? 'page' : undefined}
              aria-label={on ? undefined : label} title={on ? undefined : label}>
              <Icon name={icon} size={19} />{on && <span>{label}</span>}
              {to === '/problems' && open > 0 && <i className="pn-count">{open}</i>}
            </Link>
          )
        })}
      </nav>
      <div className="tn-right">
        <HostSearch />
        <Link to="/problems" className="icon-btn bell" aria-label={open ? `Проблемы: ${open}` : 'Проблем нет'} title={open ? `Открытых проблем: ${open}` : 'Проблем нет'}>
          <Icon name="bell" size={19} />{open > 0 && <i />}
        </Link>
        <div className="me">
          <button className="avatar" aria-haspopup="menu" aria-expanded={menu} aria-label={`Пользователь ${user.login}`} onClick={() => setMenu((v) => !v)}>
            {user.login.slice(0, 1).toUpperCase()}
          </button>
          {menu && (
            <div className="me-pop" role="menu">
              <p><b>{user.login}</b><small>Администратор</small></p>
              <Link to="/settings" role="menuitem" onClick={() => setMenu(false)}><Icon name="gear" size={18} />Настройки</Link>
              <button role="menuitem" onClick={logout}><Icon name="logout" size={18} />Выйти</button>
            </div>
          )}
        </div>
      </div>
    </header>
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
      <input id="host-search" ref={ref} value={q} placeholder="Поиск устройства…" autoComplete="off"
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
