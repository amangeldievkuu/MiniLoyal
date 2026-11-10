import type { components } from './schema';

type Schemas = components['schemas'];

export type ApiErrorBody = Schemas['Error'];
export type StaffSession = Schemas['StaffSession'];
export type StaffMe = Schemas['StaffMe'];
export type ScanResult = Schemas['ScanResult'];
export type ScannedMember = Schemas['ScannedMember'];
export type PointsTransaction = Schemas['PointsTransaction'];
export type StaffEarnRequest = Schemas['StaffEarnRequest'];
export type StaffRedeemRequest = Schemas['StaffRedeemRequest'];
