/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_MINILOYAL_APP_ID?: string;
  readonly VITE_LIFF_ID?: string;
  readonly VITE_MINILOYAL_API?: string;
  /** Version of the personal-data notice shown in the consent step. */
  readonly VITE_CONSENT_VERSION?: string;
}
