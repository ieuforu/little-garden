import { useEffect, useRef, useState } from "react";

interface TurnstileAPI {
  render(element: HTMLElement, options: Record<string, unknown>): string;
  remove(id: string): void;
  reset(id: string): void;
}
declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}
let scriptPromise: Promise<void> | undefined;
function loadScript() {
  if (window.turnstile) return Promise.resolve();
  scriptPromise ??= new Promise<void>((resolve, reject) => {
    const script = document.createElement("script");
    script.src =
      "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
    script.async = true;
    script.onload = () => resolve();
    script.onerror = () => {
      script.remove();
      scriptPromise = undefined;
      reject(new Error("verification_unavailable"));
    };
    document.head.append(script);
  });
  return scriptPromise;
}

export function Turnstile({
  siteKey,
  onToken,
}: {
  siteKey: string;
  onToken: (token: string) => void;
}) {
  const container = useRef<HTMLDivElement>(null);
  const callback = useRef(onToken);
  callback.current = onToken;
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true,
      widget: string | undefined;
    const timer = setTimeout(() => {
      if (active && !widget) setError(true);
    }, 12_000);
    void loadScript()
      .then(() => {
        if (!active || !container.current || !window.turnstile) return;
        clearTimeout(timer);
        widget = window.turnstile.render(container.current, {
          sitekey: siteKey,
          action: "plant",
          theme: "light",
          language: "zh-cn",
          callback: (token: string) => {
            if (active) callback.current(token);
          },
          "error-callback": () => {
            if (active) setError(true);
          },
          "expired-callback": () => {
            if (widget) window.turnstile?.reset(widget);
          },
        });
      })
      .catch(() => {
        if (active) setError(true);
      });
    return () => {
      active = false;
      clearTimeout(timer);
      if (widget) window.turnstile?.remove(widget);
    };
  }, [siteKey, attempt]);
  return (
    <div className="verification">
      <div ref={container} />
      {error && (
        <p role="alert">
          验证暂时没加载好。
          <button
            className="text-button"
            onClick={() => {
              setError(false);
              setAttempt((value) => value + 1);
            }}
          >
            重新加载
          </button>
        </p>
      )}
    </div>
  );
}
