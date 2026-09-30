/**
 * Design tokens, Sprint 2 (DS 0.1).
 *
 * Three-layer architecture:
 *   1. foundation.css - raw palette ramps, spacing, radii, type, motion.
 *      No meaning attached; the only file allowed a literal hex colour.
 *   2. semantic.css   - the fixed set of --color-*, spacing/radius/motion
 *      alias, and typography-role tokens components are allowed to
 *      reference. Resolves light/dark theme entirely outside component
 *      code (see the doc comment at the top of semantic.css).
 *
 * Component (layer 3) tokens live next to the one component that needs
 * them, not here.
 *
 * Importing this module once (done in src/main.tsx) pulls both stylesheets
 * into the bundle as a side effect - nothing is exported, because tokens
 * are consumed as CSS custom properties, not JS values.
 */
// Roboto Flex (SIL OFL-1.1), bundled locally by @fontsource-variable.
// Imported before foundation.css so the @font-face rules exist by the
// time --font-family-sans names the family. Vite emits the woff2 into
// dist/assets; nothing is fetched from a remote origin at runtime.
import '@fontsource-variable/roboto-flex/index.css';
import './foundation.css';
import './semantic.css';

export {};
