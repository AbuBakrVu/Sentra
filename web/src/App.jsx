import { useCallback, useEffect, useState } from 'react'
import { api } from './api.js'
import Overview from './pages/Overview.jsx'
import HostPage from './pages/HostPage.jsx'
import AddServer from './pages/AddServer.jsx'
import Settings from './pages/Settings.jsx'
import Login from './pages/Login.jsx'

// Минимальный роутер на History API: нам хватает четырёх экранов.
export function navigate(to) {
  window.history.pushState({}, '', to)
  window.dispatchEvent(new Event('dozor:nav'))
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
    window.addEventListener('dozor:nav', on)
    return () => { window.removeEventListener('popstate', on); window.removeEventListener('dozor:nav', on) }
  }, [])
  return path
}

export default function App() {
  const [user, setUser] = useState(undefined) // undefined — проверяем сессию
  const path = usePath()

  useEffect(() => {
    api.me().then(setUser).catch(() => setUser(null))
    const out = () => setUser(null)
    window.addEventListener('dozor:logout', out)
    return () => window.removeEventListener('dozor:logout', out)
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
  else if (path === '/settings') page = <Settings />
  else page = <Overview />

  const tab = (to, label) => (
    <Link to={to} className={path === to ? 'nav-link active' : 'nav-link'} aria-current={path === to ? 'page' : undefined}>{label}</Link>
  )

  return (
    <div className="shell">
      <header className="topbar">
        <div className="topbar-inner">
          <Link to="/" className="brand" aria-label="Дозор, обзор">
            <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden="true"><circle cx="16" cy="16" r="11" fill="none" stroke="currentColor" strokeWidth="3" /><circle cx="16" cy="16" r="4" fill="currentColor" /></svg>
            Дозор
          </Link>
          <nav className="nav">
            {tab('/', 'Обзор')}
            {tab('/add', 'Добавить сервер')}
            {tab('/settings', 'Настройки')}
          </nav>
          <div className="who">
            <span>{user.login}</span>
            <button className="link-btn" onClick={logout}>Выйти</button>
          </div>
        </div>
      </header>
      <main className="main">{page}</main>
    </div>
  )
}
