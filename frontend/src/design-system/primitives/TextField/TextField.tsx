import { forwardRef, useId } from 'react';
import type { InputHTMLAttributes, ReactNode } from 'react';
import { IconAlertCircle, IconClose, IconSpinner } from '../icons';
import './TextField.css';

export type TextFieldSize = 'md' | 'sm';

export interface TextFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'size' | 'id'> {
  /** Always rendered as a real `<label for>` - this field has no icon-only/label-less mode. */
  label: string;
  id?: string;
  size?: TextFieldSize;
  /** Presence (even `""`) puts the field in the error state; absence means no error. */
  error?: string;
  /** Shown under the field when there is no error. */
  helperText?: string;
  /**
   * Async-validation affordance: shows a trailing spinner and marks the
   * input `aria-busy`, without disabling it - the operator can keep typing
   * while e.g. a name-uniqueness check runs.
   */
  loading?: boolean;
  leadingIcon?: ReactNode;
  /** Renders a trailing clear button; called on click/Enter/Space. */
  onClear?: () => void;
}

/**
 * States implemented: default, hover, focus-visible, disabled, loading
 * (via the async-validation affordance above), and error. "Pressed" and
 * "selected" don't apply to a free-text input - there is nothing to press
 * or select as a discrete unit.
 */
export const TextField = forwardRef<HTMLInputElement, TextFieldProps>(function TextField(
  {
    label,
    id,
    size = 'md',
    error,
    helperText,
    loading = false,
    leadingIcon,
    onClear,
    className,
    disabled,
    value,
    ...rest
  },
  ref,
) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const helperId = `${inputId}-helper`;
  const hasError = error !== undefined;
  const message = hasError ? error : helperText;

  const wrapperClasses = ['ds-text-field', `ds-text-field--${size}`];
  if (hasError) wrapperClasses.push('ds-text-field--error');
  if (disabled) wrapperClasses.push('ds-text-field--disabled');
  if (className) wrapperClasses.push(className);

  const showClear = Boolean(onClear) && !loading && !disabled && value !== undefined && value !== '';

  return (
    <div className={wrapperClasses.join(' ')}>
      <label className="ds-text-field__label" htmlFor={inputId}>
        {label}
      </label>
      <div className="ds-text-field__control">
        {leadingIcon && <span className="ds-text-field__icon ds-text-field__icon--leading">{leadingIcon}</span>}
        <input
          ref={ref}
          id={inputId}
          className="ds-text-field__input"
          disabled={disabled}
          value={value}
          aria-invalid={hasError || undefined}
          aria-busy={loading || undefined}
          aria-describedby={message ? helperId : undefined}
          {...rest}
        />
        {loading && (
          <span className="ds-text-field__icon ds-text-field__icon--trailing">
            <IconSpinner title="Validating" />
          </span>
        )}
        {!loading && hasError && (
          <span className="ds-text-field__icon ds-text-field__icon--trailing ds-text-field__icon--danger">
            <IconAlertCircle title="Error" />
          </span>
        )}
        {showClear && (
          <button
            type="button"
            className="ds-text-field__clear"
            onClick={onClear}
            aria-label={`Clear ${label}`}
          >
            <IconClose />
          </button>
        )}
      </div>
      {message && (
        <p className="ds-text-field__helper" id={helperId} role={hasError ? 'alert' : undefined}>
          {message}
        </p>
      )}
    </div>
  );
});
