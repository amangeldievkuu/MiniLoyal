import { useTranslation } from 'react-i18next';

const MAX_DIGITS = 9;

/** Applies one keypad key to a digit string. */
export function pressKey(value: string, key: string): string {
  if (key === 'clear') return '';
  if (key === 'back') return value.slice(0, -1);
  const next = (value === '0' ? '' : value) + key;
  return next.length > MAX_DIGITS ? value : next.replace(/^0+(?=\d)/, '');
}

/**
 * A big numeric input with an on-screen keypad for one-handed use. The
 * input stays editable so a hardware keyboard or a test can type into it.
 */
export function Keypad({
  id,
  label,
  value,
  onChange,
  prefix,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  prefix?: string;
}) {
  const { t } = useTranslation();
  const keys = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '00', '0', 'back'];
  return (
    <div className="flex flex-col gap-3">
      <label htmlFor={id} className="text-base font-semibold">
        {label}
      </label>
      <div className="flex items-center gap-2 rounded-xl border-2 border-zinc-400 bg-white px-4 dark:border-zinc-600 dark:bg-zinc-900">
        {prefix && <span className="text-2xl text-zinc-500">{prefix}</span>}
        <input
          id={id}
          inputMode="numeric"
          pattern="[0-9]*"
          autoComplete="off"
          value={value}
          onChange={(e) => onChange(e.target.value.replace(/\D/g, '').slice(0, MAX_DIGITS))}
          className="min-h-16 w-full bg-transparent text-right font-mono text-4xl tabular-nums outline-none"
        />
      </div>
      <div className="grid grid-cols-3 gap-2">
        {keys.map((k) => (
          <button
            key={k}
            type="button"
            onClick={() => onChange(pressKey(value, k))}
            aria-label={k === 'back' ? t('keypad.backspace') : undefined}
            className="min-h-14 rounded-xl bg-zinc-200 text-2xl font-semibold active:bg-zinc-300 dark:bg-zinc-800 dark:active:bg-zinc-700"
          >
            {k === 'back' ? '⌫' : k}
          </button>
        ))}
      </div>
      <button
        type="button"
        onClick={() => onChange('')}
        className="min-h-12 self-end rounded-xl px-4 text-base text-zinc-700 underline dark:text-zinc-300"
      >
        {t('keypad.clear')}
      </button>
    </div>
  );
}
