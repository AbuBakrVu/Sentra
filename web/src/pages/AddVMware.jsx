import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago, plural } from '../format.js'
import { AddTabs } from './AddProxmox.jsx'

function VCenterSteps() {
  return (
    <ol className="howto">
      <li>
        <b>Откройте vSphere Client</b> по адресу <code>https://адрес-vcenter/ui</code> и войдите как <code>administrator@vsphere.local</code>.
      </li>
      <li>
        <b>Создайте пользователя.</b> Меню <b>Administration › Single Sign On › Users and Groups</b>, вкладка <b>Users</b>, домен <b>vsphere.local</b>, кнопка <b>Add</b>.
        <ul>
          <li>Username: <code>sentra</code></li>
          <li>Пароль: длинный и случайный, его вы вставите ниже</li>
          <li>Description: <code>Sentra monitoring</code>, нажмите <b>Add</b></li>
        </ul>
        <p className="why">Зачем: отдельный пользователь вместо administrator. Его легко найти в журнале событий и удалить, если мониторинг больше не нужен.</p>
      </li>
      <li>
        <b>Дайте права только на просмотр.</b> В дереве слева выберите самый верхний объект — сам vCenter. Вкладка <b>Permissions</b>, кнопка <b>Add</b> (+).
        <ul>
          <li>Domain: <b>vsphere.local</b>, User: <code>sentra</code></li>
          <li>Role: <b>Read-only</b></li>
          <li>Галочка <b>Propagate to children</b> включена, нажмите <b>OK</b></li>
        </ul>
        <p className="why">Зачем: Read-only видит хосты, машины и хранилища, но не может ничего включить, выключить или изменить. «Propagate to children» распространяет права вниз по дереву: без неё Sentra увидит пустой vCenter.</p>
      </li>
    </ol>
  )
}

function ESXiSteps() {
  return (
    <ol className="howto">
      <li>
        <b>Откройте ESXi Host Client</b> по адресу <code>https://IP-хоста/ui</code> и войдите как <code>root</code>.
      </li>
      <li>
        <b>Создайте пользователя.</b> <b>Manage › Security &amp; users › Users</b>, кнопка <b>Add user</b>.
        <ul>
          <li>User name: <code>sentra</code></li>
          <li>Пароль: длинный и случайный, его вы вставите ниже</li>
        </ul>
        <p className="why">Зачем: отдельный пользователь вместо root. У root есть права на всё, а Sentra нужно только смотреть.</p>
      </li>
      <li>
        <b>Дайте права только на просмотр.</b> В навигаторе слева правой кнопкой по <b>Host</b> › <b>Permissions</b>, кнопка <b>Add user</b>.
        <ul>
          <li>User: <code>sentra</code></li>
          <li>Role: <b>Read-only</b></li>
          <li>Галочка <b>Propagate to all children</b> включена, нажмите <b>Add user</b></li>
        </ul>
        <p className="why">Зачем: Read-only видит хост, машины и хранилища, но ничего не может изменить. Бесплатной лицензии ESXi это не мешает: она запрещает через API только изменения.</p>
      </li>
    </ol>
  )
}

function summaryText(s) {
  if (!s) return ''
  const h = s.hosts || 0, v = s.vms || 0
  return `${h} ${plural(h, 'хост', 'хоста', 'хостов')}, ${v} ВМ, запущено ${s.running || 0}`
}

function Sources({ reloadKey }) {
  const [list, setList] = useState(null)
  const [busy, setBusy] = useState(null)
  const [msg, setMsg] = useState(null)
  const load = () => api.vmwList().then(setList).catch((e) => setMsg({ tone: 'critical', text: e.message }))
  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t) }, [reloadKey])

  async function remove(p) {
    if (!confirm(`Отключить «${p.name}»? Все его хосты и машины пропадут из панели. История графиков останется в хранилище.`)) return
    setBusy(p.id)
    try { await api.vmwDelete(p.id); load() } catch (e) { setMsg({ tone: 'critical', text: e.message }) } finally { setBusy(null) }
  }
  async function trust(p) {
    if (!confirm('Принять новый сертификат VMware? Делайте это, только если вы сами перевыпустили сертификат или переустановили сервер.')) return
    setBusy(p.id)
    try {
      const r = await api.vmwTrust(p.id)
      setMsg({ tone: 'ok', text: `Новый сертификат принят. Отпечаток ${r.fingerprint.slice(0, 23)}…` })
      load()
    } catch (e) { setMsg({ tone: 'critical', text: e.message }) } finally { setBusy(null) }
  }

  if (!list || list.length === 0) return msg ? <p className={`banner ${msg.tone}`}>{msg.text}</p> : null
  return (
    <section className="sources" aria-labelledby="src-h">
      <h2 id="src-h">Подключённые VMware</h2>
      {msg && <p className={`banner ${msg.tone}`}>{msg.text}</p>}
      <ul className="src-list">
        {list.map((p) => {
          const failing = !!p.last_error
          return (
            <li key={p.id} className={failing ? 'src failing' : 'src'}>
              <div className="src-main">
                <div className="src-name">{p.name}</div>
                <div className="muted small">{p.api_type === 'VirtualCenter' ? 'vCenter' : 'ESXi'} {p.url}, пользователь {p.username}</div>
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

export default function AddVMware({ initialTarget = 'vcenter', inDialog = false }) {
  const [f, setF] = useState({ name: '', url: '', username: initialTarget === 'esxi' ? 'sentra' : 'sentra@vsphere.local', password: '', fingerprint: '' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)
  const [done, setDone] = useState(null)
  const [how, setHow] = useState(initialTarget)
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value })
  const pick = (k) => {
    setHow(k)
    // подставляем привычное имя пользователя, пока его не меняли вручную
    if (f.username === 'sentra@vsphere.local' || f.username === 'sentra') setF({ ...f, username: k === 'vcenter' ? 'sentra@vsphere.local' : 'sentra' })
  }

  async function submit(e) {
    e.preventDefault()
    setBusy(true); setError(null); setDone(null)
    try {
      const r = await api.vmwAdd({ name: f.name, url: f.url, username: f.username, password: f.password, fingerprint: f.fingerprint })
      setDone(r)
      setF({ ...f, name: '', url: '', password: '', fingerprint: '' })
    } catch (err) { setError(err.message) } finally { setBusy(false) }
  }

  return (
    <div className="narrow">
      {!inDialog && (
        <>
          <h1 className="headline">Подключить VMware</h1>
          <p className="lead">Sentra читает данные через vSphere API, на хосты и в машины ничего ставить не нужно. Подойдёт vCenter или отдельный хост ESXi. Появятся все хосты, виртуальные машины и хранилища, новые машины добавятся сами.</p>
          <AddTabs current="vmw" />
        </>
      )}

      <ol className="steps equipment-setup-columns">
        <li>
          <h2>Создайте пользователя только для чтения</h2>
          <p>Sentra нужен отдельный пользователь с ролью Read-only. Так Sentra видит хосты, машины и хранилища, но ничего не может в них изменить.</p>
          <div className="toolbar" role="group" aria-label="Что подключаете">
            <button type="button" className={how === 'vcenter' ? 'seg active' : 'seg'} onClick={() => pick('vcenter')}>vCenter</button>
            <button type="button" className={how === 'esxi' ? 'seg active' : 'seg'} onClick={() => pick('esxi')}>Отдельный ESXi</button>
          </div>
          {how === 'vcenter' ? <VCenterSteps /> : <ESXiSteps />}
          <details className="help">
            <summary>Необязательно: диски внутри виртуальных машин</summary>
            <p>Заполнение дисков внутри ВМ знает только её система. VMware узнаёт его через VMware Tools, отдельные права для этого не нужны.</p>
            <ul>
              <li><b>Linux:</b> <code>apt install open-vm-tools</code> (Debian, Ubuntu) или <code>dnf install open-vm-tools</code> (Alma, Rocky, CentOS).</li>
              <li><b>Windows:</b> в vSphere Client машина › <b>Guest OS › Install VMware Tools</b>, затем установщик с подключившегося диска.</li>
            </ul>
            <p>Без VMware Tools Sentra покажет у ВМ процессор и память, а вместо диска — подсказку.</p>
          </details>
          <details className="help">
            <summary>Что Sentra делает с этим доступом и как его отозвать</summary>
            <ul>
              <li>Раз в 30 секунд Sentra читает список хостов, машин и хранилищ и их нагрузку (quick stats). Больше ничего: команд VMware она не отправляет.</li>
              <li>Сессия держится между опросами, поэтому в vCenter не копятся входы. При отключении Sentra закрывает сессию.</li>
              <li>Пароль хранится только в базе этой коробки.</li>
              <li>Отозвать доступ: удалите пользователя <code>sentra</code> или его права на вкладке Permissions.</li>
            </ul>
          </details>
        </li>
        <li>
          <h2>Укажите адрес и пользователя</h2>
          <form className="form wide-form" onSubmit={submit}>
            <label>{how === 'vcenter' ? 'Адрес vCenter' : 'Адрес хоста ESXi'}
              <input value={f.url} onChange={set('url')} placeholder={how === 'vcenter' ? '192.168.1.30 или vcenter.local' : '192.168.1.31'} required autoComplete="off" />
            </label>
            <label>Пользователь
              <input value={f.username} onChange={set('username')} required autoComplete="off" spellCheck="false" />
            </label>
            <label>Пароль
              <input value={f.password} onChange={set('password')} type="password" required autoComplete="new-password" />
            </label>
            <label><span>Название в панели <span className="muted">(необязательно)</span></span>
              <input value={f.name} onChange={set('name')} placeholder="Например, Серверная, ESXi" maxLength={100} />
            </label>
            <details className="help">
              <summary>Отпечаток сертификата</summary>
              <p className="small">Если оставить пустым, Sentra запомнит сертификат, который покажет сервер при подключении, и дальше будет доверять только ему. Чтобы сверить заранее, вставьте отпечаток SHA-256 сертификата: в браузере откройте адрес сервера и посмотрите сведения о сертификате.</p>
              <label>Отпечаток SHA-256
                <input value={f.fingerprint} onChange={set('fingerprint')} placeholder="AB:CD:…" spellCheck="false" autoComplete="off" />
              </label>
            </details>
            {error && <p className="banner critical">{error}</p>}
            {done && (
              <p className="banner ok">
                Подключено: {done.product}, {done.hosts} {plural(done.hosts, 'хост', 'хоста', 'хостов')}, {done.vms} ВМ. Через минуту они появятся на <Link to="/">обзоре</Link>.
              </p>
            )}
            <div><button className="btn primary" disabled={busy} type="submit">{busy ? 'Проверяем подключение…' : 'Подключить'}</button></div>
          </form>
        </li>
      </ol>

      {!inDialog && <Sources reloadKey={done?.id} />}

      <details className="help">
        <summary>Если что-то пошло не так</summary>
        <ul>
          <li><b>VMware не отвечает</b>: коробка не видит порт 443 сервера. Проверьте маршрут и межсетевой экран.</li>
          <li><b>Не принял логин или пароль</b>: в vCenter пользователь пишется с доменом, <code>sentra@vsphere.local</code>, на ESXi — без домена, <code>sentra</code>. Если на ESXi включён Lockdown mode, добавьте <code>sentra</code> в Exception users.</li>
          <li><b>Не видно ни одного хоста или машин</b>: права выданы не на корень или без галочки «Propagate to children».</li>
          <li><b>Сертификат сменился</b>: так бывает после обновления vCenter или перевыпуска сертификата. Если вы это делали, нажмите «Принять новый сертификат». Если нет, проверьте, что в сети нет подмены.</li>
          <li>Хосты под управлением vCenter лучше подключать через vCenter, а не каждый ESXi отдельно: тогда видны кластеры и переезды машин.</li>
        </ul>
      </details>
    </div>
  )
}
