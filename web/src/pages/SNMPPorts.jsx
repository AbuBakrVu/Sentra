import { useState } from 'react'
import Chart from '../Chart.jsx'
import { bitrate } from '../format.js'
import { marksFor } from '../marks.js'

function linkStatus(port) {
  if (port.admin_status === 2 || (port.admin_status == null && !port.admin_up)) return 'Выключен'
  if (port.up) return 'На связи'
  if (port.oper_status === 6) return 'Отсутствует'
  if (port.oper_status === 3 || port.admin_status === 3) return 'Тестирование'
  if (port.oper_status === 5) return 'Ожидает подключения'
  if (port.oper_status === 2 || port.oper_status === 7 || port.oper_status == null) return 'Нет связи'
  return 'Неизвестно'
}

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

export default function SNMPPorts({ id, meta, range, tick, history, children }) {
  const [selectedId, select] = useState(null)
  const ports = meta.ifaces || []
  const physical = ports.filter((port) => port.physical)
  const other = ports.filter((port) => !port.physical)
  const preferred = physical.length ? physical : ports
  const selected = ports.find((port) => (port.index ?? port.name) === selectedId) || preferred.find((port) => port.up) || preferred[0]
  const iface = selected?.traffic_label || selected?.name
  const watched = new Set(meta.if_watch || [])
  const rowProps = { selected, watched, select }

  return (
    <section className="storages snmp-ports" aria-labelledby="if-h">
      <h2 id="if-h">Физические порты {physical.length > 0 && <span className="muted small">{physical.filter((port) => port.up).length} из {physical.length} на связи</span>}</h2>
      {physical.length > 0
        ? <PortRows label="Физические порты" ports={physical} {...rowProps} />
        : <p className="muted small">{ports.length ? 'Пока не удалось определить физические порты по данным SNMP.' : 'Данные об интерфейсах ещё не получены по SNMP.'}</p>}
      {other.length > 0 && (physical.length > 0 ? (
        <details className="other-ports">
          <summary>Другие интерфейсы ({other.length})</summary>
          <PortRows label="Другие интерфейсы" ports={other} {...rowProps} />
        </details>
      ) : <PortRows label="Интерфейсы SNMP" ports={other} {...rowProps} />)}
      <div className="port-traffic">
        {children}
        {selected && <Chart key={iface} hostId={id} metric="net" iface={iface} range={range} refreshKey={tick} title={`Трафик ${selected.name}`} marks={marksFor('net', history)} />}
      </div>
    </section>
  )
}
