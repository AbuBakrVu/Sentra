import { useState } from 'react'
import { api } from '../api.js'

export default function Users() {
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [created, setCreated] = useState('')

  async function submit(event) {
    event.preventDefault()
    setCreated('')
    if (password !== confirmation) {
      setError('Пароли не совпадают.')
      return
    }
    setError('')
    setBusy(true)
    try {
      const user = await api.createUser(login.trim(), password)
      setCreated(user.login)
      setLogin('')
      setPassword('')
      setConfirmation('')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="narrow">
      <h1 className="headline">Пользователи</h1>
      <form className="form" onSubmit={submit}>
        <h2>Создать пользователя</h2>
        <p className="muted">Новая учётная запись получит доступ к просмотру мониторинга.</p>
        <label>Логин<input value={login} onChange={(event) => setLogin(event.target.value)} maxLength={64} autoComplete="off" spellCheck={false} required /></label>
        <label>Пароль
          <input type="password" value={password} onChange={(event) => setPassword(event.target.value)} minLength={8} maxLength={128} autoComplete="new-password" aria-describedby="new-user-password-hint" required />
          <span className="hint" id="new-user-password-hint">От 8 до 128 символов</span>
        </label>
        <label>Повторите пароль<input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} maxLength={128} autoComplete="new-password" required /></label>
        {error && <p className="banner critical" role="alert">{error}</p>}
        {created && <p className="banner ok" role="status">Пользователь <b>{created}</b> создан и может войти в Sentra.</p>}
        <div><button className="btn primary" type="submit" disabled={busy}>{busy ? 'Создаём…' : 'Создать пользователя'}</button></div>
      </form>
    </div>
  )
}
