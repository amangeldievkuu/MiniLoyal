import { useEffect, useState } from 'react';

interface Health {
  status: string;
  version: string;
  uptime_seconds: number;
}

/** Placeholder shell. The dashboard screens arrive in Phase 1. */
export function App() {
  const [health, setHealth] = useState<Health | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetch('/healthz')
      .then((r) => (r.ok ? (r.json() as Promise<Health>) : Promise.reject(new Error(r.statusText))))
      .then(setHealth)
      .catch((e: Error) => setError(e.message));
  }, []);

  return (
    <main style={{ fontFamily: 'system-ui, sans-serif', padding: 24 }}>
      <h1>MiniLoyal Dashboard</h1>
      {health && (
        <p>
          API {health.status}, version {health.version}, up {health.uptime_seconds}s
        </p>
      )}
      {error && <p role="alert">API unreachable: {error}</p>}
    </main>
  );
}
