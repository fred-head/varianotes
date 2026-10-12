# Markdown editor constraints

- Markdown remains the persisted document format. Keep editor changes
  serializable and portable; cover changed behavior in `MarkdownEditor.test.tsx`.
- `readOnly` mode must not mutate the document. Removing an image node must not
  delete its asset; cleanup is a separate explicit workflow.
- Keep pasted/selected images on the existing upload path so they remain
  ordinary note-owned assets referenced by relative Markdown paths.
