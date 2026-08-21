import { ancestry } from "./solution.ts";

const tree: Record<string, string | null> = {
  a: "b", b: "c", c: null, loopX: "loopY", loopY: "loopX",
};
const fetch = async (id: string) => ({ id, parent: tree[id] ?? null });

function eq(a: unknown, b: unknown, label: string) {
  const sa = JSON.stringify(a), sb = JSON.stringify(b);
  if (sa !== sb) throw new Error(`${label}: got ${sa} want ${sb}`);
}

const main = async () => {
  eq(await ancestry(fetch, "a"), ["a", "b", "c"], "chain");
  eq(await ancestry(fetch, "c"), ["c"], "root only");

  let cycled = "";
  try { await ancestry(fetch, "loopX"); } catch (e) { cycled = (e as Error).message; }
  eq(cycled, "cycle detected", "cycle");

  const deep: Record<string, string> = {};
  for (let i = 0; i < 80; i++) deep[`n${i}`] = `n${i + 1}`;
  const deepFetch = async (id: string) => ({ id, parent: deep[id] ?? null });
  let tooLong = "";
  try { await ancestry(deepFetch, "n0"); } catch (e) { tooLong = (e as Error).message; }
  eq(tooLong, "chain too long", "cap");

  // The refactor must not leave recursion behind.
  const src = await (await import("node:fs/promises")).readFile("solution.ts", "utf8");
  if (/\bfunction\s+step\b/.test(src) || /\.then\s*\(/.test(src)) {
    throw new Error("still uses the callback/recursive form");
  }
  console.log("OK");
};
main().catch((e) => { console.error(String(e)); process.exit(1); });
