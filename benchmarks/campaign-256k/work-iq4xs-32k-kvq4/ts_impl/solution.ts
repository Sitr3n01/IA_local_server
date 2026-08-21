export function groupConsecutive(values: number[]): number[][] {
  const groups: number[][] = [];

  for (const value of values) {
    const lastGroup = groups[groups.length - 1];

    if (lastGroup !== undefined && value - lastGroup[lastGroup.length - 1] === 1) {
      lastGroup.push(value);
    } else {
      groups.push([value]);
    }
  }

  return groups;
}
