import type { Me, Role } from '../api/types';

/** The caller's role in a tenant, or null when they are not a member. */
export function roleIn(me: Me | null, tenantId: string | undefined): Role | null {
  if (!me || !tenantId) return null;
  return me.memberships.find((m) => m.tenant.id === tenantId)?.role ?? null;
}

/** Owners and admins manage apps, stores, staff, keys and channels. */
export function canManage(role: Role | null): boolean {
  return role === 'owner' || role === 'admin';
}

/** Roles a user with `actor` role may grant to or set on others. */
export function grantableRoles(actor: Role | null): Role[] {
  if (actor === 'owner') return ['owner', 'admin', 'staff'];
  if (actor === 'admin') return ['admin', 'staff'];
  return [];
}
