export function groupConsecutive(values: number[]): number[][] {
  const groups: number[][] = [];
  let current: number[] = [];

  for (const value of values) {
    if (current.length > 0 && value === current[current.length - 1] + 1) {
      current.push(value);
    } else {
      if (current.length > 0) {
        groups.push(current);
      }
      current = [value];
    }
  }

  if (current.length > 0) {
    groups.push(current);
  }

  return groups;
}
