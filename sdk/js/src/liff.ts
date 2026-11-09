import liff from '@line/liff';

/**
 * The slice of the LIFF SDK that MiniLoyal uses. Declared structurally so a
 * test double, or a future LIFF major version, can stand in for `@line/liff`.
 */
export interface LiffLike {
  init(config: { liffId: string; withLoginOnExternalBrowser?: boolean }): Promise<void>;
  isLoggedIn(): boolean;
  login(config?: { redirectUri?: string }): void;
  logout(): void;
  getIDToken(): string | null;
  getAccessToken(): string | null;
  getLanguage(): string;
  getFriendship(): Promise<{ friendFlag: boolean }>;
}

/** The real LIFF SDK, typed as the slice above. */
export const defaultLiff: LiffLike = liff as unknown as LiffLike;
