import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';

import en from '../locales/en.json';
import zhTW from '../locales/zh-TW.json';

// Every language must carry the same keys as zh-TW; a missing one fails tsc.
const enChecked: typeof zhTW = en;

export const LANGUAGES = [
  { code: 'zh-TW', label: '中文', html: 'zh-Hant-TW' },
  { code: 'en', label: 'EN', html: 'en' },
] as const;

export type Language = (typeof LANGUAGES)[number]['code'];

const STORAGE_KEY = 'ml.staff.lang';

function isLanguage(v: unknown): v is Language {
  return LANGUAGES.some((l) => l.code === v);
}

function initialLanguage(): Language {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (isLanguage(stored)) return stored;
  } catch {
    // Storage may be unavailable; fall through to the default.
  }
  return navigator.language.toLowerCase().startsWith('en') ? 'en' : 'zh-TW';
}

function applyHtmlLang(code: Language) {
  document.documentElement.lang = LANGUAGES.find((l) => l.code === code)?.html ?? code;
}

void i18n.use(initReactI18next).init({
  resources: { 'zh-TW': { translation: zhTW }, en: { translation: enChecked } },
  lng: initialLanguage(),
  fallbackLng: 'zh-TW',
  interpolation: { escapeValue: false },
  returnNull: false,
});
applyHtmlLang(isLanguage(i18n.language) ? i18n.language : 'zh-TW');

/** Switches the UI language and remembers it on this device (a preference, not a secret). */
export function setLanguage(code: Language): void {
  void i18n.changeLanguage(code);
  applyHtmlLang(code);
  try {
    localStorage.setItem(STORAGE_KEY, code);
  } catch {
    // Remembering the language is a convenience only.
  }
}

export default i18n;

declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'translation';
    resources: { translation: typeof zhTW };
  }
}
