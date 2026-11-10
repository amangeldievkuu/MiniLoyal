import type { ReactNode } from 'react';
import { useCallback, useEffect, useState } from 'react';
import type { Member, Session } from '@miniloyal/sdk';
import { MiniLoyal, MiniLoyalError } from '@miniloyal/sdk';

import { CardScreen } from './Card';
import { strings } from './i18n';
import { PointsScreen } from './Points';

type Tab = 'card' | 'points' | 'profile';

const CONSENT_VERSION = import.meta.env.VITE_CONSENT_VERSION ?? '2026-11';

type Screen =
  | { kind: 'loading' }
  | { kind: 'redirecting' }
  | { kind: 'error'; message: string; requestId: string | null }
  | { kind: 'ready'; ml: MiniLoyal; session: Session; member: Member; isFriend: boolean };

function describe(err: unknown, fallback: string): { message: string; requestId: string | null } {
  if (err instanceof MiniLoyalError) {
    return { message: `${err.message} (${err.code})`, requestId: err.requestId };
  }
  return { message: err instanceof Error ? err.message : fallback, requestId: null };
}

/** The demo cafe: sign in with LINE, consent, profile, add-friend prompt. */
export function App() {
  const [screen, setScreen] = useState<Screen>({ kind: 'loading' });
  const t = strings(screen.kind === 'ready' ? screen.session.app.locale : undefined);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const ml = await MiniLoyal.init({
          appId: import.meta.env.VITE_MINILOYAL_APP_ID ?? '',
          liffId: import.meta.env.VITE_LIFF_ID ?? '',
          baseUrl: import.meta.env.VITE_MINILOYAL_API,
        });
        if (cancelled) return;
        if (ml.state === 'redirecting' || !ml.session) {
          setScreen({ kind: 'redirecting' });
          return;
        }
        const [member, isFriend] = await Promise.all([ml.me(), ml.friendship()]);
        if (cancelled) return;
        setScreen({ kind: 'ready', ml, session: ml.session, member, isFriend });
      } catch (err) {
        if (!cancelled)
          setScreen({ kind: 'error', ...describe(err, strings(undefined).genericError) });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const update = useCallback((member: Member) => {
    setScreen((s) => (s.kind === 'ready' ? { ...s, member } : s));
  }, []);

  switch (screen.kind) {
    case 'loading':
      return (
        <Shell title={t.title}>
          <p role="status">{t.signingIn}</p>
        </Shell>
      );
    case 'redirecting':
      return (
        <Shell title={t.title}>
          <p role="status">{t.openingLogin}</p>
        </Shell>
      );
    case 'error':
      return (
        <Shell title={t.title}>
          <p role="alert">{screen.message}</p>
          {screen.requestId && (
            <p className="muted">
              {t.requestId} {screen.requestId}
            </p>
          )}
        </Shell>
      );
    case 'ready':
      return <MemberView screen={screen} onMember={update} />;
  }
}

function MemberView({
  screen,
  onMember,
}: {
  screen: Extract<Screen, { kind: 'ready' }>;
  onMember: (member: Member) => void;
}) {
  const { ml, session, member, isFriend } = screen;
  const t = strings(session.app.locale);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>('card');

  async function accept() {
    setBusy(true);
    setError(null);
    try {
      onMember(await ml.consent(CONSENT_VERSION));
    } catch (err) {
      setError(describe(err, t.genericError).message);
    } finally {
      setBusy(false);
    }
  }

  const addFriendUrl = ml.addFriendUrl();

  const profile = (
    <section className="card profile" aria-label={t.member}>
      {member.picture_url ? (
        <img className="avatar" src={member.picture_url} alt="" width={64} height={64} />
      ) : (
        <div className="avatar placeholder" aria-hidden="true" />
      )}
      <div>
        <h2>{member.display_name ?? t.member}</h2>
        <p className="muted">
          {isFriend ? t.following : t.notFollowing}
          {session.isNew ? ` · ${t.welcome}` : ''}
        </p>
      </div>
    </section>
  );

  return (
    <Shell title={session.app.name} footer={`${session.app.name} · ${session.app.locale}`}>
      {!member.consent ? (
        <>
          {profile}
          <section className="card" aria-label={t.consentTitle}>
            <h3>{t.consentTitle}</h3>
            <p>{t.consentBody}</p>
            <button type="button" onClick={accept} disabled={busy}>
              {busy ? t.saving : t.agree}
            </button>
            {error && <p role="alert">{error}</p>}
          </section>
        </>
      ) : (
        <>
          <nav className="tabs" role="tablist" aria-label={session.app.name}>
            {(
              [
                ['card', t.tabCard],
                ['points', t.tabPoints],
                ['profile', t.tabProfile],
              ] as const
            ).map(([id, label]) => (
              <button
                key={id}
                type="button"
                role="tab"
                aria-selected={tab === id}
                className={tab === id ? 'tab active' : 'tab'}
                onClick={() => setTab(id)}
              >
                {label}
              </button>
            ))}
          </nav>
          {tab === 'card' && <CardScreen ml={ml} t={t} />}
          {tab === 'points' && <PointsScreen ml={ml} t={t} />}
          {tab === 'profile' && (
            <>
              {profile}
              <section className="card" aria-label={t.cardTitle}>
                <p className="muted">
                  {t.consentVersion} {member.consent.version} · {t.memberSince}{' '}
                  {member.created_at.slice(0, 10)}
                </p>
              </section>
            </>
          )}
        </>
      )}

      {!isFriend && addFriendUrl && (
        <section className="card" aria-label={t.addFriendTitle}>
          <h3>{t.addFriendTitle}</h3>
          <p>{t.addFriendBody}</p>
          <a className="button" href={addFriendUrl} target="_blank" rel="noopener noreferrer">
            {t.addFriend}
          </a>
        </section>
      )}
    </Shell>
  );
}

function Shell({
  title,
  footer,
  children,
}: {
  title: string;
  footer?: string;
  children: ReactNode;
}) {
  return (
    <main className="shell">
      <header>
        <h1>{title}</h1>
      </header>
      {children}
      {footer && <footer className="muted">{footer}</footer>}
    </main>
  );
}
