// Type-checked by `npm run check`. Shows the whole member flow inside a MINI App.
import { MiniLoyal, MiniLoyalError } from '../src/index';

export async function run(): Promise<void> {
  const ml = await MiniLoyal.init({
    appId: 'f7c1d2e3-0000-4000-8000-000000000000',
    liffId: '1661234567-AbCdEfGh',
    baseUrl: 'https://api.example.com',
    onSessionChange: (session) => console.log(session ? 'signed in' : 'signed out'),
  });

  // LINE login is opening in the LIFF browser: render nothing and wait.
  if (ml.state === 'redirecting') return;

  const me = await ml.me();
  console.log(me.display_name, me.is_friend);

  // Ask for consent once, with the version of your personal-data notice.
  if (!me.consent) {
    await ml.consent('2026-11');
  }

  // Profile fields; custom_fields replaces the stored object.
  await ml.updateMe({ birthday: '1990-05-01', custom_fields: { favourite: 'latte' } });

  // Prompt to add the brand's Official Account when the member is not a friend.
  if (!(await ml.friendship())) {
    const url = ml.addFriendUrl();
    if (url) window.open(url, '_blank', 'noopener');
  }

  try {
    await ml.updateMe({ phone: 'not a phone' });
  } catch (err) {
    if (err instanceof MiniLoyalError) {
      console.warn(err.code, err.status, err.requestId, err.field('phone'));
    }
  }
}
