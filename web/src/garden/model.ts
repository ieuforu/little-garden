export interface Flower {
  id: string;
  gardenId: number;
  x: number;
  y: number;
  seed: number;
  palette: number;
  styleVersion: number;
  createdAt: string;
}
export interface Garden {
  id: number;
  width: number;
  height: number;
  capacity: number;
  revision: number;
  paused: boolean;
  planted: number;
}
export interface Snapshot {
  type: "snapshot";
  garden: Garden;
  flowers: Flower[];
}
export interface FlowerEvent {
  type: "flower";
  revision: number;
  flower: Flower;
  duplicate?: boolean;
}
export interface GardenConfig extends Snapshot {
  siteKey: string;
  development: boolean;
}
export interface Point {
  x: number;
  y: number;
}
export interface PlantRequest extends Point {
  gardenId: number;
  requestId: string;
  palette: number;
}
export const palettes = [
  { name: "日落橘", color: "#dc8a6d", center: "#f1d695", leaf: "#7b8b63" },
  { name: "雾紫色", color: "#b8a2cd", center: "#eee3b6", leaf: "#7b927b" },
  { name: "奶油黄", color: "#e5c374", center: "#8b7552", leaf: "#8b9769" },
  { name: "海盐蓝", color: "#9cb8b8", center: "#f1deb1", leaf: "#6d8b7e" },
] as const;

// The HTTP acknowledgement can arrive before or after its WebSocket event.
// Only ordered stream events advance revision. A gap requires a fresh snapshot.
export function reconcile(
  current: Snapshot | null,
  event: Snapshot | FlowerEvent,
): Snapshot | null {
  if (event.type === "snapshot") {
    if (
      current &&
      (event.garden.id < current.garden.id ||
        (event.garden.id === current.garden.id &&
          event.garden.revision < current.garden.revision))
    )
      return current;
    return event;
  }
  if (!current || event.flower.gardenId !== current.garden.id) return null;
  if (event.revision <= current.garden.revision) return current;
  if (event.revision !== current.garden.revision + 1) return null;
  return {
    ...current,
    garden: {
      ...current.garden,
      revision: event.revision,
      planted: current.garden.planted + 1,
    },
    flowers: current.flowers.some((f) => f.id === event.flower.id)
      ? current.flowers
      : [...current.flowers, event.flower],
  };
}

export function acknowledge(
  current: Snapshot | null,
  event: FlowerEvent,
): Snapshot | null {
  if (
    !current ||
    event.duplicate ||
    event.flower.gardenId !== current.garden.id ||
    event.revision < current.garden.revision ||
    current.flowers.some((f) => f.id === event.flower.id)
  )
    return current;
  return { ...current, flowers: [...current.flowers, event.flower] };
}

export function canPlant(
  point: Point,
  flowers: Flower[],
  garden: Garden,
): boolean {
  return (
    Number.isFinite(point.x) &&
    Number.isFinite(point.y) &&
    point.x >= 60 &&
    point.x <= garden.width - 60 &&
    point.y >= 100 &&
    point.y <= garden.height - 200 &&
    flowers.every((f) => Math.hypot(f.x - point.x, f.y - point.y) >= 70)
  );
}
