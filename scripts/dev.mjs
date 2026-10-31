import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
await mkdir(new URL("../.local/", import.meta.url), { recursive: true });
const children = [];
let stopping = false;
const stop = (code = 0) => {
  if (stopping) return;
  stopping = true;
  process.exitCode = code;
  for (const child of children) child.kill("SIGTERM");
  setTimeout(() => {
    for (const child of children) child.kill("SIGKILL");
  }, 9000).unref();
};
function run(command, args, options) {
  const child = spawn(command, args, { stdio: "inherit", ...options });
  child.on("error", (error) => {
    console.error(error.message);
    stop(1);
  });
  child.on("exit", (code) => {
    if (!stopping) stop(code ?? 1);
  });
  children.push(child);
}
process.on("SIGINT", () => stop());
process.on("SIGTERM", () => stop());
const built = await new Promise((resolve) => {
  const build = spawn(
    process.env.GARDEN_GO ?? "go",
    ["build", "-o", `${root}/.local/garden`, "./cmd/garden"],
    { cwd: `${root}/server`, stdio: "inherit" },
  );
  children.push(build);
  build.on("error", (error) => {
    console.error(error.message);
    resolve(false);
  });
  build.on("exit", (code) => resolve(code === 0));
});
if (!built || stopping) {
  stop(1);
} else {
  run(`${root}/.local/garden`, ["serve"], {
    cwd: `${root}/server`,
    env: {
      ...process.env,
      GARDEN_MODE: "development",
      LISTEN_ADDR: "127.0.0.1:8081",
      DATABASE_PATH: `${root}/.local/garden.db`,
      ADMIN_SOCKET: `${root}/.local/admin.sock`,
    },
  });
  run(
    process.execPath,
    [
      fileURLToPath(
        new URL("../web/node_modules/vite/bin/vite.js", import.meta.url),
      ),
      ...process.argv.slice(2),
    ],
    { cwd: `${root}/web` },
  );
}
