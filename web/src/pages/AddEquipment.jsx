import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import Icon from '../Icons.jsx'
import { equipmentSetup } from './EquipmentSetup.jsx'
import './AddEquipment.css'

const categories = [
  {
    id: 'servers', title: 'Серверы', subtitle: 'Linux, агент',
    description: 'Процессор, память, диски и сеть. Агент подключается одной командой.',
    items: [
      { title: 'Linux-сервер', detail: 'Ubuntu, Debian, AlmaLinux и другие системы с systemd', href: '/add/linux', keywords: 'линух линукс linux ubuntu debian alma rocky centos агент сервер' },
    ],
  },
  {
    id: 'virtualization', title: 'Виртуализация', subtitle: 'Proxmox, VMware',
    description: 'Узлы, виртуальные машины и хранилища через API.',
    items: [
      { title: 'Proxmox VE', detail: 'Кластер, ВМ и контейнеры через API-токен', href: '/add/proxmox', keywords: 'проксмокс pve lxc qemu гипервизор' },
      ...(api.pbsList ? [{ title: 'Proxmox Backup Server', detail: 'Хранилища и свежесть резервных копий', href: '/add/pbs', keywords: 'pbs бэкап бэкапы резервное копирование' }] : []),
      { title: 'VMware vCenter', detail: 'Все подключённые хосты ESXi и их виртуальные машины', href: '/add/vmware', keywords: 'вмваре всфера vsphere гипервизор' },
      { title: 'VMware ESXi', detail: 'Отдельный хост, пользователь с правами Read-only', href: '/add/vmware/esxi', keywords: 'вмваре всфера vsphere гипервизор' },
    ],
  },
  {
    id: 'network', title: 'Сетевые устройства', subtitle: 'ping и SNMP',
    description: 'Процессор, память и каждый интерфейс: связь и трафик.',
    items: [
      { title: 'Устройство по IP-адресу', detail: 'Проверка доступности по ICMP', href: '/add/ip', keywords: 'ip ipv4 ping пинг сеть доступность' },
      { title: 'Ideco NGFW', detail: 'SNMPv3, пользователь и ключ шифрования', href: '/add/snmp', keywords: 'айдеко ideco ngfw утм utm фаервол межсетевой экран' },
      { title: 'MikroTik', detail: 'SNMPv3 или v2c, IP › SNMP', href: '/add/snmp/mikrotik', keywords: 'микротик routeros маршрутизатор роутер' },
      { title: 'UserGate', detail: 'SNMPv3', href: '/add/snmp/usergate', keywords: 'юзергейт фаервол межсетевой экран' },
      { title: 'NAS', detail: 'Synology, QNAP по SNMP', href: '/add/snmp/nas', keywords: 'синолоджи сетевое хранилище nas' },
      { title: 'Другой коммутатор или роутер', detail: 'Любое устройство со стандартным SNMP', href: '/add/snmp/other', keywords: 'switch router cisco huawei eltex d-link коммутатор маршрутизатор' },
    ],
  },
  {
    id: 'internet', title: 'Интернет', subtitle: 'канал связи',
    description: 'Доступность интернета, задержка и потери пакетов.',
    items: [
      { title: 'Интернет-канал', detail: 'Проверка роутера и внешних адресов с коробки Sentra', href: '/internet', action: 'Настроить', keywords: 'интернет интернет-канал ping пинг провайдер' },
    ],
  },
]

const quickSearch = ['Ideco', 'MikroTik', 'ESXi', 'Proxmox', 'Ubuntu', 'Synology']

function useConnectionCounts(refresh) {
  const [counts, setCounts] = useState({})
  useEffect(() => {
    let active = true
    api.hosts().then((hosts) => {
      if (!active) return
      const live = hosts.filter((h) => !h.meta?.gone)
      const virtualization = new Set(live.filter((h) => /^(pve|vmw)-/.test(h.kind)).map((h) => h.source_id).filter(Boolean))
      setCounts((prev) => ({
        ...prev,
        servers: live.filter((h) => h.kind === 'agent').length,
        virtualization: virtualization.size + live.filter((h) => h.kind === 'pbs').length,
        network: live.filter((h) => h.kind === 'snmp' || h.kind === 'ping').length,
      }))
    }).catch(() => {})
    api.inet().then(({ config }) => {
      if (active && typeof config?.enabled === 'boolean') setCounts((prev) => ({ ...prev, internet: Number(config.enabled) }))
    }).catch(() => {})
    return () => { active = false }
  }, [refresh])
  return counts
}

function ConnectionWindow({ item, close }) {
  const dialog = useRef(null)
  useLayoutEffect(() => {
    if (!dialog.current.open) dialog.current.showModal()
  }, [])

  return (
    <dialog ref={dialog} className="equipment-dialog" aria-labelledby="equipment-dialog-title" onClose={close}>
      <header className="equipment-dialog-header">
        <div>
          <h2 id="equipment-dialog-title">{item.action || 'Подключить'} {item.title}</h2>
          <p>{item.detail}</p>
        </div>
        <button className="icon-btn" type="button" aria-label="Закрыть окно подключения" onClick={() => dialog.current.close()}>
          <Icon name="close" size={20} />
        </button>
      </header>
      <div className="equipment-dialog-content">
        {equipmentSetup(item.href, true)}
      </div>
    </dialog>
  )
}

export default function AddEquipment() {
  const [selected, setSelected] = useState('network')
  const [query, setQuery] = useState('')
  const [connecting, setConnecting] = useState(null)
  const [refresh, setRefresh] = useState(0)
  const counts = useConnectionCounts(refresh)
  const category = categories.find((item) => item.id === selected)
  const terms = query.trim().toLocaleLowerCase('ru').split(/\s+/).filter(Boolean)
  const searching = terms.length > 0
  const titleMatch = (item) => terms.every((term) => item.title.toLocaleLowerCase('ru').includes(term))
  const items = searching ? categories.flatMap(({ title, items }) => items.filter((item) => {
    const text = [title, item.title, item.detail, item.keywords].join(' ').toLocaleLowerCase('ru')
    return terms.every((term) => text.includes(term))
  })).sort((a, b) => Number(titleMatch(b)) - Number(titleMatch(a))) : category.items

  return (
    <div className="equipment-catalog">
      <h1 className="equipment-title">Добавить оборудование</h1>
      <p className="equipment-intro">Найдите модель или выберите тип ниже.</p>

      <div className="equipment-search" role="search">
        <Icon name="search" size={20} />
        <label className="sr-only" htmlFor="equipment-search">Найти модель или тип оборудования</label>
        <input id="equipment-search" type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Ideco, MikroTik, ESXi, Ubuntu…" autoComplete="off" />
      </div>
      <div className="equipment-quick" role="group" aria-label="Часто ищут">
        <span>Часто ищут:</span>
        {quickSearch.map((name) => (
          <button type="button" key={name} aria-pressed={query.trim() === name} onClick={() => setQuery(query.trim() === name ? '' : name)}>{name}</button>
        ))}
      </div>

      <div className="equipment-layout">
        <div className="equipment-categories" role="group" aria-label="Категории оборудования">
          {categories.map(({ id, title, subtitle }) => (
            <button key={id} type="button" aria-pressed={!searching && selected === id} onClick={() => { setSelected(id); setQuery('') }}>
              <span><strong>{title}</strong><small>{subtitle}</small></span>
              {counts[id] != null && <span className="equipment-count" aria-label={`Подключено: ${counts[id]}`}>{counts[id]}</span>}
            </button>
          ))}
        </div>
        <section className="equipment-panel" aria-labelledby="equipment-section">
          <h2 id="equipment-section">{searching ? 'Результаты поиска' : category.title}</h2>
          <p className="equipment-description" role="status">
            {searching ? (items.length ? `Найдено вариантов: ${items.length}` : 'Ничего не найдено. Попробуйте искать по производителю или типу устройства.') : category.description}
          </p>
          {items.length > 0 && (
            <ul className="equipment-items">
              {items.map((item) => (
                <li key={item.href} className="equipment-item">
                  <div><h3>{item.title}</h3><p>{item.detail}</p></div>
                  {item.href.startsWith('/add/snmp') && <small className="equipment-time">≈ 2 мин</small>}
                  <button type="button" className="btn primary equipment-connect" aria-haspopup="dialog" aria-label={`${item.action || 'Подключить'} ${item.title}`} onClick={() => setConnecting(item)}>{item.action || 'Подключить'}</button>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
      {connecting && <ConnectionWindow item={connecting} close={() => { setConnecting(null); setRefresh((value) => value + 1) }} />}
    </div>
  )
}
