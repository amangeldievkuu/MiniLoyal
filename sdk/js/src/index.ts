/**
 * MiniLoyal SDK for LINE MINI Apps.
 *
 * Phase 0 ships the public shape only. `MiniLoyal.init()` will run `liff.init()`,
 * exchange the LIFF ID token at POST /v1/auth/line and keep the session fresh;
 * the member methods arrive with their API endpoints (Phases 2 to 6).
 */
import createClient from 'openapi-fetch';

import type { paths } from './api/schema';

export type { paths, components } from './api/schema';

/** Options for {@link MiniLoyal.init}. */
export interface MiniLoyalOptions {
  /** The MiniLoyal app ID shown in the dashboard. */
  appId: string;
  /** The LIFF ID of the current MINI App channel (Developing, Review or Published). */
  liffId: string;
  /** API origin. Defaults to https://api.miniloyal.app once it exists. */
  baseUrl?: string;
}

/** The health document returned by the API. */
export type Health = paths['/healthz']['get']['responses']['200']['content']['application/json'];

export class MiniLoyal {
  readonly appId: string;
  readonly liffId: string;
  readonly baseUrl: string;
  private readonly client: ReturnType<typeof createClient<paths>>;

  private constructor(options: MiniLoyalOptions) {
    this.appId = options.appId;
    this.liffId = options.liffId;
    this.baseUrl = (options.baseUrl ?? 'http://localhost:8080').replace(/\/$/, '');
    this.client = createClient<paths>({ baseUrl: this.baseUrl });
  }

  /**
   * Creates a client. Later phases run `liff.init()` and log the member in here;
   * for now it only validates the options.
   */
  static init(options: MiniLoyalOptions): MiniLoyal {
    if (!options.appId) throw new Error('MiniLoyal.init: appId is required');
    if (!options.liffId) throw new Error('MiniLoyal.init: liffId is required');
    return new MiniLoyal(options);
  }

  /** Checks that the API is reachable. */
  async health(): Promise<Health> {
    const { data, error } = await this.client.GET('/healthz');
    if (error || !data) throw new Error('MiniLoyal: API unreachable');
    return data;
  }
}
