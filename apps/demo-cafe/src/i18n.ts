// Visible strings in zh-TW (primary) and en, picked from the app's locale.
// Typed keys: a key missing from `en` fails `npm run check`.
const zhTW = {
  title: 'Demo Cafe',
  signingIn: '正在登入…',
  openingLogin: '正在開啟 LINE 登入…',
  requestId: '請求編號',
  member: 'LINE 會員',
  following: '已加入店家的 LINE 好友',
  notFollowing: '尚未加入店家的 LINE 好友',
  welcome: '歡迎加入！',
  consentTitle: '開始之前',
  consentBody:
    '我們會保存您的 LINE 名稱與頭像，以及您在這裡累積的點數與優惠券，用於經營會員計畫。您隨時可以匯出或刪除您的資料。',
  agree: '我同意',
  saving: '儲存中…',
  cardTitle: '會員卡',
  consentVersion: '同意版本',
  memberSince: '加入日期',
  addFriendTitle: '在 LINE 上收到優惠',
  addFriendBody: '加入店家為好友，即可收到優惠券與最新消息。',
  addFriend: '加入店家好友',
  genericError: '發生錯誤',
  tabCard: '會員卡',
  tabPoints: '點數',
  tabProfile: '我的資料',
  cardHint: '結帳時請出示此 QR 碼給店員掃描。',
  cardRefreshing: '更新中…',
  cardRefreshIn: '秒後更新',
  cardQrLabel: '會員卡 QR 碼',
  cardRetrying: '無法更新會員卡，正在重試…',
  balance: '目前點數',
  pointsUnit: '點',
  lifetimeEarned: '累計獲得',
  nextExpiry: '即將到期',
  noExpiry: '沒有即將到期的點數',
  earnRule: '消費 {spend} 元可得 {points} 點',
  pointsDisabled: '目前暫停累積點數',
  history: '點數紀錄',
  historyEmpty: '還沒有點數紀錄。',
  loadMore: '載入更多',
  loading: '載入中…',
  kindEarn: '獲得',
  kindRedeem: '使用',
  kindAdjust: '調整',
  kindExpire: '到期',
  balanceAfter: '餘額',
};

const en: typeof zhTW = {
  title: 'Demo Cafe',
  signingIn: 'Signing you in…',
  openingLogin: 'Opening LINE login…',
  requestId: 'Request ID',
  member: 'LINE member',
  following: 'Following the shop on LINE',
  notFollowing: 'Not yet following the shop',
  welcome: 'Welcome!',
  consentTitle: 'Before we start',
  consentBody:
    'We keep your LINE name and picture, and the points and coupons you earn here, to run the loyalty programme. You can export or delete your data at any time.',
  agree: 'I agree',
  saving: 'Saving…',
  cardTitle: 'Member card',
  consentVersion: 'Consent version',
  memberSince: 'Member since',
  addFriendTitle: 'Get offers in LINE',
  addFriendBody: 'Add the shop as a friend to receive coupons and news.',
  addFriend: 'Add the shop as a friend',
  genericError: 'Something went wrong',
  tabCard: 'Card',
  tabPoints: 'Points',
  tabProfile: 'Profile',
  cardHint: 'Show this code to the staff at checkout.',
  cardRefreshing: 'Refreshing…',
  cardRefreshIn: 's to next code',
  cardQrLabel: 'Member card QR code',
  cardRetrying: 'Could not refresh the card, retrying…',
  balance: 'Points balance',
  pointsUnit: 'pts',
  lifetimeEarned: 'Earned in total',
  nextExpiry: 'Expiring soon',
  noExpiry: 'Nothing expires soon',
  earnRule: 'Spend NT${spend} to earn {points} pts',
  pointsDisabled: 'Points are paused',
  history: 'History',
  historyEmpty: 'No points yet.',
  loadMore: 'Load more',
  loading: 'Loading…',
  kindEarn: 'Earned',
  kindRedeem: 'Spent',
  kindAdjust: 'Adjusted',
  kindExpire: 'Expired',
  balanceAfter: 'Balance',
};

export type StringKey = keyof typeof zhTW;
export type Strings = typeof zhTW;

/** Returns the string table for a BCP 47 locale; Chinese locales use zh-TW. */
export function strings(locale: string | undefined): typeof zhTW {
  const tag = (locale ?? navigatorLocale()).toLowerCase();
  return tag.startsWith('zh') ? zhTW : en;
}

function navigatorLocale(): string {
  return typeof navigator !== 'undefined' && navigator.language ? navigator.language : 'zh-TW';
}
