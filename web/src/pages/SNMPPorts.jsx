import { useState } from 'react'
import { api } from '../api.js'
import Chart from '../Chart.jsx'
import { bitrate } from '../format.js'
import { marksFor } from '../marks.js'

import PortMap, { linkStatus } from './PortMap.jsx'

function PortRows({ ports, label, selected, watched, select }) {
  return (
    <div className="tbl-box">
      <table aria-label={label}>
        <thead><tr><th>Интерфейс</th><th>Состояние</th><th className="num">Скорость линка</th><th className="num">Приём</th><th className="num">Передача</th></tr></thead>
        <tbody>
          {ports.map((port) => (
            <tr key={port.index ?? port.name} className={port.admin_up && !port.up && watched.has(port.name) ? 'if-down' : undefined}>
              <td>
                <div className="port-name">
                  <button type="button" className="link-btn" aria-pressed={port === selected} aria-label={`График трафика ${port.name}`} onClick={() => select(port.index ?? port.name)}><b>{port.name}</b></button>
                  {port.alias && port.alias !== port.name && <span className="muted">{port.alias}</span>}
                  {port.mac && <span className="muted small">{port.mac}</span>}
                </div>
              </td>
              <td data-label="Состояние"><span className={port.up ? 'badge s-ok' : ''}>{linkStatus(port)}</span></td>
              <td className="num" data-label="Скорость линка">{port.up && port.speed > 0 ? bitrate(port.speed / 8) : '—'}</td>
              <td className="num" data-label="Приём">{bitrate(port.rx)}</td>
              <td className="num" data-label="Передача">{bitrate(port.tx)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

const portKey = (port) => port.traffic_label || port.name

function PortNames({ id, ports, names, onSave }) {
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState(null)

  async function submit(event) {
    event.preventDefault()
    const fields = new FormData(event.currentTarget)
    const names = Object.fromEntries(ports.map((port) => {
      const key = portKey(port)
      return [key, fields.get(key).trim()]
    }).filter(([, value]) => value))
    setBusy(true)
    setMessage(null)
    try {
      onSave(await api.savePortNames(id, names))
      setMessage({ text: 'Имена портов сохранены.', ok: true })
    } catch (error) {
      setMessage({ text: error.message, ok: false })
    } finally {
      setBusy(false)
    }
  }

  return (
    <details className="help">
      <summary>Аппаратные имена портов</summary>
      <form className="form" onSubmit={submit}>
        {ports.map((port) => {
          const key = portKey(port)
          return <label key={key}>{key}
            <input name={key} defaultValue={Object.hasOwn(names, key) ? names[key] : ''} placeholder={port.hardware_name || port.name} maxLength={80} />
          </label>
        })}
        {message && <p role={message.ok ? 'status' : 'alert'} className={message.ok ? 'muted small' : 'banner critical'}>{message.text}</p>}
        <button type="submit" className="btn primary" disabled={busy}>Сохранить</button>
      </form>
    </details>
  )
}

export default function SNMPPorts({ id, meta, range, tick, history, stale, portNames = {}, onPortNamesSaved, children }) {
  const [selectedId, select] = useState(null)
  const ports = meta.ifaces || []
  // A cluster interface has no verified front-panel position. Keep its metrics available below.
  const isJack = (port) => port.physical && port.name !== 'if_cluster'
  const jacks = ports.filter(isJack)
  const physical = jacks.map((port) => {
    const key = portKey(port)
    return Object.hasOwn(portNames, key) ? { ...port, hardware_name: portNames[key] } : port
  })
  const other = ports.filter((port) => !isJack(port))
  const preferred = physical.length ? physical : other
  const selected = [...physical, ...other].find((port) => (port.index ?? port.name) === selectedId) || preferred.find((port) => port.up) || preferred[0]
  const iface = selected && portKey(selected)
  const watched = new Set(meta.if_watch || [])
  const rowProps = { selected, watched, select }

  return (
    <section className="storages snmp-ports" aria-labelledby="if-h">
      <h2 id="if-h">Физические порты {physical.length > 0 && !stale && <span className="muted small">{physical.filter((port) => port.up).length} из {physical.length} на связи</span>}</h2>
      {physical.length > 0
        ? <PortMap ports={physical} meta={meta} stale={stale} selected={selected} select={select} />
        : <p className="muted small">{ports.length ? 'Пока не удалось определить физические порты по данным SNMP.' : 'Данные об интерфейсах ещё не получены по SNMP.'}</p>}
      {onPortNamesSaved && jacks.length > 0 && <PortNames id={id} ports={jacks} names={portNames} onSave={onPortNamesSaved} />}
      {other.length > 0 && (physical.length > 0 ? (
        <details className="other-ports">
          <summary>Другие интерфейсы ({other.length})</summary>
          <PortRows label="Другие интерфейсы" ports={other} {...rowProps} />
        </details>
      ) : <PortRows label="Интерфейсы SNMP" ports={other} {...rowProps} />)}
      <div className="port-traffic">
        {children}
        {selected && <Chart key={iface} hostId={id} metric="net" iface={iface} range={range} refreshKey={tick} title={`Трафик ${selected.hardware_name || selected.name}`} marks={marksFor('net', history)} />}
      </div>
    </section>
  )
}
