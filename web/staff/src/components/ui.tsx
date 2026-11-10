import type { ButtonHTMLAttributes, ReactNode } from 'react';

type Variant = 'primary' | 'secondary' | 'danger' | 'ghost';

const variants: Record<Variant, string> = {
  primary:
    'bg-brand-600 text-white hover:bg-brand-700 disabled:bg-zinc-300 disabled:text-zinc-600 dark:disabled:bg-zinc-800 dark:disabled:text-zinc-400',
  secondary:
    'border border-zinc-300 bg-white text-zinc-900 hover:bg-zinc-100 disabled:opacity-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100 dark:hover:bg-zinc-800',
  danger: 'bg-red-700 text-white hover:bg-red-800 disabled:opacity-50',
  ghost: 'text-zinc-700 hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800',
};

/** A button with a touch target of at least 48 px. */
export function Button({
  variant = 'primary',
  size = 'md',
  className = '',
  type = 'button',
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: 'md' | 'lg' }) {
  const sizing = size === 'lg' ? 'min-h-16 px-6 text-xl' : 'min-h-12 px-4 text-base';
  return (
    <button
      type={type}
      className={`inline-flex items-center justify-center gap-2 rounded-xl font-semibold transition-colors disabled:cursor-not-allowed ${sizing} ${variants[variant]} ${className}`}
      {...rest}
    />
  );
}

type Tone = 'neutral' | 'success' | 'warning' | 'danger';

const tones: Record<Tone, string> = {
  neutral:
    'border-zinc-300 bg-white text-zinc-900 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100',
  success: 'border-green-700 bg-green-50 text-green-950 dark:bg-green-950 dark:text-green-50',
  warning: 'border-amber-700 bg-amber-50 text-amber-950 dark:bg-amber-950 dark:text-amber-50',
  danger: 'border-red-700 bg-red-50 text-red-950 dark:bg-red-950 dark:text-red-50',
};

/** A message box. role="alert" for errors so screen readers announce them. */
export function Alert({ tone = 'neutral', children }: { tone?: Tone; children: ReactNode }) {
  return (
    <div
      role={tone === 'danger' || tone === 'warning' ? 'alert' : 'status'}
      className={`rounded-xl border-2 px-4 py-3 text-base ${tones[tone]}`}
    >
      {children}
    </div>
  );
}

export function Badge({ tone = 'neutral', children }: { tone?: Tone; children: ReactNode }) {
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2.5 py-0.5 text-sm font-medium ${tones[tone]}`}
    >
      {children}
    </span>
  );
}

export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return (
    <section
      className={`rounded-2xl border border-zinc-200 bg-white p-4 shadow-sm dark:border-zinc-800 dark:bg-zinc-900 ${className}`}
    >
      {children}
    </section>
  );
}

/** A centered full-screen message with an optional action. */
export function Screen({ children }: { children: ReactNode }) {
  return (
    <main className="mx-auto flex min-h-screen w-full max-w-md flex-col justify-center gap-4 px-4 py-6">
      {children}
    </main>
  );
}
