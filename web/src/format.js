const nf1 = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 1 })
const nf0 = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 0 })

export const pct = (v) => (v == null ? '—' : `${nf0.format(v)}%`)

export function bytes(v) {
  if (v == null) return '—'
  const u = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ']
  let i = 0
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${i ? nf1.format(v) : nf0.format(v)} ${u[i]}`
}

export function bitrate(bytesPerSec) {
  if (bytesPerSec == null) return '—'
  let v = bytesPerSec * 8
  const u = ['бит/с', 'Кбит/с', 'Мбит/с', 'Гбит/с']
  let i = 0
  while (v >= 1000 && i < u.length - 1) { v /= 1000; i++ }
  return `${i ? nf1.format(v) : nf0.format(v)} ${u[i]}`
}

export function duration(sec) {
  if (sec == null) return '—'
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60)
  if (d) return `${d} дн ${h} ч`
  if (h) return `${h} ч ${m} мин`
  return `${m} мин`
}

export function ago(unix) {
  if (!unix) return 'ещё не выходил на связь'
  const s = Math.max(0, Math.round(Date.now() / 1000 - unix))
  if (s < 60) return 'только что'
  if (s < 3600) return `${Math.floor(s / 60)} мин назад`
  if (s < 86400) return `${Math.floor(s / 3600)} ч назад`
  return new Date(unix * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit' })
}

// склонение: plural(5, 'устройство', 'устройства', 'устройств')
export function plural(n, one, few, many) {
  const a = Math.abs(n) % 100, b = a % 10
  if (a > 10 && a < 20) return many
  if (b > 1 && b < 5) return few
  if (b === 1) return one
  return many
}

export const statusText = {
  ok: 'В порядке',
  warning: 'Внимание',
  critical: 'Авария',
  offline: 'Нет связи',
  waiting: 'Ждём данные',
}
