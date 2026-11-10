import type { StampCard, StampCardCreate, StampCardUpdate } from '../../api/types';

export interface StampDraft {
  name: string;
  stamps_required: string;
  reward_coupon_template_id: string;
  max_stamps_per_visit: string;
}

export type StampErrorCode = 'nameRequired' | 'stampsRequired' | 'maxPerVisit';
/** Field to the code of its message under stampCards.errors. */
export type StampErrors = Partial<Record<keyof StampDraft, StampErrorCode>>;

export const emptyStampDraft: StampDraft = {
  name: '',
  stamps_required: '10',
  reward_coupon_template_id: '',
  max_stamps_per_visit: '1',
};

export const toStampDraft = (c: StampCard): StampDraft => ({
  name: c.name,
  stamps_required: String(c.stamps_required),
  reward_coupon_template_id: c.reward_coupon_template_id ?? '',
  max_stamps_per_visit: String(c.max_stamps_per_visit),
});

const int = (s: string): number => (/^\d+$/.test(s.trim()) ? Number(s.trim()) : NaN);
const inRange = (n: number, min: number, max: number) => n >= min && n <= max;

export function validateStamp(d: StampDraft): StampErrors {
  const e: StampErrors = {};
  if (!d.name.trim()) e.name = 'nameRequired';
  if (!inRange(int(d.stamps_required), 2, 100)) e.stamps_required = 'stampsRequired';
  if (!inRange(int(d.max_stamps_per_visit), 1, 100)) {
    e.max_stamps_per_visit = 'maxPerVisit';
  }
  return e;
}

export function toStampCreate(d: StampDraft): StampCardCreate {
  return {
    name: d.name.trim(),
    stamps_required: int(d.stamps_required),
    max_stamps_per_visit: int(d.max_stamps_per_visit),
    ...(d.reward_coupon_template_id
      ? { reward_coupon_template_id: d.reward_coupon_template_id }
      : {}),
  };
}

/** Only changed fields; removing the reward goes into `clear`. */
export function toStampUpdate(d: StampDraft, original: StampCard): StampCardUpdate {
  const before = toStampDraft(original);
  const body: StampCardUpdate = {};
  if (d.name !== before.name) body.name = d.name.trim();
  if (d.stamps_required !== before.stamps_required) body.stamps_required = int(d.stamps_required);
  if (d.max_stamps_per_visit !== before.max_stamps_per_visit) {
    body.max_stamps_per_visit = int(d.max_stamps_per_visit);
  }
  if (d.reward_coupon_template_id !== before.reward_coupon_template_id) {
    if (d.reward_coupon_template_id) body.reward_coupon_template_id = d.reward_coupon_template_id;
    else body.clear = ['reward_coupon_template_id'];
  }
  return body;
}
