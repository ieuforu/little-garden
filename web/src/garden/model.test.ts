import { describe, expect, it } from "vitest";
import { acknowledge, canPlant, reconcile } from "./model";
import type { Flower, Snapshot } from "./model";

const flower: Flower = {
  id: "a",
  gardenId: 1,
  x: 400,
  y: 600,
  seed: 42,
  palette: 0,
  styleVersion: 1,
  createdAt: "2026-10-31T00:00:00Z",
};
const empty = (): Snapshot => ({
  type: "snapshot",
  garden: {
    id: 1,
    width: 2400,
    height: 1600,
    revision: 0,
    paused: false,
    capacity: 800,
    planted: 0,
  },
  flowers: [],
});
describe("ordered garden state", () => {
  it("deduplicates HTTP-first and stream-first delivery without skipping a revision", () => {
    const event = { type: "flower" as const, revision: 1, flower };
    const ackFirst = acknowledge(empty(), event)!;
    expect(ackFirst.garden.revision).toBe(0);
    const final = reconcile(ackFirst, event)!;
    expect(final.flowers).toHaveLength(1);
    expect(final.garden.planted).toBe(1);
    expect(acknowledge(reconcile(empty(), event), event)).toEqual(final);
    expect(reconcile(final, event)).toEqual(final);
  });
  it("requires resynchronization after missed events or a garden change", () => {
    expect(
      reconcile(empty(), { type: "flower", revision: 3, flower }),
    ).toBeNull();
    expect(
      reconcile(empty(), {
        type: "flower",
        revision: 1,
        flower: { ...flower, gardenId: 2 },
      }),
    ).toBeNull();
  });
  it("does not resurrect a hidden flower from a delayed acknowledgement", () => {
    const moderated = {
      ...empty(),
      garden: { ...empty().garden, revision: 2, planted: 1 },
    };
    expect(
      acknowledge(moderated, { type: "flower", revision: 1, flower })?.flowers,
    ).toHaveLength(0);
    expect(
      acknowledge(moderated, {
        type: "flower",
        revision: 2,
        flower,
        duplicate: true,
      })?.flowers,
    ).toHaveLength(0);
  });
  it("ignores stale snapshots and accepts a new garden", () => {
    const current = { ...empty(), garden: { ...empty().garden, revision: 4 } };
    expect(reconcile(current, empty())).toBe(current);
    const next = { ...empty(), garden: { ...empty().garden, id: 2 } };
    expect(reconcile(current, next)).toBe(next);
  });
  it("checks world boundaries, finite coordinates and spacing", () => {
    expect(canPlant({ x: 0, y: 500 }, [], empty().garden)).toBe(false);
    expect(canPlant({ x: NaN, y: 500 }, [], empty().garden)).toBe(false);
    expect(canPlant({ x: 430, y: 620 }, [flower], empty().garden)).toBe(false);
    expect(canPlant({ x: 500, y: 600 }, [flower], empty().garden)).toBe(true);
  });
});
