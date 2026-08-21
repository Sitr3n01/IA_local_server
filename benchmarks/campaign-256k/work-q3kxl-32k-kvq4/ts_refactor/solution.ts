export type Fetcher = (id: string) => Promise<{ id: string; parent: string | null }>;

export async function ancestry(fetch: Fetcher, start: string): Promise<string[]> {
  const seen = new Set<string>();
  const chain: string[] = [];
  let id = start;

  while (true) {
    if (seen.has(id)) {
      throw new Error("cycle detected");
    }

    seen.add(id);
    chain.push(id);

    if (chain.length > 50) {
      throw new Error("chain too long");
    }

    const node = await fetch(id);

    if (node.parent === null) {
      return chain;
    }

    id = node.parent;
  }
}
