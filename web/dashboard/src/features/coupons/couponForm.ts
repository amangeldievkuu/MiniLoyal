import type {
  CouponTemplate,
  CouponTemplateClearable,
  CouponTemplateCreate,
  CouponTemplateUpdate,
  CouponType,
} from '../../api/types';
import { fromLocalInput, toLocalInput } from '../../lib/datetime';

/** Form state: everything as typed, so empty optional fields stay empty. */
export interface CouponDraft {
  name: string;
  description: string;
  type: CouponType;
  value: string;
  total_limit: string;
  per_member_limit: string;
  valid_from: string;
  valid_to: string;
  validity_days: string;
  claimable: boolean;
}

export type CouponField = keyof CouponDraft;
export type CouponErrorCode =
  | 'nameRequired'
  | 'amountValue'
  | 'percentValue'
  | 'totalLimit'
  | 'perMemberLimit'
  | 'perMemberAboveTotal'
  | 'validityDays'
  | 'date'
  | 'windowOrder';
/** Field to the code of its message under coupons.errors. */
export type CouponErrors = Partial<Record<CouponField, CouponErrorCode>>;

export const emptyDraft: CouponDraft = {
  name: '',
  description: '',
  type: 'amount_off',
  value: '',
  total_limit: '',
  per_member_limit: '1',
  valid_from: '',
  valid_to: '',
  validity_days: '',
  claimable: false,
};

export function toDraft(t: CouponTemplate): CouponDraft {
  return {
    name: t.name,
    description: t.description ?? '',
    type: t.type,
    value: t.value == null ? '' : String(t.value),
    total_limit: t.total_limit == null ? '' : String(t.total_limit),
    per_member_limit: String(t.per_member_limit),
    valid_from: toLocalInput(t.valid_from),
    valid_to: toLocalInput(t.valid_to),
    validity_days: t.validity_days == null ? '' : String(t.validity_days),
    claimable: t.claimable,
  };
}

const int = (s: string): number => (/^\d+$/.test(s.trim()) ? Number(s.trim()) : NaN);
const inRange = (n: number, min: number, max: number) => n >= min && n <= max;

/** Mirrors the API rules so the operator sees problems before submitting. */
export function validateCoupon(d: CouponDraft): CouponErrors {
  const e: CouponErrors = {};
  if (!d.name.trim()) e.name = 'nameRequired';
  if (d.type === 'amount_off' && !inRange(int(d.value), 1, 1_000_000)) {
    e.value = 'amountValue';
  }
  if (d.type === 'percent_off' && !inRange(int(d.value), 1, 100)) {
    e.value = 'percentValue';
  }
  const total = d.total_limit.trim() === '' ? null : int(d.total_limit);
  if (total !== null && !inRange(total, 1, 10_000_000)) e.total_limit = 'totalLimit';
  const per = int(d.per_member_limit);
  if (!inRange(per, 1, 100)) e.per_member_limit = 'perMemberLimit';
  else if (total !== null && inRange(total, 1, 10_000_000) && per > total) {
    e.per_member_limit = 'perMemberAboveTotal';
  }
  if (d.validity_days.trim() !== '' && !inRange(int(d.validity_days), 1, 3650)) {
    e.validity_days = 'validityDays';
  }
  const from = fromLocalInput(d.valid_from);
  const to = fromLocalInput(d.valid_to);
  if (d.valid_from.trim() !== '' && from === null) e.valid_from = 'date';
  if (d.valid_to.trim() !== '' && to === null) e.valid_to = 'date';
  if (from && to && !(new Date(to) > new Date(from))) e.valid_to = 'windowOrder';
  return e;
}

export function toCreateBody(d: CouponDraft): CouponTemplateCreate {
  const from = fromLocalInput(d.valid_from);
  const to = fromLocalInput(d.valid_to);
  return {
    name: d.name.trim(),
    type: d.type,
    per_member_limit: int(d.per_member_limit),
    claimable: d.claimable,
    ...(d.description.trim() ? { description: d.description.trim() } : {}),
    ...(d.type !== 'free_item' ? { value: int(d.value) } : {}),
    ...(d.total_limit.trim() ? { total_limit: int(d.total_limit) } : {}),
    ...(from ? { valid_from: from } : {}),
    ...(to ? { valid_to: to } : {}),
    ...(d.validity_days.trim() ? { validity_days: int(d.validity_days) } : {}),
  };
}

/**
 * Only what changed: an emptied optional field goes into `clear`. When type and
 * value are locked (coupons were issued) they are never sent.
 */
export function toUpdateBody(
  d: CouponDraft,
  original: CouponTemplate,
  lockedTypeValue: boolean,
): CouponTemplateUpdate {
  const before = toDraft(original);
  const body: CouponTemplateUpdate = {};
  const clear: CouponTemplateClearable[] = [];
  const changed = (k: CouponField) => d[k] !== before[k];

  if (changed('name')) body.name = d.name.trim();
  if (changed('description')) {
    if (d.description.trim()) body.description = d.description.trim();
    else clear.push('description');
  }
  if (!lockedTypeValue) {
    if (changed('type')) body.type = d.type;
    if (d.type === 'free_item') {
      if (original.value != null) clear.push('value');
    } else if (changed('value') || changed('type')) {
      body.value = int(d.value);
    }
  }
  if (changed('total_limit')) {
    if (d.total_limit.trim()) body.total_limit = int(d.total_limit);
    else clear.push('total_limit');
  }
  if (changed('per_member_limit')) body.per_member_limit = int(d.per_member_limit);
  if (changed('valid_from')) {
    const v = fromLocalInput(d.valid_from);
    if (v) body.valid_from = v;
    else clear.push('valid_from');
  }
  if (changed('valid_to')) {
    const v = fromLocalInput(d.valid_to);
    if (v) body.valid_to = v;
    else clear.push('valid_to');
  }
  if (changed('validity_days')) {
    if (d.validity_days.trim()) body.validity_days = int(d.validity_days);
    else clear.push('validity_days');
  }
  if (changed('claimable')) body.claimable = d.claimable;
  if (clear.length > 0) body.clear = clear;
  return body;
}
