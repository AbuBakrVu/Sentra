import { useState } from 'react'
import { api } from '../api.js'

export default function Login({ onLogin }) {
  const [login, setLogin] = useState('admin')
  const [password, setPassword] = useState('')
  const [error, setError] = useState(null)
  const [busy, setBusy] = useState(false)

  async function submit(e) {
    e.preventDefault()
    setBusy(true); setError(null)
    try { onLogin(await api.login(login, password)) } catch (err) { setError(err.message) } finally { setBusy(false) }
  }

  return (
    <div className="login">
      <form className="login-card form" onSubmit={submit}>
        <div className="brand big">
          <svg width="30" height="30" viewBox="0 0 32 32" aria-hidden="true"><circle cx="16" cy="16" r="11" fill="none" stroke="currentColor" strokeWidth="3" /><circle cx="16" cy="16" r="4" fill="currentColor" /></svg>
          Дозор
        </div>
        <p className="muted">Мониторинг серверов и сети</p>
        <label>Логин<input value={login} onChange={(e) => setLogin(e.target.value)} autoComplete="username" required /></label>
        <label>Пароль<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus required /></label>
        {error && <p className="banner critical">{error}</p>}
        <button className="btn primary wide" disabled={busy} type="submit">{busy ? 'Входим…' : 'Войти'}</button>
        <p className="muted small">Пароль первого входа сервер записал в журнал и в файл <code>initial-admin-password.txt</code> в каталоге данных.</p>
      </form>
    </div>
  )
}
