import { useState } from 'react';
import { useTranslation } from 'react-i18next';

const DEFAULT_MAX = 999_999_999;

/** Applies one keypad key to a digit string; a key that would pass max is ignored. */
export function pressKey(value: string, key: string, max = DEFAULT_MAX): string {
  if (key === 'clear') return '';
  if (key === 'back') return value.slice(0, -1);
  const next = ((value === '0' ? '' : value) + key).replace(/^0+(?=\d)/, '');
  return Number(next) > max ? value : next;
}

/**
 * Accepts typed or pasted text only when all of it fits: an amount above max
 * is refused as a whole (null), never truncated into a different amount.
 */
export function acceptInput(raw: string, max = DEFAULT_MAX): string | null {
  const digits = raw.replace(/\D/g, '');
  return digits !== '' && Number(digits) > max ? null : digits;
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
  max = DEFAULT_MAX,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  prefix?: string;
  /** The largest amount the API accepts for this field. */
  max?: number;
}) {
  const { t } = useTranslation();
  const [refused, setRefused] = useState(false);
  const keys = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '00', '0', 'back'];

  function set(next: string | null) {
    setRefused(next === null);
    if (next !== null) onChange(next);
  }

  function press(k: string) {
    const digit = k !== 'back' && k !== 'clear';
    const over = digit && Number((value === '0' ? '' : value) + k) > max;
    set(over ? null : pressKey(value, k, max));
  }

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
          aria-describedby={`${id}-max`}
          onChange={(e) => set(acceptInput(e.target.value, max))}
          className="min-h-16 w-full bg-transparent text-right font-mono text-4xl tabular-nums outline-none"
        />
      </div>
      <p
        id={`${id}-max`}
        aria-live="polite"
        className="min-h-5 text-sm text-red-700 dark:text-red-400"
      >
        {refused ? t('keypad.tooLarge', { max: max.toLocaleString() }) : ''}
      </p>
      <div className="grid grid-cols-3 gap-2">
        {keys.map((k) => (
          <button
            key={k}
            type="button"
            onClick={() => press(k)}
            aria-label={k === 'back' ? t('keypad.backspace') : undefined}
            className="min-h-14 rounded-xl bg-zinc-200 text-2xl font-semibold active:bg-zinc-300 dark:bg-zinc-800 dark:active:bg-zinc-700"
          >
            {k === 'back' ? '⌫' : k}
          </button>
        ))}
      </div>
      <button
        type="button"
        onClick={() => set('')}
        className="min-h-12 self-end rounded-xl px-4 text-base text-zinc-700 underline dark:text-zinc-300"
      >
        {t('keypad.clear')}
      </button>
    </div>
  );
}
