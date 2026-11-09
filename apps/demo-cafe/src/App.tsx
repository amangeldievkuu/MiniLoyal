import { MiniLoyal } from '@miniloyal/sdk';

const ml = MiniLoyal.init({
  appId: import.meta.env.VITE_MINILOYAL_APP_ID ?? 'app_demo',
  liffId: import.meta.env.VITE_LIFF_ID ?? '0000000000-demo',
  baseUrl: import.meta.env.VITE_MINILOYAL_API ?? 'http://localhost:8080',
});

/** Placeholder shell. The cafe loyalty flow (login, card, coupons) arrives in Phase 6. */
export function App() {
  return (
    <main style={{ fontFamily: 'system-ui, sans-serif', padding: 24 }}>
      <h1>Demo Cafe</h1>
      <p>
        App {ml.appId}, LIFF {ml.liffId}
      </p>
    </main>
  );
}
