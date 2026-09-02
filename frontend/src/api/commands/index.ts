/**
 * Command surface (mutations) for the operator console.
 *
 * The dependency direction is page -> feature -> api/commands -> transport,
 * and every real command lives in a domain module beside this one.
 *
 * Mutations are deliberately NOT routed through `getTransport()`, the
 * build-time dev/prod switch that reads use. `getTransport()` resolves to
 * `httpTransport.dev` under `vite dev`, and a mutation must never have an
 * HTTP path available to it even in development: ADR 0018 control 1 puts
 * the authorization in a native Win32 dialog owned by cia-console.exe,
 * reached over the DACL-protected named pipe, and the deprecated HTTP
 * administrative surface is exactly what that control exists to stop being
 * used. Commands therefore import `bridgeTransport` directly, so no
 * mutation can reach a non-bridge transport by construction rather than by
 * convention. See `./models.ts`.
 *
 * Sprint 1 declared two placeholder commands here that awaited
 * `getTransport()` and then threw. They were removed once `./models.ts`
 * landed: a dead stub that models the wrong routing is a template for the
 * next author's mistake, and `setActiveModel` was superseded by
 * `switchModel` anyway. Maintenance drain/resume followed the same pattern
 * once Sprint 7 gave it a real screen - see `./maintenance.ts`.
 */
export {
  loadModel,
  unloadModel,
  switchModel,
  NativeHostUnavailableError,
} from './models';
export { drainProvider, resumeProvider } from './maintenance';
