import { useEffect } from 'react';

import { start, useSession } from './auth/session';
import { Header } from './components/Header';
import { Counter } from './screens/Counter';
import { InviteScreen, StartScreen } from './screens/StartScreens';

/** The staff app: sign in through LINE, then the counter. */
export function App() {
  const state = useSession();

  useEffect(() => {
    void start();
  }, []);

  if (state.status === 'signed_in') {
    return (
      <>
        <Header session={state.session} />
        <Counter session={state.session} />
      </>
    );
  }
  if (state.status === 'needs_invite') return <InviteScreen state={state} />;
  return <StartScreen state={state} />;
}
