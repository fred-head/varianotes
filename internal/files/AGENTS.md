# Notebook filesystem constraints

- Every read, write, move, and delete must remain inside the notebook root.
  Preserve traversal, symlink, regular-file, and race revalidation checks;
  use the existing confined-root helpers where applicable.
- Keep Markdown writes version-checked and atomic. Ambiguous asset ownership
  means retain the asset; cleanup candidates must be revalidated before delete.
- For path, persistence, asset-lifecycle, or destructive-operation changes,
  follow the focused filesystem checks in `../../docs/development/testing.md`.
