import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago } from '../format.js'
import { AddTabs } from './AddProxmox.jsx'

const initial = { url: '', name: '', datastore: '', namespace: '', token_id: '', secret: '', fingerprint: '', warn_hours: '36' }

export default function AddPBS() {
  const [form, setForm] = useState(initial)
  const [sources, setSources] = useState([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [listError, setListError] = useState('')
  const set = (field) => (event) => setForm((value) => ({ ...value, [field]: event.target.value }))

  useEffect(() => {
    let active = true
    const load = () => api.pbsList()
      .then((list) => { if (active) { setSources(list); setListError('') } })
      .catch((err) => { if (active) setListError(err.message) })
    load()
    const timer = setInterval(load, 15000)
    return () => { active = false; clearInterval(timer) }
  }, [])

  async function submit(event) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      const source = await api.pbsAdd({ ...form, warn_hours: Number(form.warn_hours) })
      setSources((list) => [...list.filter((p) => p.id !== source.id), source])
      setForm(initial)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="narrow">
      <h1 className="headline">Подключить Proxmox Backup Server</h1>
      <p className="lead">Sentra проверяет свободное место и возраст последней завершённой копии каждой группы.</p>
      <AddTabs current="pbs" />
      <ol className="steps">
        <li>
          <h2>Создайте токен в PBS</h2>
          <p>В <b>Configuration → Access Control</b> создайте отдельного пользователя и API-токен. Назначьте обоим роль <code>DatastoreAudit</code> на <code>/datastore/имя</code> с наследованием прав.</p>
        </li>
        <li>
          <h2>Настройте мониторинг хранилища</h2>
          <form className="form wide-form" onSubmit={submit}>
            <label>Адрес PBS <input value={form.url} onChange={set('url')} placeholder="pbs.example.com:8007" required autoComplete="off" /></label>
            <label>Datastore <input value={form.datastore} onChange={set('datastore')} required placeholder="backup" autoComplete="off" /></label>
            <label>Пространство имён <input value={form.namespace} onChange={set('namespace')} placeholder="Пустое поле — корневое пространство" autoComplete="off" /></label>
            <label>ID токена <input value={form.token_id} onChange={set('token_id')} placeholder="sentra@pbs!monitor" required autoComplete="off" /></label>
            <label>Секрет токена <input type="password" value={form.secret} onChange={set('secret')} required autoComplete="new-password" /></label>
            <label>Название в Sentra <input value={form.name} onChange={set('name')} placeholder="По умолчанию — имя datastore" maxLength={100} /></label>
            <label>Предупреждать, если копия старше (ч) <input type="number" value={form.warn_hours} onChange={set('warn_hours')} min={1} max={8760} required /></label>
            <label>Отпечаток SHA-256 для самоподписанного сертификата <input value={form.fingerprint} onChange={set('fingerprint')} placeholder="AB:CD:…" autoComplete="off" spellCheck={false} /></label>
            <p className="muted small">Отпечаток можно посмотреть на PBS командой <code>proxmox-backup-manager cert info</code>.</p>
            {error && <p className="banner critical" role="alert">{error}</p>}
            <div><button className="btn primary" disabled={busy}>{busy ? 'Проверяем подключение…' : 'Подключить'}</button></div>
          </form>
        </li>
      </ol>

      {(sources.length > 0 || listError) && <section className="sources" aria-labelledby="pbs-src">
        <h2 id="pbs-src">Подключённые хранилища</h2>
        {listError && <p className="banner critical" role="alert">{listError}</p>}
        <ul className="src-list">
          {sources.map((source) => (
            <li key={source.id} className={source.last_error ? 'src failing' : 'src'}>
              <div className="src-main">
                <div className="src-name"><Link to={`/hosts/${source.id}`}>{source.name}</Link></div>
                <div className="muted small">{source.url} · {source.datastore} · {source.namespace || 'корневое пространство'}</div>
                {source.last_error ? <p className="src-err">{source.last_error}</p> : <p className="small">Опрошен {ago(source.last_seen)}.</p>}
              </div>
            </li>
          ))}
        </ul>
      </section>}
    </div>
  )
}
