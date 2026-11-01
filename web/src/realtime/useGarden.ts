import { useCallback, useEffect, useRef, useState } from "react";
import { acknowledge, reconcile } from "../garden/model";
import type {
  FlowerEvent,
  GardenConfig,
  PlantRequest,
  Snapshot,
} from "../garden/model";

export class APIError extends Error {
  constructor(
    public code: string,
    public status = 0,
    public retryAfter = 0,
  ) {
    super(code);
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`/api/v1/${path}`, {
      ...options,
      signal: options.signal ?? AbortSignal.timeout(10_000),
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", ...options.headers },
    });
  } catch {
    throw new APIError("network_error");
  }
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    throw new APIError(
      data.error ?? "server_busy",
      response.status,
      Number(response.headers.get("Retry-After")) || 0,
    );
  }
  return response.json() as Promise<T>;
}

export type Connection =
  | "connecting"
  | "connected"
  | "reconnecting"
  | "offline";

export function useGarden() {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const stateRef = useRef<Snapshot | null>(null);
  const [config, setConfig] = useState<GardenConfig | null>(null);
  const [connection, setConnection] = useState<Connection>("connecting");
  const [loadError, setLoadError] = useState(false);
  const [expiresAt, setExpiresAt] = useState(0);
  const socketRef = useRef<WebSocket | null>(null);
  const update = useCallback((value: Snapshot | null) => {
    stateRef.current = value;
    setSnapshot(value);
  }, []);

  useEffect(() => {
    let active = true;
    let attempts = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const abort = new AbortController();
    function schedule(fn: () => void) {
      if (!active) return;
      clearTimeout(timer);
      const delay =
        Math.min(30_000, 1000 * 2 ** Math.min(attempts++, 5)) +
        Math.random() * 1000;
      timer = setTimeout(fn, delay);
    }
    function connect() {
      if (!active || !navigator.onLine) {
        if (active) {
          setConnection("offline");
          schedule(connect);
        }
        return;
      }
      if (
        socketRef.current?.readyState === WebSocket.CONNECTING ||
        socketRef.current?.readyState === WebSocket.OPEN
      )
        return;
      const url = new URL("/api/v1/ws", location.href);
      url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
      const socket = new WebSocket(url);
      socketRef.current = socket;
      socket.onmessage = (event) => {
        if (!active || socket !== socketRef.current) return;
        try {
          const message = JSON.parse(event.data) as Snapshot | FlowerEvent;
          if (message.type !== "snapshot" && message.type !== "flower")
            throw new Error("Unknown message");
          const next = reconcile(stateRef.current, message);
          if (!next) {
            socket.close();
            return;
          }
          update(next);
          // A successful TCP upgrade is not proof that the initial snapshot arrived.
          if (message.type === "snapshot") {
            attempts = 0;
            setConnection("connected");
            setLoadError(false);
          }
        } catch {
          socket.close();
        }
      };
      socket.onerror = () => socket.close();
      socket.onclose = () => {
        if (active && socket === socketRef.current) {
          setConnection(navigator.onLine ? "reconnecting" : "offline");
          schedule(connect);
        }
      };
    }
    async function initialize() {
      try {
        const result = await request<GardenConfig>("garden", {
          signal: abort.signal,
        });
        if (!active) return;
        setConfig(result);
        update(result);
        setLoadError(false);
        connect();
        const session = await request<{ expiresAt: number }>("session", {
          signal: abort.signal,
        });
        if (active) setExpiresAt(session.expiresAt);
      } catch {
        if (active && !stateRef.current) {
          setLoadError(true);
          setConnection("reconnecting");
          schedule(initialize);
        }
      }
    }
    const wake = () => {
      if (!active) return;
      if (socketRef.current?.readyState === WebSocket.OPEN) return;
      clearTimeout(timer);
      if (stateRef.current) connect();
      else void initialize();
    };
    window.addEventListener("online", wake);
    void initialize();
    return () => {
      active = false;
      abort.abort();
      clearTimeout(timer);
      socketRef.current?.close();
      window.removeEventListener("online", wake);
    };
  }, [update]);

  const authenticate = useCallback(async (token: string) => {
    const result = await request<{ expiresAt: number }>("session", {
      method: "POST",
      body: JSON.stringify({ token }),
    });
    setExpiresAt(result.expiresAt);
  }, []);

  const plant = useCallback(
    async (req: PlantRequest) => {
      const post = () =>
        request<FlowerEvent>("flowers", {
          method: "POST",
          body: JSON.stringify(req),
        });
      let result: FlowerEvent;
      try {
        result = await post();
      } catch (error) {
        // Retry only an uncertain network result, always with the same idempotency key.
        if (!(error instanceof APIError) || error.status !== 0) {
          if (error instanceof APIError && error.status === 401)
            setExpiresAt(0);
          throw error;
        }
        await new Promise((resolve) => setTimeout(resolve, 700));
        result = await post();
      }
      update(acknowledge(stateRef.current, result));
      if (
        result.duplicate ||
        result.flower.gardenId !== stateRef.current?.garden.id
      )
        socketRef.current?.close();
      return result;
    },
    [update],
  );

  return {
    snapshot,
    config,
    connection,
    loadError,
    authenticate,
    plant,
    expiresAt,
  };
}
