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
  { name: "珊瑚橘", color: "#d88a73", center: "#796249", leaf: "#748461", ink: "#ae6654" },
  { name: "雾紫", color: "#b6a2c7", center: "#e6c56e", leaf: "#74856c", ink: "#8a769b" },
  { name: "麦芽黄", color: "#e5c475", center: "#94805c", leaf: "#81906b", ink: "#b7954c" },
  { name: "晴蓝", color: "#92b4c4", center: "#efe0af", leaf: "#728a7e", ink: "#688d9f" },
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
