/**
 * MiniLoyal SDK for LINE MINI Apps.
 *
 * `MiniLoyal.init()` runs `liff.init()`, sends the member to LINE login when
 * needed, exchanges the LIFF ID token for a one-hour MiniLoyal session and
 * keeps that session fresh. The session lives in memory only.
 */
export { MiniLoyal, newKey } from './client';
export type {
  AppPublic,
  Health,
  LineLoginRequest,
  Member,
  MemberExport,
  MemberSession,
  MemberUpdate,
  MemberCard,
  MiniLoyalOptions,
  MutationOptions,
  PageOptions,
  PointsHistoryPage,
  PointsKind,
  PointsSummary,
  PointsTransaction,
  Session,
  SessionState,
  StopWatching,
} from './client';
export { MiniLoyalError, isMiniLoyalError } from './errors';
export type { ApiError, SdkErrorCode } from './errors';
export type { LiffLike } from './liff';
export type { components, paths } from './api/schema';
