import type { TFunction } from 'i18next';

import type { CouponInstance } from '../api/types';
import i18n from '../i18n';

/** "NT$50 off", "20% off" or "Free item". */
export function couponValue(template: CouponInstance['template'], t: TFunction): string {
  switch (template.type) {
    case 'amount_off':
      return t('coupons.amountOff', { value: (template.value ?? 0).toLocaleString() });
    case 'percent_off':
      return t('coupons.percentOff', { value: template.value ?? 0 });
    default:
      return t('coupons.freeItem');
  }
}

export function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString(i18n.language, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  });
}
