# Changes Summary

## PR #429 — Bare image path detection

**Branch:** `feat/bare-image-paths`  
**Files:** `internal/app/input/on_textarea.go`, `internal/app/input/on_textarea_test.go`

Detect image file paths in user input — both `@`-prefixed and bare paths (drag-drop, or mentioned in text).

### Bare path regex (`bareImagePathRe`)
Matches any whitespace-delimited token ending in an image extension (`.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`) that contains a directory separator (`/` or `\`). This catches drag-drop paths and paths mentioned in conversation without triggering on bare filenames like `image.png`.

### `ProcessImageRefs` refactored into two-step pipeline
- **Step 1 ( `@` paths ):** Load the image, **remove the `@path` text** from the message, abort on load failure. The `@` prefix signals explicit intent.
- **Step 2 ( bare paths ):** Load the image, **keep the path text** in the message, skip silently on load failure. Text-only providers (DeepSeek) strip images before sending.

### Tests added
- `Test_bareImagePathRe` — 10 cases covering absolute paths, relative paths with separators, Windows paths, and non-matching text
- `TestProcessImageRefs` — 6 cases covering `@` load, bare path load, corrupt file handling, missing file, and no-image input

---

## PR #430 — File autocomplete shows all files + gitignore

**Branch:** `feat/file-autocomplete-gitignore`  
**File:** `internal/app/kit/suggest/suggest.go`

### Removed extension filter
Deleted `supportedFileExtensions` map that limited `@` suggestions to only `.md` and image files. Every file type now appears.

### Gitignore support
Added `loadGitignore()` parser and `gitignore.Matches()` matcher with support for:
- Negation (`!` prefix)
- Anchoring (`/` prefix)
- Directory-only (`/` suffix)
- `**` globs (basic support via `matchDoubleStar`)
- Basename vs full-path matching (per gitignore spec)

### Raised scan caps
- `fileScanMaxResults`: 500 → **2000**
- `fileScanMaxDirsVisited`: 2000 → **8000**

---

## PR #432 — Strip images from messages sent to DeepSeek

**Branch:** `feat/deepseek-strip-images`  
**File:** `internal/llm/deepseek/client.go`

DeepSeek's Chat Completions API rejects `image_url` content parts. When conversation history contains images — from bare-path detection or a prior session with a vision-capable model — those images get serialized as `image_url` and the request fails.

In `Client.Stream()`, strip `Images` from all messages before sending. Vision-capable providers (Gemini, Anthropic, OpenAI) are unaffected — this only applies to the DeepSeek client.

---

## Live branch fix — Project-root anchored `@` suggestions

**Branch:** `feat/all-changes`  
**Files:** `internal/app/env.go`, `internal/app/model_workspace.go`

When the model runs `cd` via Bash, `m.env.CWD` changes. Previously, `changeCwd()` fed the new CWD to `HandleCwdChange`, causing `@` file suggestions to show files from wherever the model `cd`'d to.

Added `ProjectRoot` field to `env` struct — set once at startup to `appCwd`. `changeCwd()` now passes `m.env.ProjectRoot` to suggestions instead of the runtime CWD, keeping `@` anchored to the original project root.
