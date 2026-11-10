import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { keys, pointsRulesQuery } from '../api/queries';
import type { PointsRules, PointsRulesPut, Role } from '../api/types';
import { AppHeader } from '../components/AppHeader';
import { ErrorAlert, PageHeader, ReadOnlyNotice } from '../components/feedback';
import { Button, Card, Field, Status } from '../components/ui';
import { formatDateTime } from '../lib/format';
import { canManage } from '../lib/roles';

export function PointsRulesPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>
      {(_app, role) => <PointsRulesView appId={appId} role={role} />}
    </AppHeader>
  );
}

export function PointsRulesView({ appId, role }: { appId: string; role: Role | null }) {
  const { t } = useTranslation();
  const rules = useQuery(pointsRulesQuery(appId));
  if (rules.isPending) return <Status>{t('common.loading')}</Status>;
  if (rules.isError) return <ErrorAlert error={rules.error} />;
  return (
    <PointsRulesForm key={appId} appId={appId} rules={rules.data} editable={canManage(role)} />
  );
}

/** Form state: numbers as typed, so empty optional fields stay empty. */
interface Draft {
  spend_per_point: string;
  points_per_unit: string;
  min_spend: string;
  max_points_per_txn: string;
  expiry_days: string;
  enabled: boolean;
}

function toDraft(r: PointsRules): Draft {
  return {
    spend_per_point: String(r.spend_per_point),
    points_per_unit: String(r.points_per_unit),
    min_spend: String(r.min_spend),
    max_points_per_txn: r.max_points_per_txn == null ? '' : String(r.max_points_per_txn),
    expiry_days: r.expiry_days == null ? '' : String(r.expiry_days),
    enabled: r.enabled,
  };
}

const int = (s: string) => (/^\d+$/.test(s.trim()) ? Number(s.trim()) : NaN);

/** Turns the draft into the request body, or null when a required number is invalid. */
function toBody(d: Draft, currency: string): PointsRulesPut | null {
  const spend = int(d.spend_per_point);
  const points = int(d.points_per_unit);
  const min = d.min_spend.trim() === '' ? 0 : int(d.min_spend);
  const cap = d.max_points_per_txn.trim() === '' ? null : int(d.max_points_per_txn);
  const expiry = d.expiry_days.trim() === '' ? null : int(d.expiry_days);
  if (!(spend >= 1) || !(points >= 1) || !(min >= 0)) return null;
  if (cap !== null && !(cap >= 1)) return null;
  if (expiry !== null && !(expiry >= 1)) return null;
  return {
    currency,
    spend_per_point: spend,
    points_per_unit: points,
    min_spend: min,
    max_points_per_txn: cap,
    expiry_days: expiry,
    enabled: d.enabled,
  };
}

/** The rule in plain language, as members would read it. */
export function RulesPreview({ rules }: { rules: PointsRulesPut | null }) {
  const { t } = useTranslation();
  if (!rules) return null;
  if (!rules.enabled) return <p>{t('points.preview.disabled')}</p>;
  const earn = t('points.preview.earn', {
    spend: rules.spend_per_point,
    count: rules.points_per_unit,
  });
  const expiry =
    rules.expiry_days == null
      ? t('points.preview.never')
      : t('points.preview.expires', { count: rules.expiry_days });
  return (
    <div className="space-y-1">
      <p className="font-medium">{`${earn}; ${expiry}`}</p>
      {rules.min_spend > 0 && <p>{t('points.preview.minSpend', { amount: rules.min_spend })}</p>}
      {rules.max_points_per_txn != null && (
        <p>{t('points.preview.cap', { count: rules.max_points_per_txn })}</p>
      )}
    </div>
  );
}

export function PointsRulesForm({
  appId,
  rules,
  editable,
}: {
  appId: string;
  rules: PointsRules;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [draft, setDraft] = useState<Draft>(() => toDraft(rules));
  const currency = rules.currency || 'TWD';
  const body = toBody(draft, currency);

  const save = useIdempotentMutation(
    async (b: PointsRulesPut, key: string) =>
      unwrap(
        await api.PUT('/v1/dashboard/apps/{appId}/points-rules', {
          params: { path: { appId }, header: { 'Idempotency-Key': key } },
          body: b,
        }),
      ),
    {
      onSuccess: (saved) => {
        qc.setQueryData(keys.pointsRules(appId), saved);
      },
    },
  );
  const err = asApiError(save.error);

  function set<K extends keyof Draft>(field: K, value: Draft[K]) {
    setDraft((d) => ({ ...d, [field]: value }));
  }

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (body) save.mutate(body);
  }

  return (
    <div>
      <PageHeader title={t('points.title')} description={t('points.help')} />
      {!editable && <ReadOnlyNotice />}
      <form onSubmit={submit} aria-busy={save.isPending} className="space-y-6">
        <Card
          description={
            rules.updated_at
              ? `${t('members.detail.updatedAt')}: ${formatDateTime(rules.updated_at)}`
              : undefined
          }
        >
          <fieldset disabled={!editable} className="grid gap-4 sm:grid-cols-2">
            <Field
              id="rules-currency"
              label={t('points.currency')}
              value={currency}
              readOnly
              disabled
            />
            <div />
            <Field
              id="rules-spend"
              name="spend_per_point"
              label={t('points.spendPerPoint')}
              hint={t('points.spendPerPointHint')}
              inputMode="numeric"
              required
              value={draft.spend_per_point}
              onChange={(e) => set('spend_per_point', e.target.value)}
              error={err?.field('spend_per_point')}
            />
            <Field
              id="rules-points"
              name="points_per_unit"
              label={t('points.pointsPerUnit')}
              inputMode="numeric"
              required
              value={draft.points_per_unit}
              onChange={(e) => set('points_per_unit', e.target.value)}
              error={err?.field('points_per_unit')}
            />
            <Field
              id="rules-min"
              name="min_spend"
              label={t('points.minSpend')}
              hint={t('points.minSpendHint')}
              inputMode="numeric"
              value={draft.min_spend}
              onChange={(e) => set('min_spend', e.target.value)}
              error={err?.field('min_spend')}
            />
            <Field
              id="rules-cap"
              name="max_points_per_txn"
              label={t('points.maxPerTxn')}
              hint={t('points.maxPerTxnHint')}
              inputMode="numeric"
              value={draft.max_points_per_txn}
              onChange={(e) => set('max_points_per_txn', e.target.value)}
              error={err?.field('max_points_per_txn')}
            />
            <Field
              id="rules-expiry"
              name="expiry_days"
              label={t('points.expiryDays')}
              hint={t('points.expiryDaysHint')}
              inputMode="numeric"
              value={draft.expiry_days}
              onChange={(e) => set('expiry_days', e.target.value)}
              error={err?.field('expiry_days')}
            />
            <label className="flex items-center gap-2 self-end text-sm">
              <input
                type="checkbox"
                name="enabled"
                checked={draft.enabled}
                onChange={(e) => set('enabled', e.target.checked)}
              />
              {t('points.enabled')}
            </label>
          </fieldset>
        </Card>

        <Card title={t('points.previewTitle')}>
          <div aria-live="polite" data-testid="rules-preview" className="text-sm">
            <RulesPreview rules={body} />
          </div>
        </Card>

        {editable && (
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={save.isPending || !body}>
              {save.isPending ? t('common.working') : t('common.save')}
            </Button>
            {save.isSuccess && <Status>{t('common.saved')}</Status>}
          </div>
        )}
        <ErrorAlert error={save.error} />
      </form>
    </div>
  );
}
