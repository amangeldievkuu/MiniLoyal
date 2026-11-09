import { useQuery } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { appsQuery, auditQuery } from '../api/queries';
import { useDeclareTenant } from '../auth/tenant';
import { EmptyState, ErrorAlert, PageHeader } from '../components/feedback';
import { Badge, Button, Card, Select, Status, Table, Td, Th } from '../components/ui';
import { formatDateTime } from '../lib/format';

export function AuditPage() {
  const { t } = useTranslation();
  const { tenantId = '' } = useParams({ strict: false });
  useDeclareTenant(tenantId);
  const [appId, setAppId] = useState<string | undefined>(undefined);
  // A stack of cursors: the last one is the page in view, popping goes back.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const apps = useQuery(appsQuery(tenantId));
  const page = useQuery(auditQuery(tenantId, appId, cursor));
  const appName = (id: string | null | undefined) =>
    apps.data?.find((a) => a.id === id)?.name ?? (id ? id.slice(0, 8) : '');

  return (
    <div>
      <PageHeader title={t('audit.title')} description={t('audit.help')} />
      <Card
        actions={
          <label className="flex items-center gap-2 text-sm">
            <span>{t('common.filter')}</span>
            <Select
              className="w-auto"
              value={appId ?? ''}
              onChange={(e) => {
                setAppId(e.target.value || undefined);
                setCursors([]);
              }}
            >
              <option value="">{t('audit.allApps')}</option>
              {apps.data?.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </Select>
          </label>
        }
      >
        {page.isPending && <Status>{t('common.loading')}</Status>}
        {page.isError && <ErrorAlert error={page.error} />}
        {page.data && page.data.items.length === 0 && <EmptyState>{t('audit.empty')}</EmptyState>}
        {page.data && page.data.items.length > 0 && (
          <Table>
            <thead>
              <tr>
                <Th>{t('audit.time')}</Th>
                <Th>{t('audit.actor')}</Th>
                <Th>{t('audit.action')}</Th>
                <Th>{t('audit.target')}</Th>
                <Th>{t('audit.app')}</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {page.data.items.map((entry) => (
                <tr key={entry.id}>
                  <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                    {formatDateTime(entry.created_at)}
                  </Td>
                  <Td>
                    <Badge>{entry.actor_type}</Badge>
                    {entry.actor_id && (
                      <span className="ml-2 font-mono text-xs text-zinc-500">
                        {entry.actor_id.slice(0, 8)}
                      </span>
                    )}
                  </Td>
                  <Td className="font-mono text-xs">{entry.action}</Td>
                  <Td>
                    <span className="text-zinc-600 dark:text-zinc-400">{entry.target_type}</span>
                    {entry.target_id && (
                      <span className="ml-1 font-mono text-xs text-zinc-500">
                        {entry.target_id.slice(0, 8)}
                      </span>
                    )}
                  </Td>
                  <Td>{appName(entry.app_id)}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        <div className="mt-4 flex justify-between">
          <Button
            variant="secondary"
            disabled={cursors.length === 0}
            onClick={() => setCursors((c) => c.slice(0, -1))}
          >
            {t('common.previous')}
          </Button>
          <Button
            variant="secondary"
            disabled={!page.data?.next_cursor}
            onClick={() => {
              const next = page.data?.next_cursor;
              if (next) setCursors((c) => [...c, next]);
            }}
          >
            {t('common.next')}
          </Button>
        </div>
      </Card>
    </div>
  );
}
