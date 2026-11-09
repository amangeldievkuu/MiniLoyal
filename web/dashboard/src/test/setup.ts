import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';

import { setLanguage } from '../i18n';

setLanguage('en');

// jsdom does not implement scrolling; the router scrolls on navigation.
window.scrollTo = vi.fn() as unknown as typeof window.scrollTo;

afterEach(() => {
  cleanup();
});
