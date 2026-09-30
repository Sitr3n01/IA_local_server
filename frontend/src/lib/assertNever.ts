/**
 * Exhaustiveness helper for discriminated unions and switch statements.
 * Passing anything other than `never` fails typecheck at the call site
 * (TypeScript can only narrow an argument to `never` once every other case
 * has been handled); reaching this function at runtime means a case was
 * missed despite that, so it throws with the unexpected value attached.
 */
export function assertNever(value: never, context?: string): never {
  throw new Error(`Unreachable case${context ? ` in ${context}` : ''}: ${JSON.stringify(value)}`);
}
