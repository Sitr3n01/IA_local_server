import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import {
  Button,
  Divider,
  IconButton,
  IconCheck,
  IconClose,
  IconInfo,
  Skeleton,
  Surface,
  TextField,
  Tooltip,
} from '../design-system/primitives';
import { Notice, StatGroup } from '../design-system/components';
import './Gallery.css';

// A marker string unique to this file's source, so
// scripts/verify-prod-bundle.mjs can prove the gallery never reaches the
// production bundle - mirrors how verify-prod-bundle.mjs already proves
// httpTransport.dev.ts doesn't leak in, via its own DEV_TRANSPORT_MARKER.
const GALLERY_MARKER = 'design-system-gallery:dev-only';

type ThemeChoice = 'system' | 'light' | 'dark';

function ThemeToggle() {
  const [theme, setTheme] = useState<ThemeChoice>('system');

  useEffect(() => {
    if (theme === 'system') {
      document.documentElement.removeAttribute('data-theme');
    } else {
      document.documentElement.setAttribute('data-theme', theme);
    }
  }, [theme]);

  return (
    <div className="gallery-theme-toggle" role="group" aria-label="Theme">
      {(['system', 'light', 'dark'] as const).map((choice) => (
        <Button
          key={choice}
          type="button"
          variant="secondary"
          size="sm"
          selected={theme === choice}
          onClick={() => setTheme(choice)}
        >
          {choice}
        </Button>
      ))}
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="gallery-section">
      <h2 className="gallery-section__title">{title}</h2>
      <div className="gallery-section__body">{children}</div>
    </section>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="gallery-row">
      <span className="gallery-row__label">{label}</span>
      <div className="gallery-row__content">{children}</div>
    </div>
  );
}

/**
 * Dev-only DS 0.1 primitive gallery. Explicit props render every
 * disabled/loading/selected/error state directly; hover, pressed, and
 * focus-visible are real pseudo-class states - exercise them by actually
 * moving the mouse or tabbing through this live page, in both themes via
 * the toggle above.
 */
export function Gallery() {
  const [textValue, setTextValue] = useState('cia-edge');
  const [selected, setSelected] = useState(false);

  return (
    <Surface level="canvas" as="main" className="gallery">
      {/* Plain text node (not an HTML sink) - verify-prod-bundle.mjs greps
          the built output for this exact string to prove the gallery
          never reaches the production bundle. */}
      <p hidden>{GALLERY_MARKER}</p>
      <header className="gallery-header">
        <h1 className="gallery-title">IA Local design system - DS 0.1 gallery</h1>
        <p className="gallery-subtitle">Dev-only. Excluded from the production bundle.</p>
        <ThemeToggle />
      </header>

      <Section title="Surface">
        <Row label="canvas / base / subtle / raised">
          <div className="gallery-swatch-row">
            <Surface level="canvas" bordered rounded className="gallery-swatch">
              canvas
            </Surface>
            <Surface level="base" bordered rounded className="gallery-swatch">
              base
            </Surface>
            <Surface level="subtle" bordered rounded className="gallery-swatch">
              subtle
            </Surface>
            <Surface level="raised" bordered rounded className="gallery-swatch">
              raised
            </Surface>
          </div>
        </Row>
      </Section>

      <Section title="Button">
        <Row label="primary - default / hover me / disabled / loading">
          <Button variant="primary">Admit model</Button>
          <Button variant="primary">Hover or tab to me</Button>
          <Button variant="primary" disabled>
            Disabled
          </Button>
          <Button variant="primary" loading>
            Loading
          </Button>
        </Row>
        <Row label="secondary - default / disabled / selected (toggle)">
          <Button variant="secondary">Cancel</Button>
          <Button variant="secondary" disabled>
            Disabled
          </Button>
          <Button variant="secondary" selected={selected} onClick={() => setSelected((v) => !v)}>
            {selected ? 'Selected' : 'Not selected'}
          </Button>
        </Row>
        <Row label="ghost (tertiary) - default / hover me / disabled">
          <Button variant="ghost">Show capacity details</Button>
          <Button variant="ghost">Hover or tab to me</Button>
          <Button variant="ghost" disabled>
            Disabled
          </Button>
        </Row>
        <Row label="sizes">
          <Button size="md">Medium</Button>
          <Button size="sm">Small</Button>
        </Row>
      </Section>

      <Section title="IconButton">
        <Row label="secondary - default / selected / disabled / loading">
          <IconButton label="Close" icon={<IconClose />} />
          <IconButton label="Acknowledge" icon={<IconCheck />} selected />
          <IconButton label="Disabled action" icon={<IconClose />} disabled />
          <IconButton label="Saving" icon={<IconCheck />} loading />
        </Row>
        <Row label="ghost (tertiary)">
          <IconButton label="Collapse navigation" icon={<IconClose />} variant="ghost" />
          <IconButton label="Acknowledged" icon={<IconCheck />} variant="ghost" selected />
        </Row>
        <Row label="primary">
          <IconButton label="Confirm" icon={<IconCheck />} variant="primary" />
        </Row>
      </Section>

      <Section title="Notice">
        <Row label="info / warning / danger, and danger with an action">
          <div className="gallery-stack">
            <Notice tone="info">
              Draining stops readiness routing, so anything gating traffic on readiness stops sending it here.
            </Notice>
            <Notice tone="warning">
              Showing the last figures received. IA Local is not answering right now.
            </Notice>
            <Notice tone="danger">A drain does not survive a restart.</Notice>
            <Notice tone="danger" action={<Button variant="secondary">Retry</Button>}>
              IA Local is not reachable right now.
            </Notice>
          </div>
        </Row>
      </Section>

      <Section title="StatGroup">
        <Row label="figures, one with a hint">
          <StatGroup
            items={[
              { label: 'Requests', value: 18, mono: true },
              { label: 'Errors', value: 5, mono: true },
              { label: 'Slowest', value: '660 ms', mono: true },
              { label: 'Maintenance state', value: 'Running', hint: 'Serving normally.' },
            ]}
          />
        </Row>
      </Section>

      <Section title="TextField">
        <Row label="default / with clear / helper text">
          <TextField
            label="Model ID"
            value={textValue}
            onChange={(e) => setTextValue(e.target.value)}
            onClear={() => setTextValue('')}
            helperText="The active model's identifier on disk."
          />
        </Row>
        <Row label="error">
          <TextField label="Port" defaultValue="99999" error="Port must be between 1 and 65535." />
        </Row>
        <Row label="loading (async validation)">
          <TextField label="Workspace name" defaultValue="checking-availability" loading />
        </Row>
        <Row label="disabled">
          <TextField label="Locked field" defaultValue="cia-edge" disabled />
        </Row>
      </Section>

      <Section title="Divider">
        <Row label="horizontal">
          <div className="gallery-divider-demo">
            <span>Above</span>
            <Divider />
            <span>Below</span>
          </div>
        </Row>
        <Row label="vertical">
          <div className="gallery-divider-demo gallery-divider-demo--row">
            <span>Left</span>
            <Divider orientation="vertical" />
            <span>Right</span>
          </div>
        </Row>
      </Section>

      <Section title="Tooltip">
        <Row label="hover or tab to the button">
          <Tooltip content="Restarts the admission-control gate without unloading the active model.">
            <IconButton label="Restart gate" icon={<IconInfo />} />
          </Tooltip>
        </Row>
      </Section>

      <Section title="Skeleton">
        <Row label="text (single / multi-line) / circle / block">
          <div className="gallery-skeleton-demo">
            <div className="gallery-skeleton-demo__col">
              <Skeleton variant="text" />
            </div>
            <div className="gallery-skeleton-demo__col">
              <Skeleton variant="text" lines={3} />
            </div>
            <Skeleton variant="circle" size="lg" />
            <div className="gallery-skeleton-demo__block">
              <Skeleton variant="block" />
            </div>
          </div>
        </Row>
      </Section>
    </Surface>
  );
}
