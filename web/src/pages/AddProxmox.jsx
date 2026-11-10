import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago, plural } from '../format.js'

export function AddTabs({ current }) {
  const tab = (to, label, key) => (
    <Link to={to} className={current === key ? 'tab active' : 'tab'} aria-current={current === key ? 'page' : undefined}>{label}</Link>
  )
  return (
    <nav className="tabs" aria-label="Что подключить">
      {tab('/add', 'Сервер с агентом', 'server')}
      {tab('/add/proxmox', 'Proxmox', 'pve')}
    </nav>
  )
}

const tokenCmd = `pveum user add sentra@pve --comment "Sentra monitoring"
pveum aclmod / -user sentra@pve -role PVEAuditor
pveum user token add sentra@pve monitor --privsep 0`

function CopyBlock({ text }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try { await navigator.clipboard.writeText(text) } catch { return }
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  return (
    <div className="cmd">
      <code style={{ whiteSpace: 'pre-wrap' }}>{text}</code>
      <button className="btn primary" type="button" onClick={copy}>{copied ? 'Скопировано' : 'Копировать'}</button>
    </div>
  )
}

function summaryText(s) {
  if (!s) return ''
  const n = s.nodes || 0, q = s.qemu || 0, l = s.lxc || 0
  return `${n} ${plural(n, 'узел', 'узла', 'узлов')}, ${q} ВМ, ${l} ${plural(l, 'контейнер', 'контейнера', 'контейнеров')}, запущено ${s.running || 0}`
}

function Sources({ reloadKey }) {
  const [list, setList] = useState(null)
  const [busy, setBusy] = useState(null)
  const [msg, setMsg] = useState(null)
  const load = () => api.pveList().then(setList).catch((e) => setMsg({ tone: 'critical', text: e.message }))
  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t) }, [reloadKey])

  async function remove(p) {
    if (!confirm(`Отключить «${p.name}»? Все его узлы и машины пропадут из панели. История графиков останется в хранилище.`)) return
    setBusy(p.id)
    try { await api.pveDelete(p.id); load() } catch (e) { setMsg({ tone: 'critical', text: e.message }) } finally { setBusy(null) }
  }
  async function trust(p) {
    if (!confirm('Принять новый сертификат Proxmox? Делайте это, только если вы сами перевыпустили сертификат или переустановили узел.')) return
    setBusy(p.id)
    try {
      const r = await api.pveTrust(p.id)
      setMsg({ tone: 'ok', text: `Новый сертификат принят. Отпечаток ${r.fingerprint.slice(0, 23)}…` })
      load()
    } catch (e) { setMsg({ tone: 'critical', text: e.message }) } finally { setBusy(null) }
  }

  if (!list || list.length === 0) return msg ? <p className={`banner ${msg.tone}`}>{msg.text}</p> : null
  return (
    <section className="sources" aria-labelledby="src-h">
      <h2 id="src-h">Подключённые Proxmox</h2>
      {msg && <p className={`banner ${msg.tone}`}>{msg.text}</p>}
      <ul className="src-list">
        {list.map((p) => {
          const failing = !!p.last_error
          return (
            <li key={p.id} className={failing ? 'src failing' : 'src'}>
              <div className="src-main">
                <div className="src-name">{p.name}</div>
                <div className="muted small">{p.url}, токен {p.token_id}</div>
                {failing
                  ? <p className="src-err">{p.last_error}</p>
                  : <p className="small">{p.last_ok ? `${summaryText(p.summary)}. Опрошен ${ago(p.last_ok)}.` : 'Первый опрос…'}</p>}
              </div>
              <div className="src-actions">
                {failing && p.last_error.includes('сертификат') && (
                  <button className="btn" disabled={busy === p.id} onClick={() => trust(p)}>Принять новый сертификат</button>
                )}
                <button className="btn danger-btn" disabled={busy === p.id} onClick={() => remove(p)}>Отключить</button>
              </div>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

export default function AddProxmox() {
  const [f, setF] = useState({ name: '', url: '', token_id: 'sentra@pve!monitor', secret: '', fingerprint: '' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)
  const [done, setDone] = useState(null)
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value })

  async function submit(e) {
    e.preventDefault()
    setBusy(true); setError(null); setDone(null)
    try {
      const r = await api.pveAdd({ name: f.name, url: f.url, tokenID: f.token_id, secret: f.secret, fingerprint: f.fingerprint })
      setDone(r)
      setF({ ...f, name: '', url: '', secret: '', fingerprint: '' })
    } catch (err) { setError(err.message) } finally { setBusy(false) }
  }

  return (
    <div className="narrow">
      <h1 className="headline">Подключить Proxmox</h1>
      <p className="lead">Sentra читает данные через API Proxmox, на узлы и в машины ничего ставить не нужно. Появятся все узлы кластера, виртуальные машины и контейнеры, новые машины добавятся сами.</p>
      <AddTabs current="pve" />

      <ol className="steps">
        <li>
          <h2>Создайте токен только для чтения</h2>
          <p>В консоли любого узла (Shell в веб-интерфейсе или SSH под root):</p>
          <CopyBlock text={tokenCmd} />
          <p className="muted small">Последняя команда покажет <b>full-tokenid</b> и <b>value</b>. Value показывается один раз: скопируйте его сразу. Роль PVEAuditor даёт только просмотр, менять машины этим токеном нельзя.</p>
        </li>
        <li>
          <h2>Укажите адрес и токен</h2>
          <form className="form wide-form" onSubmit={submit}>
            <label>Адрес любого узла
              <input value={f.url} onChange={set('url')} placeholder="192.168.1.20 или https://pve.local:8006" required autoComplete="off" />
            </label>
            <label>ID токена (full-tokenid)
              <input value={f.token_id} onChange={set('token_id')} required autoComplete="off" spellCheck="false" />
            </label>
            <label>Секрет токена (value)
              <input value={f.secret} onChange={set('secret')} type="password" placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" required autoComplete="off" />
            </label>
            <label><span>Название в панели <span className="muted">(необязательно)</span></span>
              <input value={f.name} onChange={set('name')} placeholder="Например, Кластер в серверной" maxLength={100} />
            </label>
            <details className="help">
              <summary>Отпечаток сертификата</summary>
              <p className="small">Если оставить пустым, Sentra запомнит сертификат, который покажет Proxmox при подключении, и дальше будет доверять только ему. Чтобы сверить заранее, вставьте отпечаток SHA-256 из Proxmox: Узел › Система › Сертификаты › pve-ssl.pem.</p>
              <label>Отпечаток SHA-256
                <input value={f.fingerprint} onChange={set('fingerprint')} placeholder="AB:CD:…" spellCheck="false" autoComplete="off" />
              </label>
            </details>
            {error && <p className="banner critical">{error}</p>}
            {done && (
              <p className="banner ok">
                Подключено: Proxmox {done.version}, {done.nodes} {plural(done.nodes, 'узел', 'узла', 'узлов')}, {done.qemu} ВМ, {done.lxc} {plural(done.lxc, 'контейнер', 'контейнера', 'контейнеров')}. Через минуту они появятся на <Link to="/">обзоре</Link>.
              </p>
            )}
            <div><button className="btn primary" disabled={busy} type="submit">{busy ? 'Проверяем подключение…' : 'Подключить'}</button></div>
          </form>
        </li>
      </ol>

      <Sources reloadKey={done?.id} />

      <details className="help">
        <summary>Если что-то пошло не так</summary>
        <ul>
          <li><b>Proxmox не отвечает</b>: коробка не видит порт 8006 узла. Проверьте маршрут и межсетевой экран.</li>
          <li><b>Не принял токен</b>: проверьте ID в формате <code>sentra@pve!monitor</code> и секрет. Если токен создан без <code>--privsep 0</code>, выдайте роль самому токену: <code>pveum aclmod / -token 'sentra@pve!monitor' -role PVEAuditor</code>.</li>
          <li><b>Сертификат сменился</b>: так бывает после переустановки узла или обновления сертификата. Если вы это делали, нажмите «Принять новый сертификат». Если нет, проверьте, что в сети нет подмены.</li>
          <li>В кластере достаточно подключить один узел: Sentra увидит все.</li>
        </ul>
      </details>
    </div>
  )
}
