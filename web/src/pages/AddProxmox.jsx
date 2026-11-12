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

const cmds = [
  {
    cmd: 'pveum user add sentra@pve --comment "Sentra monitoring"',
    what: 'Создаёт отдельного пользователя sentra во встроенной базе пользователей Proxmox (часть @pve).',
    why: 'Чтобы не отдавать Sentra учётную запись root. Отдельного пользователя легко узнать в журнале и удалить, если мониторинг больше не нужен.',
  },
  {
    cmd: 'pveum aclmod / -user sentra@pve -role PVEAuditor',
    what: 'Выдаёт пользователю роль PVEAuditor на путь «/», то есть на весь кластер: все узлы, машины и хранилища.',
    why: 'PVEAuditor — роль только для просмотра. Sentra видит состояние и нагрузку, но не может включить, выключить, удалить машину или поменять настройки.',
  },
  {
    cmd: 'pveum user token add sentra@pve monitor --privsep 0',
    what: 'Создаёт для пользователя API-токен с именем monitor и показывает его ID (full-tokenid) и секрет (value).',
    why: 'Токен — это ключ для программ: Sentra входит по нему, а не по паролю. Флаг --privsep 0 значит, что у токена те же права, что у пользователя, то есть только просмотр. Без флага права пришлось бы выдавать токену отдельной командой.',
  },
]
const tokenCmd = cmds.map((c) => c.cmd).join('\n')

function WebSteps() {
  return (
    <ol className="howto">
      <li>
        <b>Откройте веб-интерфейс Proxmox</b> по адресу <code>https://IP-узла:8006</code> и войдите под root (realm «Linux PAM»).
      </li>
      <li>
        <b>Создайте пользователя.</b> Слева выберите <b>Datacenter</b>, затем <b>Permissions › Users</b> и нажмите <b>Add</b>.
        <ul>
          <li>User name: <code>sentra</code></li>
          <li>Realm: <b>Proxmox VE authentication server</b></li>
          <li>Пароль задайте любой: входить им не придётся</li>
          <li>Comment: <code>Sentra monitoring</code>, нажмите <b>Add</b></li>
        </ul>
        <p className="why">Зачем: отдельный пользователь вместо root. Его легко найти в журнале и удалить, если мониторинг больше не нужен.</p>
      </li>
      <li>
        <b>Дайте права только на просмотр.</b> В <b>Datacenter › Permissions</b> нажмите <b>Add › User Permission</b>.
        <ul>
          <li>Path: <code>/</code> (весь кластер)</li>
          <li>User: <code>sentra@pve</code></li>
          <li>Role: <b>PVEAuditor</b></li>
          <li>Галочка <b>Propagate</b> включена, нажмите <b>Add</b></li>
        </ul>
        <p className="why">Зачем: PVEAuditor видит состояние узлов, машин и хранилищ, но не может ничего включить, выключить или изменить. «Propagate» распространяет права на все узлы и машины внутри кластера.</p>
      </li>
      <li>
        <b>Создайте токен.</b> В <b>Datacenter › Permissions › API Tokens</b> нажмите <b>Add</b>.
        <ul>
          <li>User: <code>sentra@pve</code></li>
          <li>Token ID: <code>monitor</code></li>
          <li>Снимите галочку <b>Privilege Separation</b></li>
          <li>Expire: оставьте <b>never</b>, нажмите <b>Add</b></li>
        </ul>
        <p className="why">Зачем: токен — это ключ для программ, Sentra входит по нему, а не по паролю. Без снятой галочки «Privilege Separation» у токена не будет прав пользователя, и Sentra покажет «у токена нет прав».</p>
      </li>
      <li>
        <b>Скопируйте Token ID и Secret</b> из появившегося окна. Token ID будет <code>sentra@pve!monitor</code>. Secret показывается <b>один раз</b>: если окно закрылось, удалите токен и создайте заново.
      </li>
    </ol>
  )
}

function ConsoleSteps() {
  return (
    <>
      <p>Откройте консоль любого узла: <b>Shell</b> в веб-интерфейсе Proxmox или SSH под root. Выполните три команды по очереди или все сразу:</p>
      <CopyBlock text={tokenCmd} />
      <ol className="howto cmds">
        {cmds.map((c) => (
          <li key={c.cmd}>
            <code className="cmd-line">{c.cmd}</code>
            <p>{c.what}</p>
            <p className="why">Зачем: {c.why}</p>
          </li>
        ))}
      </ol>
      <p className="muted small">Последняя команда выведет таблицу: <b>full-tokenid</b> — это ID токена (<code>sentra@pve!monitor</code>), <b>value</b> — секрет. Секрет показывается один раз, скопируйте его сразу.</p>
    </>
  )
}

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
  const [how, setHow] = useState('web')
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
          <h2>Создайте в Proxmox доступ только для чтения</h2>
          <p>Sentra нужен пользователь с правом только смотреть и ключ доступа (API-токен) для него. Так Sentra видит узлы, машины и хранилища, но ничего не может в них изменить. Это можно сделать мышкой в веб-интерфейсе Proxmox или тремя командами в консоли, результат одинаковый.</p>
          <div className="toolbar" role="group" aria-label="Как создать доступ">
            <button type="button" className={how === 'web' ? 'seg active' : 'seg'} onClick={() => setHow('web')}>В веб-интерфейсе</button>
            <button type="button" className={how === 'console' ? 'seg active' : 'seg'} onClick={() => setHow('console')}>В консоли</button>
          </div>
          {how === 'web' ? <WebSteps /> : <ConsoleSteps />}
          <details className="help">
            <summary>Необязательно: диски внутри виртуальных машин</summary>
            <p>Proxmox не знает, насколько заполнен диск внутри ВМ: это знает только её система. Sentra спрашивает об этом гостевой агент QEMU. Для этого токену нужно ещё одно право, а в машине должен работать агент.</p>
            <ul>
              <li><b>Proxmox 9:</b> право <code>VM.GuestAgent.Audit</code>, только чтение сведений от агента.
                <CopyBlock text={'pveum role add SentraGuest -privs VM.GuestAgent.Audit\npveum aclmod /vms -user sentra@pve -role SentraGuest'} /></li>
              <li><b>Proxmox 8:</b> отдельного права на чтение нет, нужно <code>VM.Monitor</code>. Учтите, что оно открывает и монитор QEMU, то есть больше, чем просмотр.
                <CopyBlock text={'pveum role add SentraGuest -privs VM.Monitor\npveum aclmod /vms -user sentra@pve -role SentraGuest'} /></li>
              <li><b>В машине:</b> Linux — <code>apt install qemu-guest-agent</code>, Windows — VirtIO-драйверы с образа <code>virtio-win</code>. В Proxmox: машина › Options › QEMU Guest Agent › Enabled, затем выключить и включить машину.</li>
            </ul>
            <p>Без этого Sentra покажет у ВМ процессор, память и сеть, а вместо диска — подсказку, чего не хватает. Версию Proxmox показывает команда <code>pveversion</code>.</p>
          </details>
          <details className="help">
            <summary>Что Sentra делает с этим доступом и как его отозвать</summary>
            <ul>
              <li>Раз в 30 секунд Sentra читает список узлов, машин и хранилищ и их нагрузку. Если выдано право на гостевой агент, раз в 5 минут спрашивает у него заполнение дисков ВМ. Больше ничего: команд Proxmox она не отправляет.</li>
              <li>Секрет токена хранится только в базе этой коробки.</li>
              <li>Отозвать доступ: в Proxmox <b>Datacenter › Permissions › API Tokens</b> удалите токен <code>monitor</code>, или в консоли выполните <code>pveum user token remove sentra@pve monitor</code>. Чтобы удалить и пользователя: <code>pveum user delete sentra@pve</code>.</li>
            </ul>
          </details>
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
