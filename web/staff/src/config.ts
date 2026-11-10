/** Deployment settings, one set per environment (never hard-coded). */
export const config = {
  appId: import.meta.env.VITE_MINILOYAL_APP_ID ?? '',
  liffId: import.meta.env.VITE_LIFF_ID ?? '',
  apiBase: (import.meta.env.VITE_MINILOYAL_API ?? '').replace(/\/$/, ''),
};

/** Whether the app was built with the settings it needs to start. */
export function configured(): boolean {
  return config.appId !== '' && config.liffId !== '';
}
