import { useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { Link } from '../App.jsx'
import { useHosts } from './Overview.jsx'

export default function AddServer() {
  const [enroll, setEnroll] = useState(null)
  const [error, setError] = useState(null)
  const [copied, setCopied] = useState(false)
  const startedAt = useRef(Math.floor(Date.now() / 1000))
  const { hosts } = useHosts(5000)

  async function create() {
    setError(null)
    try {
      setEnroll(await api.enroll())
      startedAt.current = Math.floor(Date.now() / 1000) - 5
    } catch (e) { setError(e.message) }
  }

  useEffect(() => { create() }, [])

  async function copy() {
    try {
      await navigator.clipboard.writeText(enroll.linux)
    } catch {
      // http-страница или старый браузер: выделяем текст, чтобы скопировать вручную
      const r = document.createRange()
      r.selectNodeContents(document.getElementById('cmd'))
      const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(r)
      document.execCommand('copy')
    }
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const fresh = (hosts || []).filter((h) => h.created_at >= startedAt.current || (h.last_seen && h.created_at >= startedAt.current))
  const until = enroll ? new Date(enroll.expires_at * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit' }) : ''

  return (
    <div className="narrow">
      <h1 className="headline">Подключить сервер</h1>
      <p className="lead">Нужен Linux с systemd и доступ к этой панели по сети. Агент сам найдёт диски и сетевые интерфейсы, настраивать ничего не нужно. Windows-серверы появятся в следующей версии.</p>

      <ol className="steps">
        <li>
          <h2>Скопируйте команду</h2>
          {error && <p className="banner critical">{error}</p>}
          {enroll && (
            <>
              <div className="cmd">
                <code id="cmd">{enroll.linux}</code>
                <button className="btn primary" onClick={copy}>{copied ? 'Скопировано' : 'Копировать'}</button>
              </div>
              <p className="muted small">Команда действует до {until}. По ней можно подключить несколько серверов. <button className="link-btn" onClick={create}>Создать новую команду</button></p>
            </>
          )}
        </li>
        <li>
          <h2>Выполните её на сервере</h2>
          <p>Под пользователем с правами sudo. Скрипт скачает агент с этой панели, проверит её сертификат и запустит службу <code>sentra-agent</code>.</p>
        </li>
        <li>
          <h2>Дождитесь появления сервера</h2>
          {fresh.length === 0 ? (
            <p className="waiting"><span className="pulse" aria-hidden="true" />Ждём подключения. Список обновляется сам.</p>
          ) : (
            <ul className="fresh">
              {fresh.map((h) => (
                <li key={h.id}>
                  <Link to={`/hosts/${h.id}`}>{h.name}</Link>
                  <span className="muted">{h.last_seen ? ' подключён, данные идут' : ' зарегистрирован, ждём первые данные'}</span>
                </li>
              ))}
            </ul>
          )}
        </li>
      </ol>

      <details className="help">
        <summary>Если что-то пошло не так</summary>
        <ul>
          <li><b>public key does not match pinned public key</b>: сервер открыл не эту панель. Проверьте адрес в команде.</li>
          <li><b>Токен подключения недействителен</b>: команда устарела, создайте новую.</li>
          <li><b>Could not connect</b>: сервер не видит панель. Проверьте, что порт 443 панели доступен с сервера.</li>
          <li>Журнал агента: <code>journalctl -u sentra-agent -n 50</code></li>
        </ul>
      </details>
    </div>
  )
}
