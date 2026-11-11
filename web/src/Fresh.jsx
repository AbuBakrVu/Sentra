import { useEffect, useState } from 'react'

// Метка свежести: когда данные на экране получены в последний раз.
export default function Fresh({ at, label = 'Обновлено', stale = 120 }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 5000)
    return () => clearInterval(t)
  }, [])
  if (!at) return null
  const s = Math.max(0, Math.round((Date.now() - at) / 1000))
  const text = s < 5 ? 'только что' : s < 60 ? `${s} с назад` : s < 3600 ? `${Math.floor(s / 60)} мин назад` : `${Math.floor(s / 3600)} ч назад`
  return <span className={s > stale ? 'fresh old' : 'fresh'} title={new Date(at).toLocaleString('ru-RU')}><i aria-hidden="true" />{label} {text}</span>
}
