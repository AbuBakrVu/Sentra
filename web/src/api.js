export class ApiError extends Error {
  constructor(status, message) {
    super(message)
    this.status = status
  }
}

async function call(method, path, body) {
  const opt = { method, headers: {}, credentials: 'same-origin' }
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json'
    opt.body = JSON.stringify(body)
  }
  let res
  try {
    res = await fetch(path, opt)
  } catch {
    throw new ApiError(0, 'Сервер мониторинга не отвечает. Проверьте сеть.')
  }
  let data = null
  try { data = await res.json() } catch { /* пустой ответ */ }
  if (!res.ok) {
    if (res.status === 401 && path !== '/api/login') window.dispatchEvent(new Event('sentra:logout'))
    throw new ApiError(res.status, data?.error || `Ошибка ${res.status}`)
  }
  return data
}

export const api = {
  me: () => call('GET', '/api/me'),
  login: (login, password) => call('POST', '/api/login', { login, password }),
  logout: () => call('POST', '/api/logout'),
  password: (old, nw) => call('POST', '/api/password', { old, new: nw }),
  hosts: () => call('GET', '/api/hosts'),
  host: (id) => call('GET', `/api/hosts/${id}`),
  rename: (id, name) => call('PATCH', `/api/hosts/${id}`, { name }),
  remove: (id) => call('DELETE', `/api/hosts/${id}`),
  chart: (id, m, range) => call('GET', `/api/hosts/${id}/chart?m=${m}&range=${range}`),
  enroll: () => call('POST', '/api/enroll'),
}
