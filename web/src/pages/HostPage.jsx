import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link, navigate } from '../App.jsx'
import Chart from '../Chart.jsx'
import { ago, bitrate, bytes, duration, pct, statusText } from '../format.js'
import { isPVE } from './Overview.jsx'
import AvailabilityBar from '../Availability.jsx'
import { ProblemRows } from './Problems.jsx'
import Fresh from '../Fresh.jsx'
import { marksFor } from '../marks.js'

const rangeList = [['1h', '1 час'], ['6h', '6 часов'], ['24h', 'Сутки'], ['7d', 'Неделя'], ['30d', 'Месяц']]

export default function HostPage({ id }) {
  const [h, setH] = useState(null)
  const [error, setError] = useState(null)
  const [range, setRange] = useState(() => sessionStorage.getItem('sentra.range') || '1h')
  const [tick, setTick] = useState(0)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')
  const [history, setHistory] = useState([])
  const [maintOpen, setMaintOpen] = useState(false)
  const [maintNote, setMaintNote] = useState('')

  useEffect(() => {
    let alive = true
    const load = () => {
      api.host(id).then((x) => alive && (setH(x), setError(null))).catch((e) => alive && setError(e.message))
      api.problems('all', 30, id).then((l) => alive && setHistory(l)).catch(() => {})
    }
    load()
    const t = setInterval(() => { load(); setTick((n) => n + 1) }, 30000)
    return () => { alive = false; clearInterval(t) }
  }, [id])

  const pickRange = (r) => { setRange(r); try { sessionStorage.setItem('sentra.range', r) } catch { /* приватный режим */ } }

  async function saveName(e) {
    e.preventDefault()
    try {
      await api.rename(id, name)
      setH({ ...h, name: name.trim() })
      setEditing(false)
    } catch (err) { alert(err.message) }
  }

  async function maint(minutes) {
    try {
      const r = await api.maintenance(id, minutes, maintNote)
      setH({ ...h, maint_until: r.maint_until, maint_comment: r.maint_comment, maintenance: minutes > 0 })
      setMaintOpen(false); setMaintNote('')
    } catch (err) { alert(err.message) }
  }

  const reload = () => {
    api.host(id).then(setH).catch(() => {})
    api.problems('all', 30, id).then(setHistory).catch(() => {})
  }

  async function remove() {
    if (!confirm(`Удалить «${h.name}» из мониторинга? История графиков сохранится в хранилище, но устройство пропадёт из панели.${isPVE(h) ? '' : ' Агент на сервере нужно будет остановить отдельно.'}`)) return
    try { await api.remove(id); navigate('/') } catch (err) { alert(err.message) }
  }

  if (error && !h) return <><Link to="/" className="back">Все устройства</Link><p className="banner critical">{error}</p></>
  if (!h) return <p className="muted">Загружаем…</p>

  const inv = h.inventory, m = h.last_metrics || {}, meta = h.meta || {}
  const pve = isPVE(h)
  const kindName = { 'pve-node': 'Узел Proxmox', 'pve-qemu': 'Виртуальная машина (QEMU)', 'pve-lxc': 'Контейнер (LXC)' }[h.kind]
  const facts = pve ? [
    ['Тип', kindName],
    ['Proxmox', meta.source_name],
    h.kind !== 'pve-node' ? ['Узел', meta.node] : ['Версия', meta.pve_version && `Proxmox VE ${meta.pve_version}`],
    h.kind !== 'pve-node' ? ['VMID', meta.vmid] : ['Процессор', meta.cpu_model ? `${meta.maxcpu} потоков, ${meta.cpu_model}` : `${meta.maxcpu} потоков`],
    h.kind !== 'pve-node' ? ['Ресурсы', `${meta.maxcpu} vCPU, ${bytes(meta.maxmem)} памяти, диск ${bytes(meta.maxdisk)}`] : ['Память', bytes(meta.maxmem)],
    ['Работает без перезагрузки', h.status === 'stopped' ? 'выключена' : meta.uptime ? duration(meta.uptime) : null],
    h.kind !== 'pve-node' && m.net_rx != null ? ['Сеть сейчас', `приём ${bitrate(m.net_rx)}, передача ${bitrate(m.net_tx)}`] : null,
    meta.tags?.length ? ['Теги', meta.tags.join(', ')] : null,
    ['Данные', `из API Proxmox, ${ago(h.last_seen)}`],
  ].filter((x) => x && x[1] != null && x[1] !== '') : [
    ['Система', inv.os_name || inv.os],
    ['Ядро', inv.kernel],
    ['Процессор', inv.cpu_cores ? `${inv.cpu_cores} ${inv.cpu_cores === 1 ? 'ядро' : inv.cpu_cores < 5 ? 'ядра' : 'ядер'}${inv.cpu_model ? `, ${inv.cpu_model}` : ''}` : null],
    ['Память', inv.mem_total ? bytes(inv.mem_total) : null],
    ['IP-адреса', inv.ips?.join(', ')],
    ['Работает без перезагрузки', m.uptime_seconds != null ? duration(m.uptime_seconds) : null],
    ['Сеть сейчас', m.net_rx != null ? `приём ${bitrate(m.net_rx)}, передача ${bitrate(m.net_tx)}` : null],
    ['Агент', inv.agent_version ? `версия ${inv.agent_version}, данные ${ago(h.last_seen)}` : null],
  ].filter(([, v]) => v)

  const charts = {
    'pve-node': [['cpu', 'Процессор'], ['mem', 'Память'], ['disk', 'Заполнение хранилищ'], ['load', 'Средняя нагрузка (load average)']],
    'pve-qemu': [['cpu', 'Процессор'], ['mem', 'Память'], ['net', 'Сеть'], ...(m.disk_max_mount ? [['disk', 'Диск']] : [])],
    'pve-lxc': [['cpu', 'Процессор'], ['mem', 'Память'], ['disk', 'Диск'], ['net', 'Сеть']],
  }[h.kind] || [['cpu', 'Процессор'], ['mem', 'Память'], ['disk', 'Заполнение дисков'], ['net', 'Сеть'], ['load', 'Средняя нагрузка (load average)']]

  return (
    <>
      <Link to="/" className="back">Все устройства</Link>
      <div className={`host-head tone-${h.status}`}>
        {editing ? (
          <form className="rename" onSubmit={saveName}>
            <label className="sr-only" htmlFor="hn">Название</label>
            <input id="hn" value={name} onChange={(e) => setName(e.target.value)} autoFocus maxLength={100} />
            <button className="btn primary" type="submit">Сохранить</button>
            <button className="btn" type="button" onClick={() => setEditing(false)}>Отмена</button>
          </form>
        ) : (
          <h1 className="headline host-title">
            {h.name}
            <button className="link-btn small" onClick={() => { setName(h.name); setEditing(true) }}>Переименовать</button>
          </h1>
        )}
        <p className="lead">
          <span className={`badge s-${h.status}`}>{statusText[h.status]}</span>
          {h.issues.length > 0 && <span> {h.issues.join(', ')}</span>}
          {h.last_seen > 0 && <Fresh at={h.last_seen * 1000} label="Данные получены" stale={180} />}
        </p>
      </div>

      <section className="host-av" aria-labelledby="av-h">
        <div className="host-av-head">
          <h2 id="av-h">Доступность за 30 дней</h2>
          {h.maintenance ? (
            <span className="maint-on">
              Обслуживание до {new Date(h.maint_until * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })}
              {h.maint_comment && <span className="muted"> ({h.maint_comment})</span>}
              <button className="link-btn" type="button" onClick={() => maint(0)}>Завершить</button>
            </span>
          ) : maintOpen ? (
            <span className="maint-form">
              <label className="sr-only" htmlFor="mn">Причина</label>
              <input id="mn" value={maintNote} onChange={(e) => setMaintNote(e.target.value)} placeholder="Причина, например замена диска" maxLength={200} />
              <button className="btn small-btn" type="button" onClick={() => maint(60)}>1 час</button>
              <button className="btn small-btn" type="button" onClick={() => maint(240)}>4 часа</button>
              <button className="btn small-btn" type="button" onClick={() => maint(24 * 60)}>Сутки</button>
              <button className="link-btn" type="button" onClick={() => setMaintOpen(false)}>Отмена</button>
            </span>
          ) : (
            <button className="btn small-btn" type="button" onClick={() => setMaintOpen(true)} title="Пока идут работы, новые проблемы не заводятся">Режим обслуживания</button>
          )}
        </div>
        <AvailabilityBar av={h.availability} />
      </section>

      {history.length > 0 && (
        <section className="host-probs" aria-labelledby="hp-h">
          <h2 id="hp-h">Проблемы за 30 дней</h2>
          <ProblemRows list={history.slice(0, 10)} showHost={false} onChange={reload} />
          {history.length > 10 && <p className="muted small"><Link to="/problems">Все проблемы</Link></p>}
        </section>
      )}

      <dl className="facts">
        {facts.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
      </dl>

      <div className="toolbar" role="group" aria-label="Период графиков">
        {rangeList.map(([r, label]) => (
          <button key={r} className={range === r ? 'seg active' : 'seg'} onClick={() => pickRange(r)}>{label}</button>
        ))}
      </div>

      {h.kind === 'pve-node' && meta.storages?.length > 0 && (
        <section className="storages" aria-labelledby="st-h">
          <h2 id="st-h">Хранилища</h2>
          <div className="tbl-box">
            <table>
              <thead><tr><th>Хранилище</th><th>Тип</th><th className="num">Занято</th><th className="num">Всего</th><th>Заполнение</th></tr></thead>
              <tbody>
                {meta.storages.map((st) => (
                  <tr key={st.name}>
                    <td><b>{st.name}</b>{st.shared && <span className="muted"> общее</span>}</td>
                    <td data-label="Тип">{st.type}</td>
                    <td className="num" data-label="Занято">{bytes(st.used)}</td>
                    <td className="num" data-label="Всего">{bytes(st.total)}</td>
                    <td data-label="Заполнение"><span className="meter-track inline"><span className={`meter-fill ${st.percent > 95 ? 'critical' : st.percent > 85 ? 'warning' : 'ok'}`} style={{ width: `${Math.min(100, st.percent)}%` }} /></span> {pct(st.percent)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}

      {h.kind === 'pve-qemu' && !m.disk_max_mount && h.status !== 'stopped' && <GuestBanner state={h.meta?.guest_agent} />}

      <div className="charts">
        {charts.map(([metric, title]) => <Chart key={metric} hostId={id} metric={metric} range={range} refreshKey={tick} title={title} marks={marksFor(metric, history)} />)}
      </div>

      {(!pve || h.meta?.gone === 1) && (
        <div className="danger">
          <button className="btn danger-btn" onClick={remove}>Удалить из мониторинга</button>
        </div>
      )}
      {pve && h.meta?.gone !== 1 && (
        <p className="muted small danger">Машины Proxmox появляются и исчезают вместе с кластером. Отключить весь Proxmox можно на странице <Link to="/add/proxmox">«Подключить Proxmox»</Link>.</p>
      )}
    </>
  )
}

// Почему не виден диск ВМ и что сделать. Proxmox сам не знает, что внутри
// диска машины: спрашивает у гостевого агента QEMU.
function GuestBanner({ state }) {
  if (state === 'pending') {
    return <p className="banner info">Спрашиваем у гостевого агента QEMU заполнение дисков. Ответ появится в течение нескольких минут.</p>
  }
  if (state === 'forbidden') {
    return (
      <div className="banner warning guest-help">
        <p><b>У токена нет права читать гостевой агент</b>, поэтому диски внутри ВМ не видны. Право выдаётся один раз на всё подключение.</p>
        <ol>
          <li>В Proxmox: Datacenter › Permissions › Roles › Create. Название <code>SentraGuest</code>, право <code>VM.GuestAgent.Audit</code> (в Proxmox 8 — <code>VM.Monitor</code>).</li>
          <li>Permissions › Add › User Permission: путь <code>/vms</code>, пользователь токена, роль <code>SentraGuest</code>, галочка Propagate.</li>
        </ol>
        <p className="muted">Sentra проверит снова в течение получаса.</p>
      </div>
    )
  }
  if (state === 'error') {
    return <p className="banner info">Гостевой агент не ответил вовремя. Sentra спросит снова через 10 минут.</p>
  }
  return (
    <div className="banner info guest-help">
      <p><b>Диск не виден: в машине нет гостевого агента QEMU.</b> Proxmox видит процессор, память и сеть машины, а заполнение диска знает только её система.</p>
      <ol>
        <li>Внутри машины: Linux — <code>apt install qemu-guest-agent</code>, Windows — VirtIO-драйверы с образа <code>virtio-win</code>.</li>
        <li>В Proxmox: машина › Options › QEMU Guest Agent › Enabled, затем выключить и включить машину (перезагрузки изнутри мало).</li>
      </ol>
      <p className="muted">После этого диски появятся здесь в течение 10 минут.</p>
    </div>
  )
}
