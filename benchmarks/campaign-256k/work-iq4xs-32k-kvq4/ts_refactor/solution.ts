export type Fetcher = (id: string) => Promise<{ id: string; parent: string | null }>;

// Walks from a node to the root and returns the chain, root last.
// Uses async/await and a plain loop. Behaviour is unchanged,
// including that a cycle throws Error("cycle detected") and that the chain is
// capped at 50 entries with Error("chain too long").
export async function ancestry(fetch: Fetcher, start: string): Promise<string[]> {
  const seen = new Set<string>();
  const chain: string[] = [];
  let id: string = start;

  while (true) {
    if (seen.has(id)) throw new Error("cycle detected");
    seen.add(id);
    chain.push(id);
    if (chain.length > 50) throw new Error("chain too long");

    const node = await fetch(id);
    if (node.parent === null) return chain;
    id = node.parent;
  }
}
