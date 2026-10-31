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
  too_close: "这里已经有一朵花啦，给它留一点生长的空间。",
  garden_full: "这座花园已经盛开了。先一起欣赏，等下一块新花田。",
  garden_paused: "花园正在休息，暂时不能种新花。",
  garden_changed: "园丁开了一块新花田，刷新后再种吧。",
  session_required: "种花凭证过期了，再验证一次就好。",
  session_blocked: "这个种花会话已停用，仍然可以看花。",
  network_error: "网络有点慢，还没确认是否种下。重新连接后会核对。",
  verification_failed: "验证没通过，请关闭后再试一次。",
  invalid_flower: "换一个花田里的位置试试。",
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
          ? `慢一点，再等 ${error.retryAfter || 3} 秒。花园有的是时间。`
          : (messages[error.code] ?? "花园暂时有点忙，稍后再试。"),
      );
    } else setNotice("连接暂时中断，稍后再试。");
  }
  async function submit(req: PlantRequest) {
    if (busy.current) return;
    busy.current = true;
    setPending(req);
    try {
      const result = await plant(req);
      if (!result.duplicate) {
        setMine((value) => value + 1);
        setNotice("种下了。谢谢你，让这里多了一点颜色。");
        if (sound) chime();
      }
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
      setNotice("正在重新连接花园，连上后就可以继续种花。");
      return;
    }
    if (Date.now() < cooldown.current) {
      setNotice("让花园喘口气，稍等几秒再种吧。");
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
      setNotice("换一块空地吧，花朵需要一点生长的空间。");
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
    else setNotice("这块花田有点满，拖动画面找一块空地吧。");
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
          <Bloom size={31} />
          <span>
            little garden<span className="wordmark-dot">.</span>
          </span>
        </a>
        <nav aria-label="主导航">
          <button onClick={() => setDialog("about")}>关于花园</button>
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
            <div className="eyebrow">
              <span className="tiny-sun" aria-hidden="true">
                ✳
              </span>{" "}
              AN OPEN GARDEN, FOR EVERYONE
            </div>
            <h1 id="page-title">
              小小一朵，
              <br />
              <span>也是一个好开始。</span>
            </h1>
          </div>
          <div className="intro-note">
            <span className="note-line" aria-hidden="true"></span>
            <p>
              找一块空地，种下一点颜色。
              <br />
              你和路过的人，会让这里慢慢开满花。
            </p>
            <span className="handwritten">good things take a little time.</span>
          </div>
        </section>

        <section className="garden-section" aria-label="共享花园">
          <div className="garden-topline">
            <div>
              <span className="garden-index">
                GARDEN No. {String(snapshot?.garden.id ?? 1).padStart(3, "0")}
              </span>
              <span className="garden-name">此刻，正在生长</span>
            </div>
            <div
              className={`connection ${connected ? "is-connected" : ""}`}
              role="status"
            >
              <span />
              {connected
                ? "与花园相连"
                : connection === "offline"
                  ? "暂时离线"
                  : "正在连接花园"}
            </div>
          </div>
          <div className="garden-stage">
            <div className="garden-corner" aria-hidden="true">
              <span>留一点空白</span>
              <span>给偶然发生的美好</span>
            </div>
            <div className="flower-count">
              <strong>{snapshot ? String(count).padStart(2, "0") : "—"}</strong>
              <span>朵花在这里</span>
            </div>
            <canvas
              ref={canvas}
              className="garden-canvas"
              tabIndex={0}
              role="application"
              aria-label={`共享花园，${count} 朵花。点击空地种花，拖动平移；键盘方向键移动位置，回车种花。也可使用下方帮我选个位置按钮。`}
              aria-describedby="garden-help"
            />
            {(!snapshot || canvasError) && (
              <div className="garden-loading" role="status">
                <Bloom size={46} />
                <p>
                  {canvasError
                    ? "这个浏览器暂时无法画出花园。"
                    : loadError
                      ? "花园暂时没连上，正在重试…"
                      : "把花园慢慢铺开…"}
                </p>
              </div>
            )}
            {snapshot && count === 0 && (
              <div className="empty-garden">
                <p>一块新花田。</p>
                <span>第一朵花，会是什么颜色？</span>
              </div>
            )}
            {snapshot?.garden.paused && (
              <div className="pause-label">花园正在休息 · 可以继续看花</div>
            )}
            <div className="canvas-controls" aria-label="花园视角">
              <button
                aria-label="放大花园"
                title="放大"
                onClick={() => renderer.current?.zoom(1.25)}
              >
                +
              </button>
              <button
                aria-label="缩小花园"
                title="缩小"
                onClick={() => renderer.current?.zoom(0.8)}
              >
                −
              </button>
              <button
                className="fit-button"
                aria-label="查看整座花园"
                title="查看整座花园"
                onClick={() => renderer.current?.fit()}
              >
                <svg
                  width="15"
                  height="15"
                  viewBox="0 0 20 20"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.3"
                  aria-hidden="true"
                >
                  <path d="M7 3H3v4m10-4h4v4M3 13v4h4m6 0h4v-4" />
                </svg>
              </button>
            </div>
            <span className="field-note" aria-hidden="true">
              a place to grow, together.
            </span>
            <div className="plant-toolbar">
              <div
                className="seed-picker"
                role="group"
                aria-label="选择花的颜色"
              >
                {palettes.map((p, i) => (
                  <button
                    key={p.name}
                    className={i === palette ? "seed selected" : "seed"}
                    aria-label={p.name}
                    aria-pressed={i === palette}
                    title={p.name}
                    disabled={!!pending}
                    onClick={() => setPalette(i)}
                  >
                    <Bloom color={p.color} size={30} />
                  </button>
                ))}
              </div>
              <span className="toolbar-divider" />
              <button
                className="plant-button"
                disabled={
                  !connected ||
                  !!pending ||
                  verifying ||
                  !!snapshot?.garden.paused ||
                  (!!snapshot &&
                    snapshot.garden.planted >= snapshot.garden.capacity)
                }
                onClick={chooseSpot}
              >
                <ShuffleIcon />
                <span>{pending ? "正在生长…" : "帮我选个位置"}</span>
              </button>
            </div>
            <div
              className={`toast ${notice ? "visible" : ""}`}
              role="status"
              aria-live="polite"
            >
              {notice}
            </div>
          </div>
          <div className="garden-caption">
            <p id="garden-help">
              <span aria-hidden="true">↖</span> 点击空地种花 · 拖动探索 ·
              花会留在这里
            </p>
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
                    setNotice("这个浏览器暂时不能播放声音。");
                  }
                } else setSound(false);
              }}
            >
              <SoundIcon enabled={sound} />
              <span>声音{sound ? "开" : "关"}</span>
            </button>
          </div>
        </section>

        <section className="afterword" aria-label="花园寄语">
          <span className="small-leaf" aria-hidden="true">
            ❧
          </span>
          <p>
            {mine > 0
              ? `今天，你为这里种下了 ${mine} 朵花。`
              : "不必做一件很大的事。"}
            <span>
              {mine > 0
                ? "小小的善意，也会慢慢长大。"
                : "留下一朵花，就已经很好。"}
            </span>
          </p>
        </section>
      </main>

      <footer className="site-footer">
        <span>
          A little experiment by{" "}
          <a href="https://github.com/ieuforu" target="_blank" rel="noreferrer">
            ieuforu
          </a>
        </span>
        <span>
          Made of tiny, good things{" "}
          <span className="footer-star" aria-hidden="true">
            ✳
          </span>
        </span>
      </footer>

      {dialog === "about" && (
        <Dialog title="一起，种一点美好。" onClose={closeDialog}>
          <p>
            这里是一块所有人共享的小花田。选一个喜欢的颜色，点在空白处，一朵只属于这次相遇的花就会长出来。
          </p>
          <p>
            花会保存下来，也会实时出现在其他人的画面里。花园最初的 16
            朵由程序播下，之后的颜色，交给每一个路过的人。
          </p>
          <p>
            拖动可以探索，双指或右下角按钮可以缩放。用键盘时，方向键移动，回车种花；也可以让我们帮你选个位置。
          </p>
          <div className="about-footnote">
            无需注册。首次种花有一次轻量验证；匿名种花凭证保留 24
            小时。花田位置是画布坐标，不是你的地理位置。
          </div>
          <a
            className="about-source"
            href="https://github.com/ieuforu/little-garden"
            target="_blank"
            rel="noreferrer"
          >
            看看这个小实验的代码 ↗
          </a>
        </Dialog>
      )}
      {dialog === "verify" && (
        <Dialog title="给花园一点保护。" onClose={closeDialog}>
          <p>第一次种花前，做一次小小的验证。之后就可以安心播种啦。</p>
          {config && (
            <Turnstile
              siteKey={config.siteKey}
              onToken={(token) => {
                void verified(token);
              }}
            />
          )}
          <p className="verification-status" role="status">
            {verifying
              ? "正在确认，种子已经准备好了…"
              : "验证完成后，会在你刚才选择的位置种下花。"}
          </p>
        </Dialog>
      )}
    </div>
  );
}
