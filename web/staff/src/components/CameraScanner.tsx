import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Alert, Button } from './ui';

interface Controls {
  stop: () => void;
}

/**
 * Browser fallback when the LINE client has no native scanner: reads QR codes
 * from the camera with @zxing/browser (loaded on demand). The stream stops
 * after the first result and on unmount.
 */
export function CameraScanner({
  onResult,
  onCancel,
}: {
  onResult: (text: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const video = useRef<HTMLVideoElement>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let controls: Controls | undefined;
    let done = false;
    const stop = () => {
      done = true;
      controls?.stop();
    };
    void (async () => {
      try {
        const { BrowserQRCodeReader } = await import('@zxing/browser');
        if (done || !video.current) return;
        const reader = new BrowserQRCodeReader();
        controls = await reader.decodeFromVideoDevice(
          undefined,
          video.current,
          (result, _err, c) => {
            if (!result || done) return;
            done = true;
            c.stop();
            onResult(result.getText());
          },
        );
        if (done) controls.stop();
      } catch {
        if (!done) setFailed(true);
      }
    })();
    return stop;
  }, [onResult]);

  return (
    <div className="flex flex-col gap-3">
      <h2 className="text-lg font-semibold">{t('scan.cameraTitle')}</h2>
      <p className="text-sm text-zinc-600 dark:text-zinc-400">{t('scan.cameraHelp')}</p>
      {failed ? (
        <Alert tone="danger">{t('scan.cameraError')}</Alert>
      ) : (
        <video
          ref={video}
          muted
          playsInline
          className="aspect-square w-full rounded-2xl bg-black object-cover"
        />
      )}
      <Button variant="secondary" onClick={onCancel}>
        {t('common.cancel')}
      </Button>
    </div>
  );
}
