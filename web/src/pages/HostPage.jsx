import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Link, navigate } from '../App.jsx'
import Chart from '../Chart.jsx'
import { ago, bitrate, bytes, duration, pct, statusText } from '../format.js'
import { isPVE } from './Overview.jsx'

const rangeList = [['1h', '1 час'], ['6h', '6 часов'], ['24h', 'Сутки'], ['7d', 'Неделя'], ['30d', 'Месяц']]

export default function HostPage({ id }) {
  const [h, setH] = useState(null)
  const [error, setError] = useState(null)
  const [range, setRange] = useState(() => sessionStorage.getItem('sentra.range') || '1h')
  const [tick, setTick] = useState(0)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')

  useEffect(() => {
    let alive = true
    const load = () => api.host(id).then((x) => alive && (setH(x), setError(null))).catch((e) => alive && setError(e.message))
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
        </p>
      </div>

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
                    <td>{st.type}</td>
                    <td className="num">{bytes(st.used)}</td>
                    <td className="num">{bytes(st.total)}</td>
                    <td><span className="meter-track inline"><span className={`meter-fill ${st.percent > 95 ? 'critical' : st.percent > 85 ? 'warning' : 'ok'}`} style={{ width: `${Math.min(100, st.percent)}%` }} /></span> {pct(st.percent)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}

      {h.kind === 'pve-qemu' && !m.disk_max_mount && h.status !== 'stopped' && (
        <p className="banner info">Заполнение диска внутри виртуальной машины Proxmox показывает только при установленном гостевом агенте QEMU. Сейчас видны процессор, память и сеть.</p>
      )}

      <div className="charts">
        {charts.map(([metric, title]) => <Chart key={metric} hostId={id} metric={metric} range={range} refreshKey={tick} title={title} />)}
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
