import { useCallback, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useImagePaste } from '../../hooks/use-image-paste'

type PasteableTextareaProps = {
  stageId: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
  className?: string
  autoFocus?: boolean
  disabled?: boolean
  maxHeight?: number
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void
}

// Drop-in replacement for a plain <textarea>, used everywhere a user writes
// text that ends up in front of the agent (AgentNoteModal, DialogChannel's
// custom-answer and line-comment boxes, PlanPanel's line-comment box).
// Composes the existing auto-grow behavior with clipboard-image paste — Cmd+V
// with a screenshot on the clipboard uploads it and inserts a
// "[Screenshot: <path>]" reference at the caret; the agent reads the file
// itself when it decides to (see
// docs/superpowers/specs/2026-08-11-clipboard-image-paste-design.md).
export function PasteableTextarea({
  stageId,
  value,
  onChange,
  placeholder,
  className,
  autoFocus,
  disabled,
  maxHeight = 400,
  onKeyDown,
}: PasteableTextareaProps): ReactElement {
  const autoGrowRef = useAutoGrowTextarea(value, maxHeight)
  const { nodeRef, attachments, uploadError, onPaste, removeAttachment } = useImagePaste(stageId, value, onChange)

  const setRefs = useCallback(
    (node: HTMLTextAreaElement | null) => {
      nodeRef.current = node
      // Note: autoGrowRef is called after nodeRef is set, in a stable manner
      autoGrowRef(node)
    },
    [],
  )

  function handleChange(event: ChangeEvent<HTMLTextAreaElement>): void {
    onChange(event.target.value)
  }

  const showStrip = attachments.length > 0 || uploadError !== null

  return (
    <div className="pasteable-textarea-wrap">
      {showStrip && (
        <div className="pasteable-attachments">
          {attachments.map((attachment) => (
            <div key={attachment.id} className={`pasteable-attachment${attachment.uploading ? ' uploading' : ''}`}>
              <img src={attachment.previewUrl} alt="Pasted screenshot" />
              <button
                type="button"
                className="pasteable-attachment-remove"
                aria-label="Remove pasted image"
                onClick={() => removeAttachment(attachment.id)}
              >
                ✕
              </button>
            </div>
          ))}
          {uploadError !== null && <span className="pasteable-attachment-error">{uploadError}</span>}
        </div>
      )}
      <textarea
        ref={setRefs}
        className={className}
        value={value}
        placeholder={placeholder}
        autoFocus={autoFocus}
        disabled={disabled}
        onChange={handleChange}
        onPaste={onPaste}
        onKeyDown={onKeyDown}
      />
    </div>
  )
}
