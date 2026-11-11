// Что рисовать поверх графика: пороги правил и интервалы, когда была проблема.
const thresholds = { cpu: [85, 95], mem: [85, 95], disk: [85, 95], inet_rtt: [150, 400], inet_loss: [3, 15] }
const own = {
  cpu: (k) => k === 'cpu',
  mem: (k) => k === 'mem',
  disk: (k) => k.startsWith('disk:'),
  inet_rtt: (k) => k === 'inet_rtt',
  inet_loss: (k) => k === 'inet_loss',
}

export function marksFor(metric, problems) {
  const now = Date.now() / 1000
  const spans = (problems || [])
    .filter((p) => p.kind === 'down' || own[metric]?.(p.key))
    .map((p) => ({ a: p.opened_at, b: p.closed_at ?? now, tone: (p.closed_at ? p.max_severity : p.severity) === 2 ? 'critical' : 'warning' }))
  const lines = (thresholds[metric] || []).map((v, i) => ({ v, tone: i ? 'critical' : 'warning' }))
  return { lines, spans }
}
