import type { components } from './api/schema';
import { MiniLoyalError } from './errors';
import type { ApiError } from './errors';
import { defaultLiff } from './liff';
import type { LiffLike } from './liff';

export type Member = components['schemas']['Member'];
export type MemberUpdate = components['schemas']['MemberUpdate'];
export type MemberExport = components['schemas']['MemberExport'];
export type MemberSession = components['schemas']['MemberSession'];
export type AppPublic = components['schemas']['AppPublic'];
export type LineLoginRequest = components['schemas']['LineLoginRequest'];
export type Health = components['schemas']['Health'];

/** Where the client is in its life cycle. */
export type SessionState = 'redirecting' | 'signed_in' | 'signed_out';

/** The signed-in member, kept in memory only. */
export interface Session {
  /** Bearer token for the MiniLoyal API. Never store it. */
  token: string;
  /** Epoch milliseconds when the token stops working. */
  expiresAt: number;
  member: Member;
  app: AppPublic;
  /** True when this sign-in created the member. */
  isNew: boolean;
}

/** Options for {@link MiniLoyal.init}. */
export interface MiniLoyalOptions {
  /** The MiniLoyal app ID shown in the dashboard. */
  appId: string;
  /** The LIFF ID of the current MINI App channel (Developing, Review or Published). */
  liffId: string;
  /** API origin, for example https://api.example.com. Defaults to the page origin. */
  baseUrl?: string;
  /**
   * Send the user to LINE login when there is no session yet. Default true.
   * With false, init resolves in the `signed_out` state and `login()` starts it.
   */
  autoLogin?: boolean;
  /** Where LINE returns after login. Defaults to the current page. */
  redirectUri?: string;
  /** Called with the session after every sign-in and with null when it ends. */
  onSessionChange?: (session: Session | null) => void;
  /** Injected for tests and non-browser runtimes. Defaults to globalThis.fetch. */
  fetch?: typeof globalThis.fetch;
  /** Injected for tests. Defaults to `@line/liff`. */
  liff?: LiffLike;
}

/** Options every mutation accepts. */
export interface MutationOptions {
  /** Reused on retries so the API runs the write once. Generated when absent. */
  idempotencyKey?: string;
}

/** How long before expiry a session is renewed proactively. */
const RENEW_BEFORE_MS = 60_000;

type Method = 'GET' | 'POST' | 'PATCH' | 'DELETE';

interface RequestSpec {
  method: Method;
  path: string;
  body?: unknown;
  idempotencyKey?: string;
  /** False for sign-in itself. */
  auth?: boolean;
}

/**
 * MiniLoyal client for a LINE MINI App.
 *
 * ```ts
 * const ml = await MiniLoyal.init({ appId, liffId, baseUrl });
 * if (ml.state === 'redirecting') return; // LINE login is opening
 * const me = await ml.me();
 * ```
 */
export class MiniLoyal {
  readonly appId: string;
  readonly liffId: string;
  readonly baseUrl: string;

  private readonly liff: LiffLike;
  private readonly fetchImpl: typeof globalThis.fetch;
  private readonly autoLogin: boolean;
  private readonly redirectUri: string | undefined;
  private readonly onSessionChange: ((session: Session | null) => void) | undefined;

  private current: Session | null = null;
  private redirecting = false;
  private exchanging: Promise<Session> | null = null;
  private now: () => number = () => Date.now();

  private constructor(options: MiniLoyalOptions) {
    this.appId = options.appId;
    this.liffId = options.liffId;
    this.baseUrl = (options.baseUrl ?? defaultOrigin()).replace(/\/$/, '');
    this.liff = options.liff ?? defaultLiff;
    this.fetchImpl = options.fetch ?? boundFetch();
    this.autoLogin = options.autoLogin !== false;
    this.redirectUri = options.redirectUri;
    this.onSessionChange = options.onSessionChange;
  }

  /**
   * Initialises LIFF and signs the member in. Never throws for the login
   * redirect: check `state` for `redirecting` and stop rendering.
   */
  static async init(options: MiniLoyalOptions): Promise<MiniLoyal> {
    if (!options.appId)
      throw new MiniLoyalError('invalid_options', 'MiniLoyal.init: appId is required');
    if (!options.liffId)
      throw new MiniLoyalError('invalid_options', 'MiniLoyal.init: liffId is required');
    const ml = new MiniLoyal(options);
    await ml.liff.init({ liffId: options.liffId });
    if (!ml.liff.isLoggedIn()) {
      if (ml.autoLogin) ml.startLogin();
      return ml;
    }
    try {
      await ml.exchange();
    } catch (err) {
      // No ID token after a login usually means the scope is missing; auto
      // login would loop, so surface it instead.
      if (err instanceof MiniLoyalError && err.code === 'redirecting') return ml;
      throw err;
    }
    return ml;
  }

  /** `redirecting` while LINE login opens, `signed_in` with a session, else `signed_out`. */
  get state(): SessionState {
    if (this.redirecting) return 'redirecting';
    return this.current ? 'signed_in' : 'signed_out';
  }

  /** The current session, or null. */
  get session(): Session | null {
    return this.current;
  }

  /** Signs in (or back in) by exchanging a fresh LIFF ID token. */
  async login(): Promise<Session> {
    if (!this.liff.isLoggedIn()) {
      this.startLogin();
      throw new MiniLoyalError('redirecting', 'redirecting to LINE login');
    }
    return this.exchange();
  }

  /** Ends the MiniLoyal session. With `{ liff: true }` also signs out of LIFF. */
  logout(options: { liff?: boolean } = {}): void {
    this.setSession(null);
    if (options.liff) this.liff.logout();
  }

  /** The member's profile. */
  me(): Promise<Member> {
    return this.request<Member>({ method: 'GET', path: '/v1/me' });
  }

  /** Updates profile fields; `custom_fields` replaces the stored object. */
  async updateMe(patch: MemberUpdate, options: MutationOptions = {}): Promise<Member> {
    const member = await this.request<Member>({
      method: 'PATCH',
      path: '/v1/me',
      body: patch,
      idempotencyKey: options.idempotencyKey ?? newKey(),
    });
    this.patchMember(member);
    return member;
  }

  /** Records consent to the personal-data notice identified by `version`. */
  async consent(version: string, options: MutationOptions = {}): Promise<Member> {
    const member = await this.request<Member>({
      method: 'POST',
      path: '/v1/me/consent',
      body: { version },
      idempotencyKey: options.idempotencyKey ?? newKey(),
    });
    this.patchMember(member);
    return member;
  }

  /** Everything the brand stores about the member (PDPA access right). */
  exportMe(): Promise<MemberExport> {
    return this.request<MemberExport>({ method: 'GET', path: '/v1/me/export' });
  }

  /** Erases the member's personal data (PDPA) and ends the session. */
  async deleteMe(): Promise<void> {
    await this.request<unknown>({ method: 'DELETE', path: '/v1/me' });
    this.setSession(null);
  }

  /**
   * Whether the member follows the brand's Official Account, from LIFF.
   * False when LIFF cannot tell (no linked account, external browser).
   */
  async friendship(): Promise<boolean> {
    try {
      const { friendFlag } = await this.liff.getFriendship();
      return friendFlag === true;
    } catch {
      return false;
    }
  }

  /** The add-friend link for the brand's Official Account, or null when unknown. */
  addFriendUrl(): string | null {
    const basicId = this.current?.app.oa_basic_id;
    if (!basicId) return null;
    return `https://line.me/R/ti/p/${encodeURIComponent(basicId)}`;
  }

  /** Checks that the API is reachable. Needs no session. */
  health(): Promise<Health> {
    return this.request<Health>({ method: 'GET', path: '/healthz', auth: false });
  }

  // ---------------------------------------------------------------- internals

  private startLogin(): void {
    this.redirecting = true;
    this.liff.login(this.redirectUri ? { redirectUri: this.redirectUri } : undefined);
  }

  /** Exchanges the LIFF ID token for a session. Concurrent callers share one exchange. */
  private exchange(): Promise<Session> {
    if (this.exchanging) return this.exchanging;
    const run = (async () => {
      const idToken = this.liff.getIDToken();
      if (!idToken) {
        if (this.autoLogin) {
          this.startLogin();
          throw new MiniLoyalError('redirecting', 'redirecting to LINE login');
        }
        throw new MiniLoyalError('not_signed_in', 'no LIFF ID token; call login() first');
      }
      const body: LineLoginRequest = { app_id: this.appId, id_token: idToken };
      const accessToken = this.liff.getAccessToken();
      if (accessToken) body.liff_access_token = accessToken;
      const language = safeLanguage(this.liff);
      if (language) body.language = language;

      const res = await this.send({ method: 'POST', path: '/v1/auth/line', body, auth: false });
      const session = await this.readSession(res);
      this.setSession(session);
      return session;
    })();
    this.exchanging = run.finally(() => {
      this.exchanging = null;
    });
    return this.exchanging;
  }

  /** Runs an authenticated request, renewing the session before and once after a 401. */
  private async request<T>(spec: RequestSpec): Promise<T> {
    if (spec.auth === false) {
      return this.parse<T>(await this.send(spec));
    }
    await this.ensureSession();
    let res = await this.send(spec);
    if (res.status === 401) {
      try {
        await this.exchange();
      } catch (err) {
        this.setSession(null);
        throw err;
      }
      res = await this.send(spec);
      if (res.status === 401) {
        this.setSession(null);
        throw await this.toError(res);
      }
    }
    return this.parse<T>(res);
  }

  private async ensureSession(): Promise<void> {
    if (this.redirecting) throw new MiniLoyalError('redirecting', 'redirecting to LINE login');
    if (this.current && this.current.expiresAt - this.now() > RENEW_BEFORE_MS) return;
    await this.exchange();
  }

  private async send(spec: RequestSpec): Promise<Response> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (spec.body !== undefined) headers['Content-Type'] = 'application/json';
    if (spec.idempotencyKey) headers['Idempotency-Key'] = spec.idempotencyKey;
    if (spec.auth !== false && this.current) headers.Authorization = `Bearer ${this.current.token}`;
    try {
      return await this.fetchImpl(this.baseUrl + spec.path, {
        method: spec.method,
        headers,
        body: spec.body === undefined ? undefined : JSON.stringify(spec.body),
      });
    } catch (err) {
      throw new MiniLoyalError('network', err instanceof Error ? err.message : 'request failed');
    }
  }

  private async parse<T>(res: Response): Promise<T> {
    if (!res.ok) throw await this.toError(res);
    if (res.status === 204) return undefined as T;
    try {
      return (await res.json()) as T;
    } catch {
      throw new MiniLoyalError('invalid_response', 'the API returned a body that is not JSON', {
        status: res.status,
      });
    }
  }

  private async readSession(res: Response): Promise<Session> {
    const data = await this.parse<MemberSession>(res);
    if (!data || typeof data.session_token !== 'string' || !data.member || !data.app) {
      throw new MiniLoyalError('invalid_response', 'unexpected sign-in response', {
        status: res.status,
      });
    }
    return {
      token: data.session_token,
      expiresAt: this.now() + data.expires_in * 1000,
      member: data.member,
      app: data.app,
      isNew: data.is_new,
    };
  }

  private async toError(res: Response): Promise<MiniLoyalError> {
    let body: unknown = null;
    try {
      body = await res.json();
    } catch {
      // Not the envelope: fall through to a generic error.
    }
    if (isEnvelope(body)) return MiniLoyalError.fromEnvelope(res.status, body);
    return new MiniLoyalError('http_' + res.status, `HTTP ${res.status}`, {
      status: res.status,
      requestId: res.headers.get('X-Request-ID'),
    });
  }

  private setSession(session: Session | null): void {
    this.current = session;
    this.redirecting = false;
    this.onSessionChange?.(session);
  }

  private patchMember(member: Member): void {
    if (this.current) this.current = { ...this.current, member };
  }
}

function isEnvelope(v: unknown): v is ApiError {
  if (!v || typeof v !== 'object') return false;
  const o = v as Record<string, unknown>;
  return (
    typeof o.code === 'string' && typeof o.message === 'string' && typeof o.request_id === 'string'
  );
}

function safeLanguage(liff: LiffLike): string | undefined {
  try {
    const lang = liff.getLanguage();
    return typeof lang === 'string' && lang ? lang : undefined;
  } catch {
    return undefined;
  }
}

function defaultOrigin(): string {
  return typeof location !== 'undefined' && location.origin
    ? location.origin
    : 'http://localhost:8080';
}

function boundFetch(): typeof globalThis.fetch {
  if (typeof globalThis.fetch !== 'function') {
    return () =>
      Promise.reject(new MiniLoyalError('network', 'fetch is not available in this runtime'));
  }
  return globalThis.fetch.bind(globalThis);
}

/** A fresh Idempotency-Key. */
export function newKey(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === 'function') return c.randomUUID();
  const bytes = new Uint8Array(16);
  if (c && typeof c.getRandomValues === 'function') c.getRandomValues(bytes);
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}
