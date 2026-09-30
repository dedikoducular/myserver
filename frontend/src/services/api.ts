// HTTP client for the MyServer API. Every response uses the envelope
// { success, data, error: { code, message } }; failures surface as ApiError
// with the server's Turkish message.

export class ApiError extends Error {
  readonly code: string
  readonly status: number

  constructor(message: string, code: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
  }
}

interface Envelope<T> {
  success: boolean
  data: T
  error: { code: string; message: string } | null
}

const BASE = '/api/v1'

let csrfToken = ''
let onUnauthorized: (() => void) | null = null

export function setCsrfToken(token: string): void {
  csrfToken = token
}

/** Registers the callback run when the server reports the session is gone. */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn
}

export interface RequestOptions {
  signal?: AbortSignal
  /** Query parameters; undefined values are skipped. */
  query?: Record<string, string | number | boolean | undefined>
}

export function apiUrl(path: string, query?: RequestOptions['query']): string {
  let url = BASE + path
  if (query) {
    const params = new URLSearchParams()
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined) params.set(k, String(v))
    }
    const qs = params.toString()
    if (qs) url += '?' + qs
  }
  return url
}

/** Absolute ws:// or wss:// URL for an API path. */
export function wsUrl(path: string, query?: RequestOptions['query']): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}${apiUrl(path, query)}`
}

function statusMessage(status: number): string {
  switch (status) {
    case 401:
      return 'Oturum açmanız gerekiyor.'
    case 403:
      return 'Yetkiniz bulunmuyor.'
    case 404:
      return 'İstenen kayıt bulunamadı.'
    case 413:
      return 'Gönderilen veri çok büyük.'
    case 429:
      return 'Çok fazla istek gönderildi. Lütfen biraz bekleyin.'
    case 502:
    case 503:
    case 504:
      return 'Sunucu şu anda yanıt veremiyor.'
    default:
      return `Sunucu beklenmeyen bir yanıt verdi (${status}).`
  }
}

async function parse<T>(res: Response): Promise<T> {
  let env: Envelope<T> | null = null
  try {
    env = (await res.json()) as Envelope<T>
  } catch {
    env = null
  }
  if (res.ok && env?.success) return env.data
  const err = new ApiError(
    env?.error?.message ?? statusMessage(res.status),
    env?.error?.code ?? 'http_' + res.status,
    res.status,
  )
  if (res.status === 401 && err.code === 'unauthorized') onUnauthorized?.()
  throw err
}

async function request<T>(method: string, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken
  let payload: BodyInit | undefined
  if (body instanceof FormData) {
    payload = body
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  let res: Response
  try {
    res = await fetch(apiUrl(path, opts.query), {
      method,
      headers,
      body: payload,
      credentials: 'same-origin',
      signal: opts.signal,
    })
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new ApiError('Sunucuya ulaşılamıyor. Ağ bağlantınızı kontrol edin.', 'network_error', 0)
  }
  return parse<T>(res)
}

export interface UploadOptions extends RequestOptions {
  onProgress?: (loaded: number, total: number) => void
}

/** Uploads with progress reporting (fetch cannot report upload progress). */
function upload<T>(path: string, form: FormData, opts: UploadOptions = {}): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', apiUrl(path, opts.query))
    xhr.withCredentials = true
    xhr.setRequestHeader('Accept', 'application/json')
    if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken)
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) opts.onProgress?.(e.loaded, e.total)
    }
    xhr.onerror = () =>
      reject(new ApiError('Sunucuya ulaşılamıyor. Ağ bağlantınızı kontrol edin.', 'network_error', 0))
    xhr.onabort = () => reject(new DOMException('İşlem iptal edildi.', 'AbortError'))
    xhr.onload = () => {
      let env: Envelope<T> | null = null
      try {
        env = JSON.parse(xhr.responseText) as Envelope<T>
      } catch {
        env = null
      }
      if (xhr.status >= 200 && xhr.status < 300 && env?.success) {
        resolve(env.data)
        return
      }
      const err = new ApiError(
        env?.error?.message ?? statusMessage(xhr.status),
        env?.error?.code ?? 'http_' + xhr.status,
        xhr.status,
      )
      // Same rule as request(): only a lost session returns to the login page.
      if (xhr.status === 401 && err.code === 'unauthorized') onUnauthorized?.()
      reject(err)
    }
    opts.signal?.addEventListener('abort', () => xhr.abort())
    xhr.send(form)
  })
}

export const api = {
  get: <T>(path: string, opts?: RequestOptions) => request<T>('GET', path, undefined, opts),
  post: <T>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('POST', path, body, opts),
  put: <T>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('PUT', path, body, opts),
  del: <T>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('DELETE', path, body, opts),
  upload,
}

/** Turkish message for any thrown value. */
export function errorMessage(e: unknown, fallback = 'İşlem tamamlanamadı.'): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error && e.message) return e.message
  return fallback
}
