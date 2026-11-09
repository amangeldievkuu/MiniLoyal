import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { asApiError } from '../api/client';
import { Alert, Button, Dialog } from './ui';

/** Renders an API or unexpected error with its request ID for support. */
export function ErrorAlert({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (!error) return null;
  const apiError = asApiError(error);
  let message = t('errors.generic');
  if (apiError) {
    const retry = apiError.details?.['retry_after_seconds'];
    message =
      apiError.status === 429 && typeof retry === 'number'
        ? t('errors.rateLimited', { seconds: retry })
        : apiError.status === 0
          ? t('errors.network')
          : apiError.message;
  }
  return (
    <Alert tone="danger">
      <p>{message}</p>
      {apiError?.requestId && (
        <p className="mt-1 text-xs opacity-75">
          {t('common.requestId')}: {apiError.requestId}
        </p>
      )}
    </Alert>
  );
}

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** Copies text to the clipboard and confirms briefly. */
export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="secondary"
      onClick={() => {
        void copyText(text).then((ok) => {
          setCopied(ok);
          if (ok) setTimeout(() => setCopied(false), 2000);
        });
      }}
    >
      {copied ? t('common.copied') : (label ?? t('common.copy'))}
    </Button>
  );
}

/** Shows a secret exactly once, with copy and a single close action. */
export function ShowOnceDialog({
  open,
  title,
  help,
  value,
  onClose,
}: {
  open: boolean;
  title: ReactNode;
  help: ReactNode;
  value: string | string[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const values = Array.isArray(value) ? value : [value];
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      description={help}
      footer={
        <>
          <CopyButton
            text={values.join('\n')}
            label={values.length > 1 ? t('me.totp.copyAll') : undefined}
          />
          <Button onClick={onClose}>{t('common.done')}</Button>
        </>
      }
    >
      <Alert tone="warning">{t('common.showOnceHelp')}</Alert>
      <ul className="mt-3 space-y-1 rounded-md bg-zinc-100 p-3 font-mono text-sm break-all dark:bg-zinc-800">
        {values.map((v) => (
          <li key={v}>{v}</li>
        ))}
      </ul>
    </Dialog>
  );
}

/** Asks before a destructive action. */
export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  onConfirm,
  onCancel,
  pending,
  error,
}: {
  open: boolean;
  title: ReactNode;
  body: ReactNode;
  confirmLabel: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
  pending?: boolean;
  error?: unknown;
}) {
  const { t } = useTranslation();
  return (
    <Dialog
      open={open}
      onClose={onCancel}
      title={title}
      footer={
        <>
          <Button variant="secondary" onClick={onCancel} disabled={pending}>
            {t('common.cancel')}
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={pending}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <p className="text-sm">{body}</p>
      {error ? (
        <div className="mt-3">
          <ErrorAlert error={error} />
        </div>
      ) : null}
    </Dialog>
  );
}

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 className="text-xl font-semibold sm:text-2xl">{title}</h1>
        {description && (
          <p className="mt-1 max-w-2xl text-sm text-zinc-600 dark:text-zinc-400">{description}</p>
        )}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  );
}

export function ReadOnlyNotice() {
  const { t } = useTranslation();
  return <Alert className="mb-4">{t('common.readOnly')}</Alert>;
}

export function EmptyState({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-md border border-dashed border-zinc-300 p-6 text-center text-sm text-zinc-500 dark:border-zinc-700 dark:text-zinc-400">
      {children}
    </p>
  );
}
