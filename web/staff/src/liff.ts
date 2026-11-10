import liff from '@line/liff';

/** The LIFF SDK; one import point so tests can mock '@line/liff'. */
export { liff };

/** Whether this LINE client can scan with the native scanner. */
export function canScanNatively(): boolean {
  try {
    return liff.isApiAvailable('scanCodeV2');
  } catch {
    return false;
  }
}
