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
export type Member = Schemas['Member'];
export type MemberDetail = Schemas['MemberDetail'];
export type MemberPage = Schemas['MemberPage'];
export type MemberExport = Schemas['MemberExport'];
export type Consent = Schemas['Consent'];
export type Gender = Schemas['Gender'];
export type Invitation = Schemas['Invitation'];
export type InvitationCreate = Schemas['InvitationCreate'];
export type InvitationCreated = Schemas['InvitationCreated'];
export type AcceptInvitationRequest = Schemas['AcceptInvitationRequest'];
export type SecurityEvent = Schemas['SecurityEvent'];
export type SecurityEventPage = Schemas['SecurityEventPage'];
export type PointsRules = Schemas['PointsRules'];
export type PointsRulesPut = Schemas['PointsRulesPut'];
export type PointsSummary = Schemas['PointsSummary'];
export type PointsTransaction = Schemas['PointsTransaction'];
export type PointsHistoryPage = Schemas['PointsHistoryPage'];
export type PointsAdjustment = Schemas['PointsAdjustment'];
export type PointsKind = Schemas['PointsKind'];
export type MemberCardRotated = Schemas['MemberCardRotated'];

export type CouponTemplate = Schemas['CouponTemplate'];
export type CouponTemplateCreate = Schemas['CouponTemplateCreate'];
export type CouponTemplateUpdate = Schemas['CouponTemplateUpdate'];
export type CouponTemplateClearable = Schemas['CouponTemplateClearable'];
export type CouponType = Schemas['CouponType'];
export type CouponState = Schemas['CouponState'];
export type CouponInstance = Schemas['CouponInstance'];
export type CouponInstancePage = Schemas['CouponInstancePage'];
export type ArchiveStatus = Schemas['ArchiveStatus'];
export type StampCard = Schemas['StampCard'];
export type StampCardCreate = Schemas['StampCardCreate'];
export type StampCardUpdate = Schemas['StampCardUpdate'];
export type StampProgress = Schemas['StampProgress'];
export type StampResult = Schemas['StampResult'];
export type StampAdjustment = Schemas['StampAdjustment'];

export const COUPON_TYPES: CouponType[] = ['amount_off', 'percent_off', 'free_item'];
export const COUPON_STATES: CouponState[] = ['available', 'redeemed', 'expired', 'voided'];
export const ARCHIVE_STATUSES: ArchiveStatus[] = ['active', 'archived'];

export const CHANNEL_ENVIRONMENTS: ChannelEnvironment[] = [
  'developing',
  'review',
  'published',
  'staff',
];
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
