import { groupConsecutive } from "./solution.ts";

function eq(a: unknown, b: unknown, label: string) {
  const sa = JSON.stringify(a), sb = JSON.stringify(b);
  if (sa !== sb) throw new Error(`${label}: got ${sa} want ${sb}`);
}

eq(groupConsecutive([1, 2, 4, 5, 6, 9]), [[1, 2], [4, 5, 6], [9]], "basic");
eq(groupConsecutive([]), [], "empty");
eq(groupConsecutive([7]), [[7]], "single");
eq(groupConsecutive([3, 3, 4]), [[3], [3, 4]], "duplicate breaks run");
eq(groupConsecutive([5, 4, 3]), [[5], [4], [3]], "descending never groups");
eq(groupConsecutive([-2, -1, 0, 2]), [[-2, -1, 0], [2]], "negatives");
console.log("OK");
