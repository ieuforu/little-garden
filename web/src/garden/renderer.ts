import { canPlant, palettes } from "./model";
import type { Flower, Garden, Point } from "./model";

interface Camera {
  x: number;
  y: number;
  scale: number;
}
export class GardenRenderer {
  private ctx: CanvasRenderingContext2D;
  private width = 1;
  private height = 1;
  private dpr = 1;
  private camera: Camera = { x: 1200, y: 800, scale: 0.4 };
  private flowers: Flower[] = [];
  private garden: Garden = {
    id: 1,
    width: 2400,
    height: 1600,
    capacity: 800,
    revision: 0,
    paused: false,
    planted: 0,
  };
  private born = new Map<string, number>();
  private initialized = false;
  private hovered: Point | null = null;
  private pending: Flower | null = null;
  private selected = 0;
  private frame = 0;
  private lastFrame = 0;
  private observer: ResizeObserver;
  private motion = matchMedia("(prefers-reduced-motion: reduce)");
  private pointers = new Map<number, Point>();
  private start: Point | null = null;
  private moved = false;
  private keyboard = false;
  private pinchDistance = 0;
  private disposed = false;

  constructor(
    private canvas: HTMLCanvasElement,
    private onPlant: (point: Point) => void,
  ) {
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("Canvas is unavailable");
    this.ctx = ctx;
    this.observer = new ResizeObserver(this.resize);
    this.observer.observe(canvas);
    canvas.addEventListener("pointerdown", this.down);
    canvas.addEventListener("pointermove", this.move);
    canvas.addEventListener("pointerup", this.up);
    canvas.addEventListener("pointercancel", this.cancel);
    canvas.addEventListener("pointerleave", this.leave);
    canvas.addEventListener("keydown", this.key);
    this.motion.addEventListener("change", this.restart);
    document.addEventListener("visibilitychange", this.restart);
    this.resize();
    this.restart();
  }
  dispose() {
    this.disposed = true;
    cancelAnimationFrame(this.frame);
    this.observer.disconnect();
    this.canvas.removeEventListener("pointerdown", this.down);
    this.canvas.removeEventListener("pointermove", this.move);
    this.canvas.removeEventListener("pointerup", this.up);
    this.canvas.removeEventListener("pointercancel", this.cancel);
    this.canvas.removeEventListener("pointerleave", this.leave);
    this.canvas.removeEventListener("keydown", this.key);
    this.motion.removeEventListener("change", this.restart);
    document.removeEventListener("visibilitychange", this.restart);
  }
  setData(flowers: Flower[], garden: Garden) {
    const now = performance.now();
    const previous = new Set(this.flowers.map((f) => f.id));
    if (this.initialized && garden.id === this.garden.id)
      for (const f of flowers)
        if (!previous.has(f.id)) this.born.set(f.id, now);
    if (garden.id !== this.garden.id) this.born.clear();
    this.initialized = true;
    this.flowers = [...flowers].sort(
      (a, b) => a.y - b.y || a.id.localeCompare(b.id),
    );
    this.garden = garden;
    this.draw(now);
  }
  setPalette(palette: number) {
    this.selected = palette;
    this.draw(performance.now());
  }
  setPending(point: Point | null, palette: number) {
    this.pending = point
      ? {
          ...point,
          id: "pending",
          gardenId: this.garden.id,
          palette,
          seed: 42,
          styleVersion: 1,
          createdAt: "",
        }
      : null;
    this.draw(performance.now());
  }
  zoom(amount: number) {
    this.camera.scale = Math.max(
      this.fitScale(),
      Math.min(1.4, this.camera.scale * amount),
    );
    this.draw(performance.now());
  }
  fit() {
    this.camera = {
      x: this.garden.width / 2,
      y: this.garden.height / 2,
      scale: this.fitScale(),
    };
    this.draw(performance.now());
  }
  randomSpot(): Point | null {
    for (let i = 0; i < 100; i++) {
      const point = this.toWorld({
        x: 32 + Math.random() * Math.max(1, this.width - 64),
        y: 72 + Math.random() * Math.max(1, this.height - 200),
      });
      if (canPlant(point, this.flowers, this.garden)) return point;
    }
    return null;
  }
  private fitScale() {
    return Math.min(
      (this.width - 28) / this.garden.width,
      (this.height - 80) / this.garden.height,
    );
  }
  private resize = () => {
    const rect = this.canvas.getBoundingClientRect();
    const first = this.width === 1;
    this.width = Math.max(1, rect.width);
    this.height = Math.max(1, rect.height);
    this.dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.canvas.width = Math.round(this.width * this.dpr);
    this.canvas.height = Math.round(this.height * this.dpr);
    if (first) this.camera.scale = Math.max(0.34, this.fitScale());
    this.draw(performance.now());
  };
  private toWorld(point: Point): Point {
    return {
      x: (point.x - this.width / 2) / this.camera.scale + this.camera.x,
      y: (point.y - this.height / 2) / this.camera.scale + this.camera.y,
    };
  }
  private position(event: PointerEvent): Point {
    const rect = this.canvas.getBoundingClientRect();
    return { x: event.clientX - rect.left, y: event.clientY - rect.top };
  }
  private down = (event: PointerEvent) => {
    if (event.button !== 0) return;
    this.keyboard = false;
    this.canvas.setPointerCapture(event.pointerId);
    const point = this.position(event);
    this.pointers.set(event.pointerId, point);
    this.start = point;
    this.moved = this.pointers.size > 1;
    if (this.pointers.size === 2) {
      const [a, b] = [...this.pointers.values()];
      this.pinchDistance = Math.hypot(a.x - b.x, a.y - b.y);
    }
  };
  private move = (event: PointerEvent) => {
    const point = this.position(event);
    const previous = this.pointers.get(event.pointerId);
    if (previous) {
      this.pointers.set(event.pointerId, point);
      if (this.pointers.size === 2) {
        const [a, b] = [...this.pointers.values()];
        const distance = Math.hypot(a.x - b.x, a.y - b.y);
        if (this.pinchDistance > 0) this.zoom(distance / this.pinchDistance);
        this.pinchDistance = distance;
        this.moved = true;
      } else if (
        this.start &&
        (this.moved ||
          Math.hypot(point.x - this.start.x, point.y - this.start.y) > 7)
      ) {
        this.moved = true;
        this.camera.x = Math.max(
          0,
          Math.min(
            this.garden.width,
            this.camera.x - (point.x - previous.x) / this.camera.scale,
          ),
        );
        this.camera.y = Math.max(
          0,
          Math.min(
            this.garden.height,
            this.camera.y - (point.y - previous.y) / this.camera.scale,
          ),
        );
      }
    }
    this.hovered = this.toWorld(point);
    this.draw(performance.now());
  };
  private up = (event: PointerEvent) => {
    const wasActive = this.pointers.has(event.pointerId);
    this.pointers.delete(event.pointerId);
    if (wasActive && !this.moved)
      this.onPlant(this.toWorld(this.position(event)));
    if (this.pointers.size === 0) {
      this.start = null;
      this.moved = false;
    }
    if (event.pointerType === "touch") this.hovered = null;
  };
  private cancel = (event: PointerEvent) => {
    this.pointers.delete(event.pointerId);
    this.moved = true;
    this.start = null;
  };
  private leave = () => {
    if (!this.pointers.size) {
      this.hovered = null;
      this.draw(performance.now());
    }
  };
  private key = (event: KeyboardEvent) => {
    if (
      [
        "ArrowLeft",
        "ArrowRight",
        "ArrowUp",
        "ArrowDown",
        "Enter",
        " ",
      ].includes(event.key)
    ) {
      event.preventDefault();
      this.keyboard = true;
      this.hovered ??= { x: this.camera.x, y: this.camera.y };
      if (event.key === "Enter" || event.key === " ")
        this.onPlant(this.hovered);
      else {
        const step = event.shiftKey ? 120 : 40;
        this.hovered = {
          x:
            this.hovered.x +
            (event.key === "ArrowRight"
              ? step
              : event.key === "ArrowLeft"
                ? -step
                : 0),
          y:
            this.hovered.y +
            (event.key === "ArrowDown"
              ? step
              : event.key === "ArrowUp"
                ? -step
                : 0),
        };
      }
      this.draw(performance.now());
    }
    if (event.key === "+" || event.key === "=") this.zoom(1.2);
    if (event.key === "-") this.zoom(1 / 1.2);
  };
  private restart = () => {
    cancelAnimationFrame(this.frame);
    if (this.disposed || document.hidden) return;
    this.draw(performance.now());
    if (!this.motion.matches) this.frame = requestAnimationFrame(this.tick);
  };
  private tick = (now: number) => {
    if (this.disposed || document.hidden) return;
    if (now - this.lastFrame >= 32) {
      this.draw(now);
      this.lastFrame = now;
    }
    this.frame = requestAnimationFrame(this.tick);
  };
  private draw(now: number) {
    const c = this.ctx;
    c.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    c.clearRect(0, 0, this.width, this.height);
    c.translate(this.width / 2, this.height / 2);
    c.scale(this.camera.scale, this.camera.scale);
    c.translate(-this.camera.x, -this.camera.y);
    const left = this.camera.x - this.width / (2 * this.camera.scale),
      right = this.camera.x + this.width / (2 * this.camera.scale);
    const top = this.camera.y - this.height / (2 * this.camera.scale),
      bottom = this.camera.y + this.height / (2 * this.camera.scale);
    for (const f of this.flowers) {
      if (
        f.x < left - 100 ||
        f.x > right + 100 ||
        f.y < top - 260 ||
        f.y > bottom + 100
      )
        continue;
      const age = now - (this.born.get(f.id) ?? -Infinity);
      const growth = this.motion.matches ? 1 : Math.min(1, age / 900);
      if (growth === 1) this.born.delete(f.id);
      this.flower(f, now, 1 - (1 - growth) ** 3);
    }
    if (this.pending) {
      c.save();
      c.globalAlpha = 0.4;
      this.flower(this.pending, now, 0.6);
      c.restore();
    }
    if (this.hovered && !this.pointers.size && !this.garden.paused) {
      const valid = canPlant(this.hovered, this.flowers, this.garden);
      const { x, y } = this.hovered;
      c.strokeStyle = valid ? palettes[this.selected].color : "#a99888";
      c.lineWidth = 1.5 / this.camera.scale;
      c.setLineDash([3 / this.camera.scale, 5 / this.camera.scale]);
      c.beginPath();
      c.arc(x, y, 17 / this.camera.scale, 0, Math.PI * 2);
      c.stroke();
      c.setLineDash([]);
      if (this.keyboard) {
        c.beginPath();
        c.moveTo(x - 7 / this.camera.scale, y);
        c.lineTo(x + 7 / this.camera.scale, y);
        c.moveTo(x, y - 7 / this.camera.scale);
        c.lineTo(x, y + 7 / this.camera.scale);
        c.stroke();
      }
    }
  }
  private flower(f: Flower, now: number, grow: number) {
    const c = this.ctx,
      p = palettes[f.palette] ?? palettes[0];
    const random = (salt: number) => {
      let x = (f.seed ^ salt) >>> 0;
      x = Math.imul(x ^ (x >>> 16), 0x45d9f3b);
      x = Math.imul(x ^ (x >>> 16), 0x45d9f3b);
      return ((x ^ (x >>> 16)) >>> 0) / 4294967296;
    };
    const stem = 140 + random(35) * 100,
      radius = 45 + random(72) * 23,
      bend = (random(41) - 0.5) * 75;
    const sway = this.motion.matches
      ? 0
      : Math.sin(now / 2500 + (f.seed % 20)) * 5;
    const baseY = f.y + stem,
      headY = baseY - stem * grow,
      headX = f.x + bend * (1 - grow) + sway * grow;
    c.fillStyle = "#6e765710";
    c.beginPath();
    c.ellipse(f.x, baseY + 4, 20, 5, 0, 0, Math.PI * 2);
    c.fill();
    c.strokeStyle = p.leaf;
    c.lineWidth = 2.7;
    c.lineCap = "round";
    c.beginPath();
    c.moveTo(f.x, baseY);
    c.bezierCurveTo(
      f.x + bend,
      baseY - stem * 0.35 * grow,
      headX - bend * 0.25,
      headY + stem * 0.2 * grow,
      headX,
      headY,
    );
    c.stroke();
    if (grow > 0.35) {
      for (let i = 0; i < 2; i++) {
        const sign = i === 0 ? 1 : -1,
          y = baseY - stem * (0.28 + i * 0.25) * grow,
          leaf = 45 * grow;
        c.fillStyle = p.leaf;
        c.globalAlpha *= 0.8;
        c.beginPath();
        c.moveTo(f.x + bend * 0.2, y);
        c.bezierCurveTo(
          f.x + sign * leaf * 0.25,
          y - leaf * 0.8,
          f.x + sign * leaf,
          y - leaf * 0.8,
          f.x + sign * leaf,
          y - leaf,
        );
        c.bezierCurveTo(
          f.x + sign * leaf,
          y - leaf * 0.1,
          f.x + sign * leaf * 0.25,
          y + 4,
          f.x + bend * 0.2,
          y,
        );
        c.fill();
        c.globalAlpha /= 0.8;
      }
    }
    c.save();
    c.translate(headX, headY);
    c.rotate(random(11) * 2 + sway * 0.005);
    const bloom = Math.max(0.001, Math.min(1, (grow - 0.35) / 0.65));
    c.scale(bloom, bloom);
    const kind = f.seed % 3,
      petals = kind === 0 ? 8 : kind === 1 ? 6 : 11;
    for (let i = 0; i < petals; i++) {
      c.save();
      c.rotate((i * Math.PI * 2) / petals);
      c.fillStyle = p.color;
      c.beginPath();
      c.ellipse(
        0,
        -radius * 0.55,
        radius * (kind === 1 ? 0.47 : 0.28),
        radius * 0.57,
        0,
        0,
        Math.PI * 2,
      );
      c.fill();
      c.strokeStyle = "#fff6e929";
      c.lineWidth = 1.5;
      c.beginPath();
      c.moveTo(0, -radius * 0.25);
      c.quadraticCurveTo(4, -radius * 0.65, 0, -radius * 0.96);
      c.stroke();
      c.restore();
    }
    c.fillStyle = p.center;
    c.beginPath();
    c.arc(0, 0, radius * 0.23, 0, Math.PI * 2);
    c.fill();
    c.fillStyle = "#6c624c65";
    for (let i = 0; i < 7; i++) {
      const a = i * 2.4;
      const r = Math.sqrt(i) * radius * 0.055;
      c.beginPath();
      c.arc(Math.cos(a) * r, Math.sin(a) * r, 1.4, 0, Math.PI * 2);
      c.fill();
    }
    c.restore();
  }
}
