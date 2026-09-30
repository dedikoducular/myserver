// Shared helpers for tests: the network is faked at the fetch / EventSource
// boundary and always answers with the real API envelope.
import { vi, type Mock } from 'vitest'
import type { AuthStatus, User } from '@/types/api'

export function ok(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ success: true, data, error: null }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

export function fail(status: number, code: string, message: string): Response {
  return new Response(JSON.stringify({ success: false, data: null, error: { code, message } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

export interface Call {
  method: string
  /** Path below /api/v1, without the query string. */
  path: string
  query: URLSearchParams
  headers: Record<string, string>
  body: unknown
  init: RequestInit
}

type Handler = (call: Call) => Response | Promise<Response>
export type Routes = Record<string, Handler | Response>

export interface FetchMock {
  fn: Mock
  calls: Call[]
  /** Calls matching "METHOD /path". */
  find: (key: string) => Call[]
}

/** Stubs global fetch. Routes are keyed "METHOD /path" (path below /api/v1).
 *  Unknown routes answer 404 with the envelope. */
export function mockFetch(routes: Routes): FetchMock {
  const calls: Call[] = []
  const fn = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://localhost')
    const call: Call = {
      method: init.method ?? 'GET',
      path: url.pathname.replace(/^\/api\/v1/, ''),
      query: url.searchParams,
      headers: (init.headers ?? {}) as Record<string, string>,
      body: typeof init.body === 'string' ? JSON.parse(init.body) : init.body,
      init,
    }
    calls.push(call)
    const route = routes[`${call.method} ${call.path}`]
    if (!route) return fail(404, 'not_found', 'İstenen kayıt bulunamadı.')
    return typeof route === 'function' ? route(call) : route.clone()
  })
  vi.stubGlobal('fetch', fn)
  return { fn, calls, find: (key) => calls.filter((c) => `${c.method} ${c.path}` === key) }
}

/** A promise with its resolvers exposed, to control when a response arrives. */
export function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

export function makeUser(over: Partial<User> = {}): User {
  return { id: 1, username: 'abdullah', role: 'admin', disabled: false, created_at: 1_700_000_000, last_login_at: null, ...over }
}

export function makeStatus(over: Partial<AuthStatus> = {}): AuthStatus {
  return { setup_complete: true, authenticated: false, user: null, csrf_token: 'csrf-anon', version: '1.0.0', language: 'tr', ...over }
}

export function signedIn(user: Partial<User> = {}): AuthStatus {
  return makeStatus({ authenticated: true, user: makeUser(user), csrf_token: 'csrf-session' })
}

/** Minimal EventSource stand-in (jsdom has none). */
export class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  static instances: FakeEventSource[] = []

  readyState = 0
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  private listeners = new Map<string, Array<(e: MessageEvent) => void>>()

  constructor(
    readonly url: string,
    readonly init?: EventSourceInit,
  ) {
    FakeEventSource.instances.push(this)
  }

  addEventListener(name: string, fn: (e: MessageEvent) => void): void {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), fn])
  }

  close(): void {
    this.closed = true
    this.readyState = 2
  }

  emitOpen(): void {
    this.readyState = 1
    this.onopen?.()
  }

  emit(name: string, data: string): void {
    for (const fn of this.listeners.get(name) ?? []) fn(new MessageEvent(name, { data }))
  }

  static install(): void {
    FakeEventSource.instances = []
    vi.stubGlobal('EventSource', FakeEventSource)
  }

  static get open(): FakeEventSource[] {
    return FakeEventSource.instances.filter((s) => !s.closed)
  }
}

/** Makes document.hidden controllable; call the returned function to change it. */
export function controlVisibility(): (hidden: boolean) => void {
  let hidden = false
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
  return (value: boolean) => {
    hidden = value
    document.dispatchEvent(new Event('visibilitychange'))
  }
}

export function restoreVisibility(): void {
  // Removes the instance override so the prototype getter is used again.
  delete (document as unknown as Record<string, unknown>).hidden
}
