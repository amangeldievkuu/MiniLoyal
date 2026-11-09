import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

import { setLanguage } from '../i18n';

setLanguage('en');

afterEach(() => {
  cleanup();
});
