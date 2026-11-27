import { bitrate } from '../format.js'
import './PortMap.css'

// Front-panel copper ports, CISCO-PRODUCTS-MIB (Catalyst 3750-X 24T/48T/24P/48P).
const catalystPorts = new Map([[1222, 24], [1223, 48], [1224, 24], [1225, 48]])

function panelPositions(meta, ports) {
  const oid = /^\.?1\.3\.6\.1\.4\.1\.9\.1\.(\d+)$/.exec(meta.object_id)
  const model = /\b(?:WS-C|Catalyst\s+)3750X-(24|48)(?:T|PF?)(?:-|\b)/i.exec(meta.sys_descr)
  const count = catalystPorts.get(Number(oid?.[1])) || Number(model?.[1])
  if (!count) return null

  const slots = new Map()
  const used = new Set()
  let member
  for (const port of ports) {
    const match = /^(?:Gi|GigabitEthernet)(\d+)\/0\/(\d+)$/i.exec(port.hardware_name || port.name)
    if (!match) continue // Modular uplinks have a separate bank.
    const number = Number(match[2])
    if ((member && member !== match[1]) || number < 1 || number > count || used.has(number)) return null
    member = match[1]
    used.add(number)
    slots.set(port, { gridColumn: Math.ceil(number / 2), gridRow: number % 2 ? 1 : 2 })
  }
  return slots.size ? slots : null
}

export function linkStatus(port) {
  if (port.admin_status === 2 || (port.admin_status == null && port.admin_up === false)) return 'Выключен'
  if (port.up) return 'На связи'
  if (port.oper_status === 6) return 'Отсутствует'
  if (port.oper_status === 3 || port.admin_status === 3) return 'Тестирование'
  if (port.oper_status === 5) return 'Ожидает подключения'
  if (port.oper_status === 2 || port.oper_status === 7 || (port.oper_status == null && port.up === false)) return 'Нет связи'
  return 'Неизвестно'
}

function loadPercent(rate, speed) {
  if (rate == null || !(speed > 0)) return '—'
  return (rate * 8 / speed).toLocaleString('ru-RU', { style: 'percent', maximumFractionDigits: 1 })
}

function Port({ port, status, selected, select, position }) {
  const up = status === 'На связи'
  const down = ['Нет связи', 'Выключен', 'Отсутствует'].includes(status)
  const title = port.hardware_name || port.name
  const logical = title !== port.name ? port.name : ''
  const description = port.alias !== title && port.alias !== port.name ? port.alias : ''
  const details = [title, logical, description, port.mac, status]
  if (up) details.push('Скорость линка: ' + bitrate(port.speed > 0 ? port.speed / 8 : null),
    'Загрузка на приём: ' + loadPercent(port.rx, port.speed), 'На передачу: ' + loadPercent(port.tx, port.speed))
  return (
    <button type="button" className="snmp-port" data-state={up ? 'up' : down ? 'down' : 'unknown'}
      style={position} aria-pressed={selected} title={details.filter(Boolean).join('\n')}
      onClick={() => select(port.index ?? port.name)}>
      <b className="snmp-port-name">{title}</b>
      <span className="snmp-jack" aria-hidden="true"><i /></span>
      <span className="snmp-port-status">{status}</span>
      {up && <span className="snmp-port-rates">
        <span><span className="sr-only">Приём </span>↓ {bitrate(port.rx)}</span>
        <span><span className="sr-only">Передача </span>↑ {bitrate(port.tx)}</span>
      </span>}
      {logical && <span className="snmp-port-label">{logical}</span>}
      {description && <span className="snmp-port-label">{description}</span>}
    </button>
  )
}

export default function PortMap({ ports, meta, stale, selected, select }) {
  const positions = panelPositions(meta, ports)
  const renderPort = (port) => <Port key={port.index ?? port.name} port={port} select={select}
    selected={port === selected} position={positions?.get(port)} status={stale ? 'Нет свежих данных' : linkStatus(port)} />
  const showDetails = ports.includes(selected)
  const active = showDetails && !stale && linkStatus(selected) === 'На связи'
  const extra = positions ? ports.filter((p) => !positions.has(p)) : []
  return (
    <div className="snmp-map" role="group" aria-label="Схема портов">
      <div className={'snmp-port-bank' + (positions ? ' paired' : '')}>
        {(positions ? ports.filter((p) => positions.has(p)) : ports).map(renderPort)}
      </div>
      {extra.length > 0 && <div className="snmp-port-bank">{extra.map(renderPort)}</div>}
      {showDetails && <div className="snmp-port-info" aria-label="Выбранный порт">
        <b>{selected.hardware_name || selected.name}</b>
        {selected.hardware_name && selected.hardware_name !== selected.name && <span>{selected.name}</span>}
        {selected.alias && selected.alias !== selected.name && selected.alias !== selected.hardware_name && <span>{selected.alias}</span>}
        {selected.mac && <span>{selected.mac}</span>}
        {active && <>
          <span>Линк {bitrate(selected.speed > 0 ? selected.speed / 8 : null)}</span>
          <span>Загрузка ↓ {loadPercent(selected.rx, selected.speed)} · ↑ {loadPercent(selected.tx, selected.speed)}</span>
        </>}
      </div>}
    </div>
  )
}
