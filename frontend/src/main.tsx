import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryProvider } from './app/providers/QueryProvider';
import { App } from './app/App';
import './design-system/tokens';
import './design-system/styles/reset.css';
import './design-system/styles/motion.css';

const rootElement = document.getElementById('root');
if (!rootElement) {
  throw new Error('#root element is missing from index.html');
}

const root = createRoot(rootElement);

/**
 * Dev-only primitive gallery, reachable at /__gallery under `vite dev`.
 *
 * Gated the same way the transport boundary excludes httpTransport.dev.ts
 * from production (src/api/transport/index.ts): `import.meta.env.DEV` is a
 * compile-time constant, so with the import behind a dynamic `import()`,
 * Rollup's dead-code elimination drops the whole branch - Gallery.tsx
 * included - from the production module graph. scripts/verify-prod-bundle.mjs
 * asserts this actually happened by scanning dist/ for a marker string
 * unique to Gallery.tsx's source.
 */
async function renderApp() {
  if (import.meta.env.DEV && window.location.pathname.startsWith('/__gallery')) {
    const { Gallery } = await import('./dev/Gallery');
    root.render(
      <StrictMode>
        <Gallery />
      </StrictMode>,
    );
    return;
  }

  root.render(
    <StrictMode>
      <QueryProvider>
        <App />
      </QueryProvider>
    </StrictMode>,
  );
}

void renderApp();
