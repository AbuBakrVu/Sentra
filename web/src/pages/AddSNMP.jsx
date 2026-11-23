import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { ago } from '../format.js'
import { AddTabs } from './AddProxmox.jsx'

// Адрес коробки, который нужно добавить в доверенные на устройстве.
const boxAddr = typeof window !== 'undefined' ? window.location.hostname : 'адрес коробки'

function IdecoSteps() {
  return (
    <ol className="howto">
      <li>
        <b>Откройте в веб-интерфейсе Ideco NGFW раздел SNMP</b> и включите переключатель вверху: состояние должно смениться с «Остановлен» на «Работает».
      </li>
      <li>
        <b>Отметьте</b> «Разрешить другим устройствам доступ к Ideco NGFW по SNMP».
      </li>
      <li>
        <b>Версия SNMP: 3.</b> Заполните поля:
        <ul>
          <li>Имя пользователя: <code>sentra</code></li>
          <li>Пароль: не короче 8 символов</li>
          <li>Ключ для шифрования: не короче 8 символов, другой, чем пароль</li>
        </ul>
        <p className="why">Зачем: в третьей версии запросы подписаны паролем и зашифрованы. Во второй версии community «public» передаётся открытым текстом, и любой в сети может его подсмотреть.</p>
      </li>
      <li>
        <b>Доверенные IP-адреса и сети:</b> «Добавить адрес» › <code>{boxAddr}</code> (IP-адрес этой коробки Sentra; если панель открыта по имени, укажите её IP).
        <p className="why">Зачем: Ideco отвечает по SNMP только адресам из этого списка. Без него Sentra не получит ответа.</p>
      </li>
      <li>
        Местоположение и имя узла заполнять необязательно, но они появятся в карточке устройства. Нажмите <b>Сохранить</b>.
      </li>
    </ol>
  )
}

function OtherSteps() {
  return (
    <ul className="howto">
      <li><b>MikroTik:</b> IP › SNMP › Enabled. Для v3: Communities › Add, Security: private, Authentication SHA1, Encryption AES, Addresses: <code>{boxAddr}/32</code>.</li>
      <li><b>Коммутатор или маршрутизатор:</b> включите SNMP-агент, создайте пользователя v3 только на чтение (или community v2c) и разрешите доступ с адреса <code>{boxAddr}</code>.</li>
      <li><b>Linux-сервер</b> лучше подключить <Link to="/add/linux">агентом Sentra</Link>: он собирает больше и не требует настройки snmpd.</li>
    </ul>
  )
}

function Devices({ reloadKey, onEdit }) {
  const [list, setList] = useState(null)
  const [busy, setBusy] = useState(null)
  const [msg, setMsg] = useState(null)
  const load = () => api.snmpList().then(setList).catch((e) => setMsg(e.message))
  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t) }, [reloadKey])

  async function remove(d) {
    if (!confirm(`Удалить «${d.name}» из мониторинга? История графиков останется в хранилище.`)) return
    setBusy(d.id)
    try { await api.snmpDelete(d.id); load() } catch (e) { setMsg(e.message) } finally { setBusy(null) }
  }
  if (!list || list.length === 0) return msg ? <p className="banner critical">{msg}</p> : null
  return (
    <section className="sources" aria-labelledby="snmp-h">
      <h2 id="snmp-h">Устройства по SNMP</h2>
      {msg && <p className="banner critical">{msg}</p>}
      <ul className="src-list">
        {list.map((d) => {
          const failing = !!d.last_error
          const a = d.auth || {}
          const how = a.version === '3' ? `SNMPv3, ${a.user}${a.auth_proto ? `, ${a.auth_proto}` : ''}${a.priv_proto ? `/${a.priv_proto}` : ''}` : 'SNMPv2c'
          return (
            <li key={d.id} className={failing ? 'src failing' : 'src'}>
              <div className="src-main">
                <div className="src-name">{d.name}</div>
                <div className="muted small">{d.addr}, {how}</div>
                {failing ? <p className="src-err">{d.last_error}</p> : <p className="small">{d.last_ok ? `Опрошено ${ago(d.last_ok)}.` : 'Первый опрос…'}</p>}
              </div>
              <div className="src-actions">
                <button className="btn" disabled={busy === d.id} onClick={() => onEdit(d)}>Изменить доступ</button>
                <button className="btn danger-btn" disabled={busy === d.id} onClick={() => remove(d)}>Удалить</button>
              </div>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

const empty = { name: '', addr: '', version: '3', community: '', user: 'sentra', auth_pass: '', priv_pass: '', auth_proto: '', priv_proto: '' }

const models = { mikrotik: 'MikroTik', usergate: 'UserGate', nas: 'NAS' }

export default function AddSNMP({ initialDevice = 'ideco', inDialog = false }) {
  const [f, setF] = useState(empty)
  const [edit, setEdit] = useState(null) // устройство, которому меняем доступ
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)
  const [done, setDone] = useState(null)
  const [how, setHow] = useState(initialDevice === 'ideco' ? 'ideco' : 'other')
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value })

  function startEdit(d) {
    const a = d.auth || {}
    setEdit(d)
    setDone(null); setError(null)
    setF({ ...empty, name: d.name, addr: d.addr, version: a.version, user: a.user || 'sentra', auth_proto: a.auth_proto || '', priv_proto: a.priv_proto || '' })
    document.getElementById('snmp-form')?.scrollIntoView({ behavior: 'smooth' })
  }

  async function submit(e) {
    e.preventDefault()
    setBusy(true); setError(null); setDone(null)
    const body = {
      name: f.name, addr: f.addr, version: f.version, community: f.community, user: f.user,
      auth_pass: f.auth_pass, priv_pass: f.priv_pass, auth_proto: f.auth_proto, priv_proto: f.priv_proto,
    }
    try {
      if (edit) {
        await api.snmpUpdate(edit.id, body)
        setDone({ name: edit.name, edited: true })
        setEdit(null)
      } else {
        setDone(await api.snmpAdd(body))
      }
      setF(empty)
    } catch (err) { setError(err.message) } finally { setBusy(false) }
  }

  const v3 = f.version === '3'
  return (
    <div className="narrow">
      {!inDialog && (
        <>
          <h1 className="headline">{how === 'other' && models[initialDevice] ? `Подключить ${models[initialDevice]}` : 'Подключить по SNMP'}</h1>
          <p className="lead">Для межсетевых экранов, маршрутизаторов и коммутаторов, куда агент не поставить. Sentra будет следить за процессором, памятью, дисками и каждым интерфейсом: трафик и связь.</p>
          <AddTabs current="snmp" />
        </>
      )}

      <ol className="steps equipment-setup-columns">
        <li>
          <h2>Включите SNMP на устройстве</h2>
          <div className="toolbar" role="group" aria-label="Устройство">
            <button type="button" className={how === 'ideco' ? 'seg active' : 'seg'} onClick={() => setHow('ideco')}>Ideco NGFW</button>
            <button type="button" className={how === 'other' ? 'seg active' : 'seg'} onClick={() => setHow('other')}>Другое устройство</button>
          </div>
          {how === 'ideco' ? <IdecoSteps /> : <OtherSteps />}
        </li>
        <li id="snmp-form">
          <h2>{edit ? `Новый доступ для «${edit.name}»` : 'Укажите адрес и доступ'}</h2>
          <form className="form wide-form" onSubmit={submit}>
            <label>IP-адрес устройства
              <input value={f.addr} onChange={set('addr')} placeholder="192.168.1.1 (порт 161 по умолчанию)" required autoComplete="off" />
            </label>
            <div className="toolbar" role="group" aria-label="Версия SNMP" style={{ justifySelf: 'start' }}>
              <button type="button" className={v3 ? 'seg active' : 'seg'} onClick={() => setF({ ...f, version: '3' })}>SNMPv3</button>
              <button type="button" className={!v3 ? 'seg active' : 'seg'} onClick={() => setF({ ...f, version: '2c' })}>SNMPv2c</button>
            </div>
            {v3 ? (
              <>
                <label>Имя пользователя
                  <input value={f.user} onChange={set('user')} required autoComplete="off" spellCheck="false" />
                </label>
                <label>Пароль
                  <input value={f.auth_pass} onChange={set('auth_pass')} type="password" minLength={8} required autoComplete="new-password" />
                </label>
                <label><span>Ключ для шифрования <span className="muted">(если задан на устройстве)</span></span>
                  <input value={f.priv_pass} onChange={set('priv_pass')} type="password" minLength={8} autoComplete="new-password" />
                </label>
                <details className="help">
                  <summary>Алгоритмы</summary>
                  <p className="small">Ideco и многие другие устройства не показывают, какие алгоритмы используются. Оставьте «Подобрать»: Sentra переберёт варианты при подключении и запомнит подходящий.</p>
                  <label>Аутентификация
                    <select value={f.auth_proto} onChange={set('auth_proto')}>
                      <option value="">Подобрать</option>
                      {['SHA', 'SHA-256', 'SHA-512', 'SHA-224', 'SHA-384', 'MD5'].map((x) => <option key={x}>{x}</option>)}
                    </select>
                  </label>
                  <label>Шифрование
                    <select value={f.priv_proto} onChange={set('priv_proto')}>
                      <option value="">Подобрать</option>
                      {['AES', 'AES-256', 'AES-192', 'DES'].map((x) => <option key={x}>{x}</option>)}
                    </select>
                  </label>
                </details>
              </>
            ) : (
              <label>SNMP Community
                <input value={f.community} onChange={set('community')} required autoComplete="off" spellCheck="false" placeholder="public" />
              </label>
            )}
            {!edit && (
              <label><span>Название в панели <span className="muted">(необязательно, иначе имя из SNMP)</span></span>
                <input value={f.name} onChange={set('name')} placeholder="Например, Ideco, офис" maxLength={100} />
              </label>
            )}
            {error && <p className="banner critical">{error}</p>}
            {done && (
              <p className="banner ok">
                {done.edited ? `Доступ для «${done.name}» обновлён.` : <>Подключено: {done.name}{done.version === '3' && done.auth_proto ? ` (SNMPv3, ${done.auth_proto}${done.priv_proto ? `/${done.priv_proto}` : ''})` : ''}. Через минуту появится на <Link to="/">обзоре</Link>.</>}
              </p>
            )}
            <div className="row-actions">
              <button className="btn primary" disabled={busy} type="submit">{busy ? (v3 && !f.auth_proto ? 'Подбираем параметры…' : 'Проверяем…') : edit ? 'Сохранить доступ' : 'Подключить'}</button>
              {edit && <button type="button" className="btn" onClick={() => { setEdit(null); setF(empty) }}>Отмена</button>}
            </div>
          </form>
        </li>
      </ol>

      {!inDialog && <Devices reloadKey={done} onEdit={startEdit} />}

      <details className="help">
        <summary>Если что-то пошло не так</summary>
        <ul>
          <li><b>Устройство не отвечает</b>: SNMP выключен, адрес <code>{boxAddr}</code> не добавлен в доверенные, или UDP-порт 161 закрыт между коробкой и устройством. Ошибку пароля по SNMP устройство часто не сообщает, а просто молчит: проверьте и пароль.</li>
          <li><b>Неверное имя пользователя или пароль</b>: проверьте, что сохранили настройки на устройстве. Пароль и ключ должны быть не короче 8 символов.</li>
          <li><b>Нет дисков</b>: у многих сетевых устройств их нет в SNMP, это нормально. Процессор, память и интерфейсы всё равно будут.</li>
        </ul>
      </details>
    </div>
  )
}
