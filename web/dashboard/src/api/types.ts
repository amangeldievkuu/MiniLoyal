import type { components } from './schema';

type Schemas = components['schemas'];

export type ApiErrorBody = Schemas['Error'];
export type Session = Schemas['Session'];
export type LoginRequest = Schemas['LoginRequest'];
export type LoginResult = Schemas['LoginResult'];
export type MfaChallenge = Schemas['MfaChallenge'];
export type RegisterRequest = Schemas['RegisterRequest'];
export type PasswordChangeRequest = Schemas['PasswordChangeRequest'];
export type User = Schemas['User'];
export type Me = Schemas['Me'];
export type Membership = Schemas['Membership'];
export type Tenant = Schemas['Tenant'];
export type TenantType = Schemas['TenantType'];
export type TenantUpdate = Schemas['TenantUpdate'];
export type Role = Schemas['Role'];
export type TeamMember = Schemas['TeamMember'];
export type TeamMemberAdd = Schemas['TeamMemberAdd'];
export type App = Schemas['App'];
export type AppCreate = Schemas['AppCreate'];
export type AppUpdate = Schemas['AppUpdate'];
export type AppStatus = Schemas['AppStatus'];
export type AppChannel = Schemas['AppChannel'];
export type AppChannelPut = Schemas['AppChannelPut'];
export type ChannelEnvironment = Schemas['ChannelEnvironment'];
export type Store = Schemas['Store'];
export type StoreCreate = Schemas['StoreCreate'];
export type StoreUpdate = Schemas['StoreUpdate'];
export type Staff = Schemas['Staff'];
export type StaffCreate = Schemas['StaffCreate'];
export type StaffCreated = Schemas['StaffCreated'];
export type StaffUpdate = Schemas['StaffUpdate'];
export type StaffRole = Schemas['StaffRole'];
export type ApiKey = Schemas['ApiKey'];
export type ApiKeyCreate = Schemas['ApiKeyCreate'];
export type ApiKeyCreated = Schemas['ApiKeyCreated'];
export type ApiKeyScope = Schemas['ApiKeyScope'];
export type AuditEntry = Schemas['AuditEntry'];
export type AuditPage = Schemas['AuditPage'];
export type TotpEnrollment = Schemas['TotpEnrollment'];
export type TotpConfirmed = Schemas['TotpConfirmed'];

export const CHANNEL_ENVIRONMENTS: ChannelEnvironment[] = ['developing', 'review', 'published'];
export const API_KEY_SCOPES: ApiKeyScope[] = [
  'members:read',
  'members:write',
  'points:write',
  'coupons:write',
  'messages:send',
  'webhooks:manage',
  'config:read',
];
export const STAFF_ROLES: StaffRole[] = ['cashier', 'manager'];
export const APP_STATUSES: AppStatus[] = ['active', 'disabled'];
export const LOCALES = ['zh-TW', 'en', 'ja', 'th'] as const;
