import { useState } from 'react'
import { Link, navigate } from '../App.jsx'
import { api } from '../api.js'

export default function AddIP({ inDialog }) {
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event) {
    event.preventDefault()
    if (busy) return
    const data = new FormData(event.currentTarget)
    setBusy(true)
    setError('')
    try {
      const host = await api.addIP(data.get('addr').trim(), data.get('name').trim())
      navigate('/hosts/' + host.id)
    } catch (err) {
      setError(err.message)
      setBusy(false)
    }
  }

  return <div className="narrow">
    {!inDialog && <><Link className="back" to="/">Все устройства</Link><h1 className="headline">Добавить по IP-адресу</h1></>}
    <p className="lead">Sentra проверяет доступность устройства по ping каждые 30 секунд.</p>
    <form className="form wide-form" onSubmit={submit}>
      <label>IPv4-адрес<input name="addr" required placeholder="192.168.1.10" autoComplete="off" spellCheck={false} /></label>
      <label>Название в Sentra<input name="name" placeholder="По умолчанию — IP-адрес" maxLength={100} /></label>
      {error && <p className="banner critical" role="alert">{error}</p>}
      <button className="btn primary" type="submit" disabled={busy}>{busy ? 'Добавляем…' : 'Добавить устройство'}</button>
    </form>
  </div>
}
