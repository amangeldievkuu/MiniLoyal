/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** The MiniLoyal app this staff app serves (one per brand app). */
  readonly VITE_MINILOYAL_APP_ID?: string;
  /** The LIFF ID of the staff app for this environment. */
  readonly VITE_LIFF_ID?: string;
  /** API origin; empty means same origin (the Vite dev proxy forwards /v1). */
  readonly VITE_MINILOYAL_API?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
