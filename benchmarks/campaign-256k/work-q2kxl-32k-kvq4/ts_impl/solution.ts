export function groupConsecutive(values: number[]): number[][] {
  const groups: number[][] = [];
  let current: number[] | undefined;
  let last: number | undefined;

  for (const value of values) {
    if (current === undefined) {
      current = [value];
    } else if (last !== undefined && value - last === 1) {
      current.push(value);
    } else {
      groups.push(current);
      current = [value];
    }
    last = value;
  }

  if (current !== undefined) {
    groups.push(current);
  }

  return groups;
}
