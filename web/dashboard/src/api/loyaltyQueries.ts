import { infiniteQueryOptions, queryOptions } from '@tanstack/react-query';

import { api, unwrap } from './client';
import { keys } from './queries';
import type {
  ArchiveStatus,
  CouponInstancePage,
  CouponState,
  CouponTemplate,
  StampCard,
  StampProgress,
} from './types';

export const couponTemplatesQuery = (appId: string, status?: ArchiveStatus) =>
  queryOptions({
    queryKey: keys.couponTemplates(appId, status),
    queryFn: async (): Promise<CouponTemplate[]> =>
      unwrap(
        await api.GET('/v1/dashboard/apps/{appId}/coupon-templates', {
          params: { path: { appId }, query: status ? { status } : {} },
        }),
      ),
  });

export const stampCardsQuery = (appId: string, status?: ArchiveStatus) =>
  queryOptions({
    queryKey: keys.stampCards(appId, status),
    queryFn: async (): Promise<StampCard[]> =>
      unwrap(
        await api.GET('/v1/dashboard/apps/{appId}/stamp-cards', {
          params: { path: { appId }, query: status ? { status } : {} },
        }),
      ),
  });

/** A member's coupons, newest first, optionally one effective state, page by page. */
export const memberCouponsQuery = (memberId: string, state?: CouponState) =>
  infiniteQueryOptions({
    queryKey: keys.memberCoupons(memberId, state),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }): Promise<CouponInstancePage> =>
      unwrap(
        await api.GET('/v1/dashboard/members/{memberId}/coupons', {
          params: {
            path: { memberId },
            query: {
              limit: 25,
              ...(state ? { state } : {}),
              ...(pageParam ? { cursor: pageParam } : {}),
            },
          },
        }),
      ),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

export const memberStampsQuery = (memberId: string) =>
  queryOptions({
    queryKey: keys.memberStamps(memberId),
    queryFn: async (): Promise<StampProgress[]> =>
      unwrap(
        await api.GET('/v1/dashboard/members/{memberId}/stamps', {
          params: { path: { memberId } },
        }),
      ),
  });
