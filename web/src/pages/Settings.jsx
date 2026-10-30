import { useState } from 'react'
import { api } from '../api.js'

export default function Settings() {
  const [old, setOld] = useState('')
  const [nw, setNw] = useState('')
  const [nw2, setNw2] = useState('')
  const [msg, setMsg] = useState(null)

  async function submit(e) {
    e.preventDefault()
    if (nw !== nw2) return setMsg({ tone: 'critical', text: 'Новые пароли не совпадают.' })
    try {
      await api.password(old, nw)
      setMsg({ tone: 'ok', text: 'Пароль изменён. На других устройствах нужно войти заново.' })
      setOld(''); setNw(''); setNw2('')
    } catch (err) { setMsg({ tone: 'critical', text: err.message }) }
  }

  return (
    <div className="narrow">
      <h1 className="headline">Настройки</h1>
      <form className="form" onSubmit={submit}>
        <h2>Сменить пароль</h2>
        <label>Текущий пароль<input type="password" autoComplete="current-password" value={old} onChange={(e) => setOld(e.target.value)} required /></label>
        <label>Новый пароль<input type="password" autoComplete="new-password" minLength={8} value={nw} onChange={(e) => setNw(e.target.value)} required /></label>
        <label>Новый пароль ещё раз<input type="password" autoComplete="new-password" minLength={8} value={nw2} onChange={(e) => setNw2(e.target.value)} required /></label>
        {msg && <p className={`banner ${msg.tone}`}>{msg.text}</p>}
        <button className="btn primary" type="submit">Сменить пароль</button>
      </form>
    </div>
  )
}
