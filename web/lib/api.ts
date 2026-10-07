export type Redemption = {
  status?: string
  months?: number
  message?: string
  progress?: number
  username?: string
  checkout_url?: string
  link_ready?: boolean
  can_regenerate?: boolean
  link_regenerated?: boolean
  expires_at?: number
}

export type Plans = {
  plans: { months: number; amount: number; currency: string }[]
}

export type Security = {
  turnstile_enabled: boolean
  turnstile_site_key: string
}

export type ApiResult<T> = {
  ok: boolean
  status: number
  data: T
  message: string
}

// The method is derived from the body: omit it for a GET, pass one for a POST.
// That means a write with no parameters still has to pass an empty object,
// otherwise it goes out as a GET and misses its route.
export async function api<T>(
  path: string,
  body?: unknown,
  init?: RequestInit,
): Promise<ApiResult<T>> {
  try {
    // The content type is set on a Headers instance rather than in an object
    // literal that `init` is then spread over. Spreading over it replaces the
    // whole header map, so any caller passing its own headers — every admin
    // write, and every redeem carrying a Turnstile token — silently lost
    // "application/json" and was rejected by the server's content-type check.
    const headers = new Headers(init?.headers)
    if (body !== undefined) headers.set("Content-Type", "application/json")
    const response = await fetch(path, {
      credentials: "same-origin",
      ...init,
      method: body === undefined ? "GET" : "POST",
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const text = await response.text()
    let data = {} as T
    try {
      data = text ? (JSON.parse(text) as T) : ({} as T)
    } catch {
      data = {} as T
    }
    const message =
      typeof (data as { message?: unknown }).message === "string"
        ? (data as { message: string }).message
        : ""
    return { ok: response.ok, status: response.status, data, message }
  } catch {
    return {
      ok: false,
      status: 0,
      data: {} as T,
      message: "网络连接失败，请检查网络后重试。",
    }
  }
}

export function adminHeader(password: string) {
  return { Authorization: `Basic ${btoa(`admin:${password}`)}` }
}

// The admin UI is served under a per-install random segment and its API lives
// under that same prefix. Both are read back from the page's own URL, so the
// bundle never has to be told the segment.
export function adminBase() {
  const segment = window.location.pathname.split("/").filter(Boolean)[0]
  return segment ? `/${segment}` : ""
}

export async function adminApi<T>(
  password: string,
  path: string,
  body?: unknown,
): Promise<ApiResult<T>> {
  return api<T>(`${adminBase()}${path}`, body, { headers: adminHeader(password) })
}

export function formatAmount(minor: number, currency: string) {
  const value = (minor / 100).toFixed(2)
  return `${value} ${currency.toUpperCase()}`
}

export function formatDate(unix?: number) {
  if (!unix) return "—"
  return new Date(unix * 1000).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  })
}

export function formatTime(unix?: number) {
  if (!unix) return "—"
  return new Date(unix * 1000).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })
}

export function formatBytes(bytes: number) {
  if (!bytes) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}
