import { vi } from 'vitest';

/** A stand-in for the LIFF SDK; tests set the return values they need. */
export const liffMock = {
  init: vi.fn(async () => undefined),
  isLoggedIn: vi.fn(() => true),
  login: vi.fn(),
  logout: vi.fn(),
  getIDToken: vi.fn((): string | null => 'line-id-token'),
  isApiAvailable: vi.fn(() => false),
  scanCodeV2: vi.fn(async (): Promise<{ value: string | null }> => ({ value: null })),
};

export function resetLiffMock(): void {
  liffMock.init.mockReset().mockResolvedValue(undefined);
  liffMock.isLoggedIn.mockReset().mockReturnValue(true);
  liffMock.login.mockReset();
  liffMock.logout.mockReset();
  liffMock.getIDToken.mockReset().mockReturnValue('line-id-token');
  liffMock.isApiAvailable.mockReset().mockReturnValue(false);
  liffMock.scanCodeV2.mockReset().mockResolvedValue({ value: null });
}
