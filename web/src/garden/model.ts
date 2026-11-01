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
  { name: "橘色", color: "#cc795b", center: "#f3d59a", leaf: "#77875d" },
  { name: "紫色", color: "#aa8ebd", center: "#f1dfac", leaf: "#748c70" },
  { name: "黄色", color: "#dab450", center: "#82704b", leaf: "#83905b" },
  { name: "蓝色", color: "#7fa4b0", center: "#f3dfad", leaf: "#688577" },
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
