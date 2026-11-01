import { useEffect, useRef, useState } from "react";
import { Dialog } from "./components/Dialog";
import { Turnstile } from "./components/Turnstile";
import { GardenRenderer } from "./garden/renderer";
import { canPlant, palettes } from "./garden/model";
import type { PlantRequest, Point } from "./garden/model";
import { APIError, useGarden } from "./realtime/useGarden";

function Bloom({
  color = "#d98d70",
  size = 30,
}: {
  color?: string;
  size?: number;
}) {
  return (
    <svg width={size} height={size} viewBox="0 0 40 40" aria-hidden="true">
      <g fill={color}>
        {Array.from({ length: 8 }, (_, i) => (
          <ellipse
            key={i}
            cx="20"
            cy="10"
            rx="5"
            ry="9"
            transform={`rotate(${i * 45} 20 20)`}
          />
        ))}
      </g>
      <circle cx="20" cy="20" r="4.5" fill="#f5e6b9" />
    </svg>
  );
}
function SoundIcon({ enabled }: { enabled: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      aria-hidden="true"
    >
      <path d="M11 5 6 9H3v6h3l5 4V5Z" />
      {enabled ? (
        <>
          <path d="M15 8a7 7 0 0 1 0 8M18 5a11 11 0 0 1 0 14" />
        </>
      ) : (
        <path d="m16 9 5 6m0-6-5 6" />
      )}
    </svg>
  );
}
function ShuffleIcon() {
  return (
    <svg
      width="17"
      height="17"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      aria-hidden="true"
    >
      <path d="M3 6h3c5 0 6 12 11 12h4m-4-4 4 4-4 4M3 18h3c5 0 6-12 11-12h4m-4-4 4 4-4 4" />
    </svg>
  );
}

const messages: Record<string, string> = {
  too_close: "这里离其他花太近，换一块空地试试。",
  garden_full: "这块花田已经种满，暂时不能再种。",
  garden_paused: "花园暂时停止种花，仍然可以浏览。",
  garden_changed: "花田已更新，请刷新后再种。",
  session_required: "验证已过期，请重新验证。",
  session_blocked: "当前会话已暂停种花，仍然可以浏览。",
  network_error: "连接中断，重新连接后会确认这朵花是否已保存。",
  verification_failed: "验证未通过，请再试一次。",
  invalid_flower: "请在花田范围内选择空地。",
};

export default function App() {
  const {
    snapshot,
    config,
    connection,
    loadError,
    authenticate,
    plant,
    expiresAt,
  } = useGarden();
  const canvas = useRef<HTMLCanvasElement>(null);
  const renderer = useRef<GardenRenderer | null>(null);
  const action = useRef<(point: Point) => void>(() => {});
  const pendingGate = useRef<PlantRequest | null>(null);
  const busy = useRef(false);
  const verificationBusy = useRef(false);
  const [palette, setPalette] = useState(0);
  const [pending, setPending] = useState<Point | null>(null);
  const [notice, setNotice] = useState("");
  const [dialog, setDialog] = useState<"about" | "verify" | null>(null);
  const [sound, setSound] = useState(false);
  const audio = useRef<AudioContext | null>(null);
  const cooldown = useRef(0);
  const [mine, setMine] = useState(0);
  const [verifying, setVerifying] = useState(false);
  const [canvasError, setCanvasError] = useState(false);

  useEffect(() => {
    if (!canvas.current) return;
    try {
      renderer.current = new GardenRenderer(canvas.current, (point) =>
        action.current(point),
      );
    } catch {
      setCanvasError(true);
    }
    return () => {
      renderer.current?.dispose();
      renderer.current = null;
    };
  }, []);
  useEffect(() => {
    if (snapshot) renderer.current?.setData(snapshot.flowers, snapshot.garden);
  }, [snapshot]);
  useEffect(() => {
    renderer.current?.setPalette(palette);
  }, [palette]);
  useEffect(() => {
    renderer.current?.setPending(pending, palette);
  }, [pending, palette]);
  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(() => setNotice(""), 6500);
    return () => clearTimeout(timer);
  }, [notice]);
  useEffect(
    () => () => {
      void audio.current?.close();
    },
    [],
  );

  function chime() {
    const ctx = audio.current;
    if (!ctx || ctx.state !== "running") return;
    const now = ctx.currentTime;
    [523.25, 659.25, 783.99].forEach((frequency, i) => {
      const oscillator = ctx.createOscillator(),
        gain = ctx.createGain();
      oscillator.frequency.value = frequency;
      gain.gain.setValueAtTime(0, now + i * 0.07);
      gain.gain.linearRampToValueAtTime(0.026, now + i * 0.07 + 0.025);
      gain.gain.exponentialRampToValueAtTime(0.0001, now + 1.1 + i * 0.07);
      oscillator.connect(gain);
      gain.connect(ctx.destination);
      oscillator.start(now + i * 0.07);
      oscillator.stop(now + 1.4 + i * 0.07);
      oscillator.onended = () => {
        oscillator.disconnect();
        gain.disconnect();
      };
    });
  }
  function explain(error: unknown) {
    if (error instanceof APIError) {
      if (error.retryAfter)
        cooldown.current = Date.now() + error.retryAfter * 1000;
      setNotice(
        error.code === "rate_limited"
          ? `请等 ${error.retryAfter || 3} 秒后再种。`
          : (messages[error.code] ?? "暂时无法种花，请稍后重试。"),
      );
    } else setNotice("连接暂时中断，稍后再试。");
  }
  async function submit(req: PlantRequest) {
    if (busy.current) return;
    busy.current = true;
    setPending(req);
    try {
      await plant(req);
      // A confirmed network retry is still one successful action in this visit.
      setMine((value) => value + 1);
      setNotice("种下了，花朵已保存。");
      if (sound) chime();
    } catch (error) {
      explain(error);
    } finally {
      busy.current = false;
      setPending(null);
    }
  }
  action.current = (point) => {
    if (
      !snapshot ||
      !config ||
      busy.current ||
      verificationBusy.current ||
      dialog === "verify"
    )
      return;
    if (connection !== "connected") {
      setNotice("正在重新连接，请连接成功后再种。");
      return;
    }
    if (Date.now() < cooldown.current) {
      setNotice("请稍等几秒后再种。");
      return;
    }
    if (
      snapshot.garden.paused ||
      snapshot.garden.planted >= snapshot.garden.capacity
    ) {
      setNotice(
        messages[snapshot.garden.paused ? "garden_paused" : "garden_full"],
      );
      return;
    }
    if (!canPlant(point, snapshot.flowers, snapshot.garden)) {
      setNotice("这里离其他花太近，换一块空地试试。");
      return;
    }
    const req = {
      ...point,
      palette,
      gardenId: snapshot.garden.id,
      requestId: crypto.randomUUID(),
    };
    if (expiresAt * 1000 > Date.now() + 30_000) {
      void submit(req);
      return;
    }
    pendingGate.current = req;
    if (config.development) {
      void verified("");
    } else setDialog("verify");
  };
  async function verified(token: string) {
    if (verificationBusy.current) return;
    verificationBusy.current = true;
    setVerifying(true);
    try {
      await authenticate(token);
      const req = pendingGate.current;
      pendingGate.current = null;
      setDialog(null);
      if (req) await submit(req);
    } catch (error) {
      setDialog(null);
      pendingGate.current = null;
      explain(error);
    } finally {
      verificationBusy.current = false;
      setVerifying(false);
    }
  }
  function chooseSpot() {
    const point = renderer.current?.randomSpot();
    if (point) action.current(point);
    else setNotice("当前画面没有找到空地，可以拖动或缩小花田再试。");
  }
  function closeDialog() {
    setDialog(null);
    pendingGate.current = null;
  }
  const connected = connection === "connected";
  const count = snapshot?.flowers.length ?? 0;

  return (
    <div className="site-shell">
      <header className="site-header">
        <a href="/" className="wordmark" aria-label="Little Garden 首页">
          <Bloom color="#be6d4b" size={25} />
          <span>little garden</span>
        </a>
        <nav aria-label="主导航">
          <button onClick={() => setDialog("about")}>怎么玩</button>
          <a
            href="https://github.com/ieuforu/little-garden"
            target="_blank"
            rel="noreferrer"
          >
            GitHub <span aria-hidden="true">↗</span>
          </a>
        </nav>
      </header>

      <main>
        <section className="intro" aria-labelledby="page-title">
          <div>
            <h1 id="page-title">一起种花</h1>
            <p>选个颜色，点一下空地。</p>
          </div>
          <div
            className={`connection ${connected ? "is-connected" : ""}`}
            role="status"
          >
            <span aria-hidden="true" />
            {connected
              ? "实时同步中"
              : connection === "offline"
                ? "暂时离线"
                : "正在连接…"}
          </div>
        </section>

        <section className="garden-section" aria-label="共享花园">
          <div className="garden-topline">
            <div className="garden-meta">
              <span className="garden-index">
                花田 <span>{String(snapshot?.garden.id ?? 1).padStart(3, "0")}</span>
              </span>
              <span className="meta-divider" aria-hidden="true">/</span>
              <span className="flower-count">
                <strong>{snapshot ? count : "—"}</strong> 朵花
              </span>
            </div>
            <div className="canvas-controls" aria-label="花园视角">
              <button
                aria-label="缩小花园"
                title="缩小"
                onClick={() => renderer.current?.zoom(0.8)}
              >
                −
              </button>
              <button
                aria-label="放大花园"
                title="放大"
                onClick={() => renderer.current?.zoom(1.25)}
              >
                +
              </button>
              <button
                className="fit-button"
                aria-label="查看整座花园"
                title="查看整座花园"
                onClick={() => renderer.current?.fit()}
              >
                <svg width="17" height="17" viewBox="0 0 20 20" fill="none"
                  stroke="currentColor" strokeWidth="1.3" aria-hidden="true">
                  <path d="M7 3H3v4m10-4h4v4M3 13v4h4m6 0h4v-4" />
                </svg>
              </button>
            </div>
          </div>

          <div className="garden-stage">
            <canvas
              ref={canvas}
              className="garden-canvas"
              tabIndex={0}
              role="application"
              aria-label={`共享花园，${count} 朵花。点击空地种花，拖动平移；键盘方向键移动位置，回车种花。也可使用下方随机种一朵按钮。`}
              aria-describedby="garden-help"
            />
            {(!snapshot || canvasError) && (
              <div className="garden-loading" role="status">
                <Bloom color="#be6d4b" size={32} />
                <p>
                  {canvasError
                    ? "浏览器暂时无法显示花田。"
                    : loadError
                      ? "连接失败，正在重试…"
                      : "正在加载花田…"}
                </p>
              </div>
            )}
            {snapshot && count === 0 && (
              <div className="empty-garden">
                <Bloom color={palettes[palette].color} size={32} />
                <p>这里还没有花</p>
                <span>点一块空地，种下第一朵。</span>
              </div>
            )}
            {snapshot?.garden.paused && (
              <div className="pause-label" role="status">暂时停止种花 · 仍可浏览</div>
            )}
            <div
              className={`toast ${notice ? "visible" : ""}`}
              role="status"
              aria-live="polite"
            >
              {notice}
            </div>
          </div>

          <div className="plant-toolbar">
            <div className="seed-picker" role="group" aria-label="选择花的颜色">
              {palettes.map((p, i) => (
                <button
                  key={p.name}
                  className={i === palette ? "seed selected" : "seed"}
                  aria-label={p.name}
                  aria-pressed={i === palette}
                  disabled={!!pending || verifying}
                  onClick={() => setPalette(i)}
                >
                  <Bloom color={p.color} size={28} />
                  <span>{p.name}</span>
                  <span className="seed-selected-mark" aria-hidden="true" />
                </button>
              ))}
            </div>
            <button
              className="plant-button"
              disabled={
                !connected ||
                canvasError ||
                !!pending ||
                verifying ||
                !!snapshot?.garden.paused ||
                (!!snapshot && snapshot.garden.planted >= snapshot.garden.capacity)
              }
              onClick={chooseSpot}
            >
              <ShuffleIcon />
              <span>{pending ? "正在种下…" : "随机种一朵"}</span>
            </button>
          </div>
        </section>

        <div className="garden-caption">
          <p id="garden-help">拖动查看花田<span> · 双指或按钮缩放</span></p>
          <button
            className="sound-toggle"
            aria-pressed={sound}
            onClick={() => {
              if (!sound) {
                try {
                  audio.current ??= new AudioContext();
                  void audio.current.resume();
                  setSound(true);
                } catch {
                  setNotice("浏览器暂时无法播放声音。");
                }
              } else setSound(false);
            }}
          >
            <SoundIcon enabled={sound} />
            <span>声音{sound ? "开" : "关"}</span>
          </button>
        </div>
      </main>

      <footer className="site-footer">
        <a href="https://github.com/ieuforu" target="_blank" rel="noreferrer">
          by ieuforu <span aria-hidden="true">↗</span>
        </a>
        <span>本次种下 <strong>{mine}</strong> 朵</span>
      </footer>

      {dialog === "about" && (
        <Dialog title="关于这座花园" onClose={closeDialog}>
          <p>这是一座共享花园。种下的花会保存，也会实时出现在其他人的画面里。</p>
          <dl className="garden-instructions">
            <div><dt>种花</dt><dd>选一个颜色，点花田里的空地。也可以按“随机种一朵”。</dd></div>
            <div><dt>查看</dt><dd>拖动画面移动，双指或右上角按钮缩放。</dd></div>
            <div><dt>键盘</dt><dd>聚焦花田后，用方向键选位置，回车种花。</dd></div>
          </dl>
          <div className="about-footnote">
            无需注册，首次种花需要人机验证，有效期为 24 小时。
            花园最初的 16 朵花由程序播下，其余由访客种下。
          </div>
          <a className="about-source" href="https://github.com/ieuforu/little-garden"
            target="_blank" rel="noreferrer">
            项目源码 <span aria-hidden="true">↗</span>
          </a>
        </Dialog>
      )}
      {dialog === "verify" && (
        <Dialog title="种花前，验证一下" onClose={closeDialog}>
          <p>用来拦住自动刷花的脚本，无需注册账号。</p>
          {config && (
            <Turnstile
              siteKey={config.siteKey}
              onToken={(token) => { void verified(token); }}
            />
          )}
          <p className="verification-status" role="status">
            {verifying ? "正在验证…" : "通过后，会在刚才选的位置种下花。"}
          </p>
        </Dialog>
      )}
    </div>
  );
}
