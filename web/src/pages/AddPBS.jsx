import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago } from '../format.js'
import { AddTabs, CopyBlock } from './AddProxmox.jsx'

const initial = { url: '', name: '', datastore: '', namespace: '', token_id: '', secret: '', fingerprint: '', warn_hours: '36' }

function TokenInstructions({ datastore }) {
  const [method, setMethod] = useState('web')
  const name = datastore.trim()
  const validName = /^[A-Za-z0-9_][A-Za-z0-9_.-]*$/.test(name)
  const path = `/datastore/${validName ? name : 'backup'}`
  const commands = [
    'proxmox-backup-manager user create sentra@pbs --comment "Sentra monitoring"',
    'proxmox-backup-manager user generate-token sentra@pbs monitor',
    `proxmox-backup-manager acl update '${path}' DatastoreAudit --auth-id sentra@pbs --propagate true`,
    `proxmox-backup-manager acl update '${path}' DatastoreAudit --auth-id 'sentra@pbs!monitor' --propagate true`,
  ].join('\n')

  return (
    <>
      <h2>Создайте доступ только для чтения в PBS</h2>
      <p>Sentra нужен отдельный пользователь и API-токен с ролью <code>DatastoreAudit</code> на выбранном хранилище.</p>
      <div className="toolbar" role="group" aria-label="Способ настройки доступа PBS">
        <button type="button" className={method === 'web' ? 'seg active' : 'seg'} aria-pressed={method === 'web'} onClick={() => setMethod('web')}>В веб-интерфейсе</button>
        <button type="button" className={method === 'console' ? 'seg active' : 'seg'} aria-pressed={method === 'console'} onClick={() => setMethod('console')}>В консоли</button>
      </div>
      {!validName && <p>Укажите имя хранилища в поле <b>Datastore</b>, чтобы подставить его в путь прав. Пока показан пример <code>backup</code>.</p>}
      {method === 'web' ? (
        <ol className="howto">
          <li>Откройте <b>Configuration → Access Control</b> на PBS. В разделе пользователей создайте <code>sentra@pbs</code>.</li>
          <li>В разделе API-токенов создайте токен <code>monitor</code> для <code>sentra@pbs</code>. Сохраните ID токена и секрет.</li>
          <li>В <b>Permissions → Add</b> добавьте <b>User Permission</b> для <code>sentra@pbs</code> и <b>API Token Permission</b> для <code>sentra@pbs!monitor</code>. Для обоих укажите путь <code>{path}</code>, роль <code>DatastoreAudit</code> и оставьте <b>Propagate</b> включённым.</li>
        </ol>
      ) : (
        <>
          <p>Откройте консоль самого сервера PBS и выполните команды от <code>root</code>.</p>
          <CopyBlock key={commands} text={commands} />
          <p>Команда <code>generate-token</code> выводит <code>tokenid</code> и <code>value</code>. В форму Sentra вставьте их как ID и секрет токена.</p>
          <p>Если пользователь уже есть, пропустите его создание. Если уже есть токен, используйте его ID и сохранённый секрет.</p>
        </>
      )}
      <p className="muted small">Секрет показывается только при создании токена.</p>
    </>
  )
}

export default function AddPBS({ inDialog = false }) {
  const [form, setForm] = useState(initial)
  const [sources, setSources] = useState([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(null)
  const [listError, setListError] = useState('')
  const set = (field) => (event) => setForm((value) => ({ ...value, [field]: event.target.value }))

  useEffect(() => {
    if (inDialog) return
    let active = true
    const load = () => api.pbsList()
      .then((list) => { if (active) { setSources(list); setListError('') } })
      .catch((err) => { if (active) setListError(err.message) })
    load()
    const timer = setInterval(load, 15000)
    return () => { active = false; clearInterval(timer) }
  }, [inDialog])

  async function submit(event) {
    event.preventDefault()
    setBusy(true)
    setError('')
    setDone(null)
    try {
      const source = await api.pbsAdd({ ...form, warn_hours: Number(form.warn_hours) })
      setSources((list) => [...list.filter((p) => p.id !== source.id), source])
      setDone(source)
      setForm(initial)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  const optionalFields = (
    <>
      <label>Название в Sentra <input value={form.name} onChange={set('name')} placeholder="По умолчанию — имя datastore" maxLength={100} /></label>
      <label>Предупреждать, если копия старше (ч) <input type="number" value={form.warn_hours} onChange={set('warn_hours')} min={1} max={8760} required /></label>
      <label>Отпечаток SHA-256 для самоподписанного сертификата <input value={form.fingerprint} onChange={set('fingerprint')} placeholder="AB:CD:…" autoComplete="off" spellCheck={false} /></label>
      <p className="muted small">Отпечаток можно посмотреть на PBS командой <code>proxmox-backup-manager cert info</code>.</p>
    </>
  )

  return (
    <div className="narrow">
      {!inDialog && (
        <>
          <h1 className="headline">Подключить Proxmox Backup Server</h1>
          <p className="lead">Sentra проверяет свободное место и возраст последней завершённой копии каждой группы.</p>
          <AddTabs current="pbs" />
        </>
      )}
      <ol className="steps equipment-setup-columns">
        <li>
          <TokenInstructions datastore={form.datastore} />
        </li>
        <li>
          <h2>Настройте мониторинг хранилища</h2>
          <form className="form wide-form" onSubmit={submit}>
            <label>Адрес PBS <input value={form.url} onChange={set('url')} placeholder="pbs.example.com:8007" required autoComplete="off" /></label>
            <label>Datastore <input value={form.datastore} onChange={set('datastore')} required placeholder="backup" autoComplete="off" /></label>
            <label>Пространство имён <input value={form.namespace} onChange={set('namespace')} placeholder="Пустое поле — корневое пространство" autoComplete="off" /></label>
            <label>ID токена <input value={form.token_id} onChange={set('token_id')} placeholder="sentra@pbs!monitor" required autoComplete="off" /></label>
            <label>Секрет токена <input type="password" value={form.secret} onChange={set('secret')} required autoComplete="new-password" /></label>
            {inDialog ? (
              <details className="help" onInvalid={(event) => { event.currentTarget.open = true }}>
                <summary>Дополнительные параметры и сертификат</summary>
                <div className="equipment-optional-fields">{optionalFields}</div>
              </details>
            ) : optionalFields}
            {error && <p className="banner critical" role="alert">{error}</p>}
            {inDialog && done && <p className="banner ok" role="status">Хранилище подключено. <Link to={`/hosts/${done.id}`}>Открыть {done.name}</Link></p>}
            <div><button className="btn primary" disabled={busy}>{busy ? 'Проверяем подключение…' : 'Подключить'}</button></div>
          </form>
        </li>
      </ol>

      {!inDialog && (sources.length > 0 || listError) && <section className="sources" aria-labelledby="pbs-src">
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
