// Полоса доступности за 30 дней: один квадрат — один день, справа сегодня.
const stateText = { ok: 'всё работало', warn: 'были предупреждения', down: 'был простой', off: 'выключено', none: 'ещё не подключено' }

function dayLabel(iso, today) {
  const d = new Date(iso + 'T12:00:00')
  if (iso === today) return 'сегодня'
  return d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' })
}

function minutes(n) {
  if (n < 60) return `${n} мин`
  const h = Math.floor(n / 60), m = n % 60
  return m ? `${h} ч ${m} мин` : `${h} ч`
}

export function pctText(p) {
  if (p == null) return '—'
  if (p >= 99.995) return '100%'
  return `${p.toLocaleString('ru-RU', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}%`
}

export function pctTone(p) {
  if (p == null) return ''
  return p < 99.5 ? 'bad' : p < 99.9 ? 'lo' : ''
}

export function hadProblems(av) {
  return !!av?.days?.some((d) => d.state === 'warn' || d.state === 'down')
}

export default function AvailabilityBar({ av, compact = false, labels = true }) {
  if (!av) return null
  const today = av.days[av.days.length - 1]?.date
  const title = (d) => {
    let t = `${dayLabel(d.date, today)}: ${stateText[d.state]}`
    if (d.down_min) t += `, недоступно ${minutes(d.down_min)}`
    if (d.notes?.length) t += '\n' + d.notes.join('\n')
    return t
  }
  const label = `Доступность за 30 дней: ${pctText(av.pct)}`
  return (
    <div className={compact ? 'av av-compact' : 'av'}>
      <div className="av-cells" role="img" aria-label={label}>
        {av.days.map((d) => <span key={d.date} className={`av-cell av-${d.state}`} title={title(d)} />)}
      </div>
      {labels && (
        <div className="av-labels">
          <span>30 дней</span>
          <b className={pctTone(av.pct)}>{pctText(av.pct)}</b>
          <span>сегодня</span>
        </div>
      )}
    </div>
  )
}
