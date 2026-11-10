import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, beforeEach, vi } from 'vitest';

import { resetSessionForTests } from '../auth/session';
import { setLanguage } from '../i18n';
import { resetPendingForTests } from '../lib/pending';
import { resetLiffMock } from './liff';

vi.mock('@line/liff', async () => ({ default: (await import('./liff')).liffMock }));

setLanguage('en');

beforeEach(() => {
  resetLiffMock();
  resetSessionForTests();
  resetPendingForTests();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
