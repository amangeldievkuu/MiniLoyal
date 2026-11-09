import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { Store } from '../api/types';
import { StoresView } from '../pages/StoresPage';
import { canManage, grantableRoles, roleIn } from '../lib/roles';

const stores: Store[] = [
  {
    id: 's1',
    app_id: 'a1',
    name: 'Daan',
    code: 'DA',
    address: null,
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
  },
];

describe('role-aware rendering', () => {
  it('hides write actions from staff', () => {
    render(
      <StoresView
        stores={stores}
        role="staff"
        onCreate={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Add store' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Your role can only view this page.');
    expect(screen.getByText('Daan')).toBeInTheDocument();
  });

  it('shows write actions to admins', () => {
    render(
      <StoresView
        stores={stores}
        role="admin"
        onCreate={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    expect(screen.getByRole('button', { name: 'Add store' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument();
  });
});

describe('role helpers', () => {
  const me = {
    user: { id: 'u1', email: 'a@b.c', totp_enabled: false, created_at: '2026-01-01T00:00:00Z' },
    memberships: [
      {
        role: 'admin' as const,
        tenant: {
          id: 't1',
          type: 'brand' as const,
          name: 'T',
          slug: 't',
          plan: 'free',
          created_at: '',
        },
      },
    ],
  };
  it('resolves roles and permissions', () => {
    expect(roleIn(me, 't1')).toBe('admin');
    expect(roleIn(me, 't2')).toBeNull();
    expect(canManage('admin')).toBe(true);
    expect(canManage('staff')).toBe(false);
    expect(grantableRoles('admin')).toEqual(['admin', 'staff']);
    expect(grantableRoles('owner')).toEqual(['owner', 'admin', 'staff']);
    expect(grantableRoles(null)).toEqual([]);
  });
});
