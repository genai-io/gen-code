# Changelog

All notable changes to San are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/).

## [v1.22.0] - 2026-07-24

### Added
- Add user-defined OpenAI- and Claude-compatible providers, configured with a base URL and API key ([@skeeey](https://github.com/skeeey) in [#404](https://github.com/genai-io/san/pull/404))
- Add `/simplify` as a built-in prompt command ([@yanmxa](https://github.com/yanmxa) in [#395](https://github.com/genai-io/san/pull/395))
- Show elapsed time and output size while Bash commands are running ([@yanmxa](https://github.com/yanmxa) in [#378](https://github.com/genai-io/san/pull/378))

### Changed
- Simplify agent selection around one implicit default runtime while preserving named custom agents and using unknown names as display labels ([@yanmxa](https://github.com/yanmxa) in [#410](https://github.com/genai-io/san/pull/410), [#417](https://github.com/genai-io/san/pull/417))
- Trim the built-in tool surface, consolidate Cron and task lookup actions, and ship optional Cron, task-tracker, and SendMessage tools disabled by default ([@yanmxa](https://github.com/yanmxa) in [#380](https://github.com/genai-io/san/pull/380), [#386](https://github.com/genai-io/san/pull/386), [#387](https://github.com/genai-io/san/pull/387))
- Rebuild `/tool` on the shared tabbed overlay and apply tool toggles on the next turn ([@yanmxa](https://github.com/yanmxa) in [#387](https://github.com/genai-io/san/pull/387))
- Reduce the system prompt and move tool-specific guidance into tool schemas ([@yanmxa](https://github.com/yanmxa) in [#390](https://github.com/genai-io/san/pull/390))
- Restrict bypass mode only with a root- and home-removal circuit breaker ([@yanmxa](https://github.com/yanmxa) in [#392](https://github.com/genai-io/san/pull/392))
- Nest and visually connect Bash and file results beneath their tool calls, with compact single-line Bash previews and full wrapped commands ([@yanmxa](https://github.com/yanmxa) in [#396](https://github.com/genai-io/san/pull/396), [#399](https://github.com/genai-io/san/pull/399), [#400](https://github.com/genai-io/san/pull/400), [#419](https://github.com/genai-io/san/pull/419))
- Expose Evolve through tool management and enable Autopilot Suggest by default while honoring explicit disablement ([@yanmxa](https://github.com/yanmxa) in [#419](https://github.com/genai-io/san/pull/419))

### Fixed
- Make Edit recover from whitespace mismatches and stale views, enforce current file observations, render inline diffs, and make Read truncation and empty-file results explicit ([@yanmxa](https://github.com/yanmxa) in [#388](https://github.com/genai-io/san/pull/388))
- Split agent stop control from spawning and stop background Bash process groups reliably ([@yanmxa](https://github.com/yanmxa) in [#393](https://github.com/genai-io/san/pull/393))
- Prevent stale, duplicated, reordered, or oversized commits from damaging native terminal scrollback ([@yanmxa](https://github.com/yanmxa) in [#401](https://github.com/genai-io/san/pull/401), [#403](https://github.com/genai-io/san/pull/403), [#405](https://github.com/genai-io/san/pull/405))
- Retry transient HTTP/2 and opaque stream-termination failures without retrying semantic or client errors ([@yanmxa](https://github.com/yanmxa) in [#406](https://github.com/genai-io/san/pull/406))
- Make MCP connections safe for concurrent per-agent use and preserve connection intent across reconnects, adoption, and reloads ([@hchenxa](https://github.com/hchenxa) in [#418](https://github.com/genai-io/san/pull/418))
- Honor globally disabled tools in subagents and avoid delegating clear, bounded work unnecessarily ([@yanmxa](https://github.com/yanmxa) in [#414](https://github.com/genai-io/san/pull/414))
- Preserve tool-result ordering by releasing queued input only after the complete tool-call batch finishes ([@yanmxa](https://github.com/yanmxa) in [#419](https://github.com/genai-io/san/pull/419))

## [v1.21.11] - 2026-07-21

### Added
- Add `/goal` for setting Autopilot goals and address related Autopilot defects ([@yanmxa](https://github.com/yanmxa) in [#349](https://github.com/genai-io/san/pull/349))
- Raise the Autopilot autonomy level ([@yanmxa](https://github.com/yanmxa) in [#347](https://github.com/genai-io/san/pull/347))
- Tint agent-owned tracker rows and fold finished-item overflow ([@yanmxa](https://github.com/yanmxa) in [#372](https://github.com/genai-io/san/pull/372))

### Changed
- Write files atomically through a shared helper ([@yanmxa](https://github.com/yanmxa) in [#354](https://github.com/genai-io/san/pull/354))

### Fixed
- Remove the streaming caret and its stranded vertical line ([@yanmxa](https://github.com/yanmxa) in [093765a](https://github.com/genai-io/san/commit/093765a231d0e7ec1628bfdd5949b9bd87a14b68), [7f5931a](https://github.com/genai-io/san/commit/7f5931a9a6594a07b2db11f3303374de2501f0fa))
- Prevent provider switches from crashing TUI cost accounting ([@yanmxa](https://github.com/yanmxa) in [#357](https://github.com/genai-io/san/pull/357))
- Keep MCP servers connected when the working directory changes ([@yanmxa](https://github.com/yanmxa) in [#361](https://github.com/genai-io/san/pull/361))
- Preserve plugin hooks across reloads ([@yanmxa](https://github.com/yanmxa) in [#362](https://github.com/genai-io/san/pull/362))
- Bound hooks awaited by the UI goroutine ([@yanmxa](https://github.com/yanmxa) in [#370](https://github.com/genai-io/san/pull/370))
- Rebuild damaged transcript indexes instead of hiding sessions ([@yanmxa](https://github.com/yanmxa) in [#363](https://github.com/genai-io/san/pull/363))
- Recover sessions with a torn final transcript record ([@yanmxa](https://github.com/yanmxa) in [#364](https://github.com/genai-io/san/pull/364))
- Keep lock-protected plugin state from escaping as live pointers ([@yanmxa](https://github.com/yanmxa) in [#366](https://github.com/genai-io/san/pull/366))
- Prevent MCP server teardown from freezing the TUI and agent ([@yanmxa](https://github.com/yanmxa) in [#367](https://github.com/genai-io/san/pull/367))
- Keep scheduled jobs in one San window from deleting another window's jobs ([@yanmxa](https://github.com/yanmxa) in [#371](https://github.com/genai-io/san/pull/371))
- Prevent a permission check from racing a mode switch ([@yanmxa](https://github.com/yanmxa) in [4b303c3](https://github.com/genai-io/san/commit/4b303c34736fb4480457514c883eecc05923d7f4))
- Measure TUI panel layouts in display columns ([@yanmxa](https://github.com/yanmxa) in [e16adfd](https://github.com/genai-io/san/commit/e16adfd3197ea92855196fdd28a415bc97821c90))
- Avoid signaling a reissued process group when stopping a task ([@yanmxa](https://github.com/yanmxa) in [f7d346b](https://github.com/genai-io/san/commit/f7d346ba1e2332000d027e44396a9a60e2afd2a1))
- Surface hooks that exit non-zero ([@yanmxa](https://github.com/yanmxa) in [970576f](https://github.com/genai-io/san/commit/970576f4c9db39e59b76bd279fcd28e24a0045d3))
- Keep `/fork` from writing fork history into its parent session ([@yanmxa](https://github.com/yanmxa) in [dc64c01](https://github.com/genai-io/san/commit/dc64c01271d9f6dbde1ec59008544277a9a549ae))
- Return a result and suppress `TurnEvent` when a turn fails ([@yanmxa](https://github.com/yanmxa) in [39794fd](https://github.com/genai-io/san/commit/39794fd2c23383a3ab886d63dd2e87827ff9942b), [81a2ef6](https://github.com/genai-io/san/commit/81a2ef62f74e713341af8c9c67e82de4b85005e6))
- Guard the package-level skill registry during concurrent reinitialization ([@yanmxa](https://github.com/yanmxa) in [4daeec4](https://github.com/genai-io/san/commit/4daeec4958166acce12e23e4247591aad846a113))
- Keep the task tracker parent-only for subagents ([@yanmxa](https://github.com/yanmxa) in [#373](https://github.com/genai-io/san/pull/373))
- Make graceful task stops complete gracefully and bound Bash task output ([@yanmxa](https://github.com/yanmxa) in [#350](https://github.com/genai-io/san/pull/350))
- Report model output whenever a turn exits ([@yanmxa](https://github.com/yanmxa) in [#351](https://github.com/genai-io/san/pull/351))
- Join tracker workers to task lifecycles rather than tool results ([@yanmxa](https://github.com/yanmxa) in [#348](https://github.com/genai-io/san/pull/348))
- Keep the tracker task list windowed on its newest items ([@yanmxa](https://github.com/yanmxa) in [#346](https://github.com/genai-io/san/pull/346))

## [v1.21.10] - 2026-07-20

### Fixed
- Derive tracker in-progress state from live executors ([@yanmxa](https://github.com/yanmxa) in [#342](https://github.com/genai-io/san/pull/342))
- Base auto-compaction on the full prompt, including cached tokens ([@yanmxa](https://github.com/yanmxa) in [#339](https://github.com/genai-io/san/pull/339))
- Bind Shift+Enter, place the real cursor, and size the input box by wrapping ([@yanmxa](https://github.com/yanmxa) in [#340](https://github.com/genai-io/san/pull/340))

## [v1.21.9] - 2026-07-20

### Added
- Support batched file edits and align the Edit tool with Pi format ([@yanmxa](https://github.com/yanmxa) in [#336](https://github.com/genai-io/san/pull/336))
- Release queued user messages at step boundaries and clarify background-agent output ([@yanmxa](https://github.com/yanmxa) in [#336](https://github.com/genai-io/san/pull/336))

### Changed
- Make built-in tools self-describing through their schemas ([@yanmxa](https://github.com/yanmxa) in [#331](https://github.com/genai-io/san/pull/331))
- Unify provider responses on the core inference response type ([@yanmxa](https://github.com/yanmxa) in [#332](https://github.com/genai-io/san/pull/332))
- Cache and compact session transcript indexes while simplifying session persistence ([@yanmxa](https://github.com/yanmxa) in [#334](https://github.com/genai-io/san/pull/334))
- Cap transcript index previews and reduce repeated conversation and agent rendering work ([@yanmxa](https://github.com/yanmxa) in [#335](https://github.com/genai-io/san/pull/335), [#336](https://github.com/genai-io/san/pull/336))
- Clarify the README, tool feedback, and subagent model-override documentation ([@yanmxa](https://github.com/yanmxa) in [#336](https://github.com/genai-io/san/pull/336))

### Fixed
- Synchronize provider state and model metadata ([@yanmxa](https://github.com/yanmxa) in [#333](https://github.com/genai-io/san/pull/333))
- Show queued messages when they are released and preserve Edit permission-rule path suggestions ([@yanmxa](https://github.com/yanmxa) in [#336](https://github.com/genai-io/san/pull/336))
- Preserve OpenAI reasoning summaries, auto-compaction progress, and the configured subagent model fallback ([@yanmxa](https://github.com/yanmxa) in [#336](https://github.com/genai-io/san/pull/336))

## [v1.21.8] - 2026-07-19

### Fixed
- Snapshot permission audit input before releasing the gate to prevent concurrent map access ([@yanmxa](https://github.com/yanmxa) in [#329](https://github.com/genai-io/san/pull/329))
- Inset and align the Bash prompt marker with result markers ([@yanmxa](https://github.com/yanmxa) in [9dbb4f5](https://github.com/genai-io/san/commit/9dbb4f59))

## [v1.21.7] - 2026-07-18

### Added
- Add JSON version output ([@hchenxa](https://github.com/hchenxa) in [#324](https://github.com/genai-io/san/pull/324))
- Add broker message routing and a flat subagent spawn-to-result model ([@yanmxa](https://github.com/yanmxa) in [bbde764](https://github.com/genai-io/san/commit/bbde7646))

### Changed
- Run subagents in the session working directory and simplify result delivery ([@yanmxa](https://github.com/yanmxa) in [63142c3](https://github.com/genai-io/san/commit/63142c30))
- Memoize model token limits per client ([@yanmxa](https://github.com/yanmxa) in [#322](https://github.com/genai-io/san/pull/322))

### Fixed
- Preserve live context and the agent chain across rebuilt sessions and restarts ([@yanmxa](https://github.com/yanmxa) in [b40d63e](https://github.com/genai-io/san/commit/b40d63e1))
- Give edit-mode subagents project instructions ([@yanmxa](https://github.com/yanmxa) in [31db935](https://github.com/genai-io/san/commit/31db935d))

## [v1.21.6] - 2026-07-18

### Added
- Render multi-line and long Bash commands as readable terminal blocks ([@yanmxa](https://github.com/yanmxa) in [#319](https://github.com/genai-io/san/pull/319))

## [v1.21.5] - 2026-07-17

### Added
- Add a dedicated `/evolve` panel for model-decided self-learning triggers ([@yanmxa](https://github.com/yanmxa) in [#311](https://github.com/genai-io/san/pull/311))
- Redesign queued-message interaction ([@yanmxa](https://github.com/yanmxa) in [#313](https://github.com/genai-io/san/pull/313))
- Add session naming features ([@hchenxa](https://github.com/hchenxa) in [3438c2c](https://github.com/genai-io/san/commit/3438c2c6))

### Changed
- Add contributor ladder documentation ([@yanmxa](https://github.com/yanmxa) in [#301](https://github.com/genai-io/san/pull/301))

### Fixed
- Fix agent timeout issue ([@hchenxa](https://github.com/hchenxa) in [66102d0](https://github.com/genai-io/san/commit/66102d0b))
- Fix installation issues when rate limited ([@hchenxa](https://github.com/hchenxa) in [1c879ae](https://github.com/genai-io/san/commit/1c879ae8))

## [v1.21.4] - 2026-07-11

### Added
- Discover OpenAI model reasoning capabilities dynamically ([@yanmxa](https://github.com/yanmxa) in [f67f5ba](https://github.com/genai-io/san/commit/f67f5bac15b47251031457021d6f4fe435c6df34))
- Show model descriptions in the provider picker ([@yanmxa](https://github.com/yanmxa) in [5e7d6b5](https://github.com/genai-io/san/commit/5e7d6b5612000493d3c3a5d8404fe8431d402836))

### Changed
- Resolve OpenAI reasoning capabilities live and simplify capability and authentication handling ([@yanmxa](https://github.com/yanmxa) in [0f6af4e](https://github.com/genai-io/san/commit/0f6af4ef825eb37372b004ef55c5fb2a5ad56a97))

## [v1.21.3] - 2026-07-10

### Added
- Guard Autopilot LLM steers with an immutable control-plane policy ([@yanmxa](https://github.com/yanmxa) in [26fc698](https://github.com/genai-io/san/commit/26fc698a5c1c310c95a0a79523687b8b119df2cb))

### Fixed
- Handle paste in the Autopilot overlay ([@yanmxa](https://github.com/yanmxa) in [160d772](https://github.com/genai-io/san/commit/160d772424f77cb0234402b9bae87119782c852c))
- Hold committed scrollback blocks visible across the print handoff ([@yanmxa](https://github.com/yanmxa) in [e02ed42](https://github.com/genai-io/san/commit/e02ed422d64c76c634cba4463049feefde99a9f4))

## [v1.21.2] - 2026-07-10

### Fixed
- Keep Autopilot system prompts session-scoped, preserve mission-only saves without rebuilding the reviewer, and refresh the Autopilot panel layout on terminal resize ([@yanmxa](https://github.com/yanmxa) in [339df84](https://github.com/genai-io/san/commit/339df8481d5c6ae3885c3d2a026ea7d47c591c96))
- Align the Autopilot continuation marker with tool-result trailers ([@yanmxa](https://github.com/yanmxa) in [d8bb6f6](https://github.com/genai-io/san/commit/d8bb6f64))
- Hide redundant successful Edit result summaries while keeping errors visible ([@yanmxa](https://github.com/yanmxa) in [787950b](https://github.com/genai-io/san/commit/787950bb))
- Thread Autopilot mission context into permission and bash prompt reviewers ([@yanmxa](https://github.com/yanmxa) in [2eb87bb](https://github.com/genai-io/san/commit/2eb87bb2))

## [v1.21.1] - 2026-07-10

### Changed
- Replace legacy PR workflows with the GitHub Apps flow ([@hchenxa](https://github.com/hchenxa) in [#291](https://github.com/genai-io/san/pull/291))
- Make the Autopilot mission dialog a direct editor with paste-friendly save, clear, and refine controls ([@yanmxa](https://github.com/yanmxa) in [#292](https://github.com/genai-io/san/pull/292))

## [v1.21.0] - 2026-07-09

### Added
- Add configurable Autopilot copilot settings, panel, and lifecycle steers ([@yanmxa](https://github.com/yanmxa) in [#286](https://github.com/genai-io/san/pull/286))
- Add Autopilot follow-ups with Suggest steer, hands-free start, and mission lifecycle ([@yanmxa](https://github.com/yanmxa) in [#287](https://github.com/genai-io/san/pull/287))

### Changed
- Improve Autopilot start flow and interactive bash handling ([@yanmxa](https://github.com/yanmxa) in [a320e52](https://github.com/genai-io/san/commit/a320e52c))

## [v1.20.11] - 2026-07-05

### Added
- Add comments in PRs when title lint fails ([@hchenxa](https://github.com/hchenxa) in [#281](https://github.com/genai-io/san/pull/281))

### Fixed
- Fix the chat window height ([@hchenxa](https://github.com/hchenxa) in [#283](https://github.com/genai-io/san/pull/283))
- Fix TUI display correctness across streaming, wrapping, and resume ([@yanmxa](https://github.com/yanmxa) in [#279](https://github.com/genai-io/san/pull/279))
- Order the tasks list by ID ([@hchenxa](https://github.com/hchenxa) in [#282](https://github.com/genai-io/san/pull/282))
- Correct the QA skill working path ([@hchenxa](https://github.com/hchenxa) in [#280](https://github.com/genai-io/san/pull/280))

## [v1.20.10] - 2026-07-05

### Added
- Auto-review permission mode ([@yanmxa](https://github.com/yanmxa) in [#270](https://github.com/genai-io/san/pull/270))
- Auto-review activity in the UI ([@yanmxa](https://github.com/yanmxa) in [#274](https://github.com/genai-io/san/pull/274))

### Changed
- Clarify codebase structure boundaries ([@yanmxa](https://github.com/yanmxa) in [#272](https://github.com/genai-io/san/pull/272))
- Render streaming markdown blocks off the UI goroutine ([@yanmxa](https://github.com/yanmxa) in [#275](https://github.com/genai-io/san/pull/275))
- Shorten the auto-review reason and scope per-frame result precompute ([@yanmxa](https://github.com/yanmxa) in [#276](https://github.com/genai-io/san/pull/276))
- Present the review mode to users as "Autopilot" and relax the judge ([@yanmxa](https://github.com/yanmxa) in [#277](https://github.com/genai-io/san/pull/277))

### Fixed
- Stop status-bar context limit flickering between provider windows ([@yanmxa](https://github.com/yanmxa) in [#271](https://github.com/genai-io/san/pull/271))

## [v1.20.9] - 2026-07-03

### Added
- ChatGPT subscription OAuth authentication for the OpenAI provider ([@yanmxa](https://github.com/yanmxa) in [#268](https://github.com/genai-io/san/pull/268))

## [v1.20.8] - 2026-07-03

### Added
- Binary self-update and upgrade support ([@hchenxa](https://github.com/hchenxa) in [#263](https://github.com/genai-io/san/pull/263))

### Fixed
- Keep interactive commands from hanging the TUI ([@yanmxa](https://github.com/yanmxa) in [#264](https://github.com/genai-io/san/pull/264))

## [v1.20.7] - 2026-06-27

### Added
- Chinese website translation ([@hchenxa](https://github.com/hchenxa) in [#251](https://github.com/genai-io/san/pull/251))

### Changed
- Refresh the README hero and consolidate intro copy ([@yanmxa](https://github.com/yanmxa) in [0274432](https://github.com/genai-io/san/commit/0274432e))
- Collapse the features overview image in README ([@yanmxa](https://github.com/yanmxa) in [041f730](https://github.com/genai-io/san/commit/041f730b))
- Fold the overview diagram under the open-architecture heading ([@yanmxa](https://github.com/yanmxa) in [5e9de7c](https://github.com/genai-io/san/commit/5e9de7c7))
- Drop the arrow in the overview-diagram summary ([@yanmxa](https://github.com/yanmxa) in [e4d971f](https://github.com/genai-io/san/commit/e4d971ff))

### Fixed
- Startup banner now visible from launch and tracks the selected model ([@yanmxa](https://github.com/yanmxa) in [3cb94e7](https://github.com/genai-io/san/commit/3cb94e7e))
- Defer the startup banner until a model is selected ([@yanmxa](https://github.com/yanmxa) in [be08db9](https://github.com/genai-io/san/commit/be08db9d))
- Hide the ctrl+o hint on frozen tool output ([@yanmxa](https://github.com/yanmxa) in [99914a3](https://github.com/genai-io/san/commit/99914a37))
- Stop re-rendering committed scrollback history ([@yanmxa](https://github.com/yanmxa) in [1b1ddb1](https://github.com/genai-io/san/commit/1b1ddb19))

## [v1.20.6] - 2026-06-18

### Added
- Subagent model overrides route across providers ([@yanmxa](https://github.com/yanmxa) in [#237](https://github.com/genai-io/san/pull/237))
- Z.ai GLM Coding Plan provider endpoint ([@wangke19](https://github.com/wangke19) in [#239](https://github.com/genai-io/san/pull/239))
- Agnes-AI provider ([@wangke19](https://github.com/wangke19) in [#236](https://github.com/genai-io/san/pull/236))

### Changed
- Add provider writing guide ([@onyx679](https://github.com/onyx679) in [#240](https://github.com/genai-io/san/pull/240))
- Document cross-provider model routing in the subagent guide ([@yanmxa](https://github.com/yanmxa) in [#237](https://github.com/genai-io/san/pull/237))
- Document `make ci` in contributing guide ([@onyx679](https://github.com/onyx679) in [#238](https://github.com/genai-io/san/pull/238))
- Guard release skill against running off main ([@yanmxa](https://github.com/yanmxa) in [#244](https://github.com/genai-io/san/pull/244))
- Add Product Hunt launch kit ([@yanmxa](https://github.com/yanmxa) in [88ccfbd](https://github.com/genai-io/san/commit/88ccfbd7))

### Fixed
- Resolve context window by model ID across all caches ([@yanmxa](https://github.com/yanmxa) in [#243](https://github.com/genai-io/san/pull/243))
- Gate vendor/model routing on registered providers ([@yanmxa](https://github.com/yanmxa) in [#237](https://github.com/genai-io/san/pull/237))
- Use ApplyPersonaOverlay and guard model override in `-p --persona` ([@dangpeng](https://github.com/dangpeng) in [#231](https://github.com/genai-io/san/pull/231))
- Make `--persona` take effect in `-p` print mode ([@dangpeng](https://github.com/dangpeng) in [#231](https://github.com/genai-io/san/pull/231))

## [v1.20.5] - 2026-06-16

### Fixed
- Split cached prompt tokens from fresh input for OpenAI-compatible providers, so per-turn input usage no longer multi-counts the re-read cache and cost applies the cache-read rate ([@yanmxa](https://github.com/yanmxa) in [#234](https://github.com/genai-io/san/pull/234))

## [v1.20.4] - 2026-06-16

### Fixed
- Truncate by display width so CJK rows don't overflow ([@yanmxa](https://github.com/yanmxa) in [6685385](https://github.com/genai-io/san/commit/66853859))
- Stop ToolToggleMsg from leaking into sub-model routing ([@yanmxa](https://github.com/yanmxa) in [9aaf344](https://github.com/genai-io/san/commit/9aaf3448))
- Enable Ctrl+V paste to API key input ([@dangpeng](https://github.com/dangpeng) in [edbe190](https://github.com/genai-io/san/commit/edbe1907))
- Count cached system prompt in context usage ([@yanmxa](https://github.com/yanmxa) in [3804db8](https://github.com/genai-io/san/commit/3804db85))
- Keep conversation cost across compaction ([@yanmxa](https://github.com/yanmxa) in [07bdce3](https://github.com/genai-io/san/commit/07bdce37))
- Clear native scrollback on /clear ([@yanmxa](https://github.com/yanmxa) in [2b94daa](https://github.com/genai-io/san/commit/2b94daa9))
- Exclude system-reminder blocks from resumed display text ([@yanmxa](https://github.com/yanmxa) in [7790790](https://github.com/genai-io/san/commit/77907907))
- Stop reprinting the brand banner on model switch ([@yanmxa](https://github.com/yanmxa) in [dfe0b71](https://github.com/genai-io/san/commit/dfe0b71c))
- Always treat typed keys as search in list selectors ([@yanmxa](https://github.com/yanmxa) in [e6cca10](https://github.com/genai-io/san/commit/e6cca107))
- Hold images back from text-only models instead of erroring ([@yanmxa](https://github.com/yanmxa) in [75fbe58](https://github.com/genai-io/san/commit/75fbe581))

## [v1.20.3] - 2026-06-15

### Fixed
- Sanitize plugin MCP server names to avoid invalid LLM tool names ([@yanmxa](https://github.com/yanmxa) in [822c400](https://github.com/genai-io/san/commit/822c4008))
- Deduplicate installed plugin entries, inline action status, fix list layout ([@yanmxa](https://github.com/yanmxa) in [2d21022](https://github.com/genai-io/san/commit/2d210229))
- Forward MCP server stderr to the log instead of the terminal ([@yanmxa](https://github.com/yanmxa) in [ea8fc5e](https://github.com/genai-io/san/commit/ea8fc5e1))
- Don't re-sync marketplace on plugin install; keep spinner inline ([@yanmxa](https://github.com/yanmxa) in [c4dafc3](https://github.com/genai-io/san/commit/c4dafc34))
- Source plugin skills from the enabled-plugin registry instead of all loaded plugins ([@yanmxa](https://github.com/yanmxa) in [#213](https://github.com/genai-io/san/pull/213))

## [v1.20.2] - 2026-06-13

### Added
- Persona system: switchable bundles of custom prompt parts (identity/behavior/rules), bundled skills, settings overlay, and a subagent allow-list. `/persona <name>` switches directly; bare `/persona` opens an interactive picker (Enter to switch, Ctrl+O to open files, Ctrl+D to delete). `/identity` is now a `/persona` alias. ([@yanmxa](https://github.com/yanmxa) in [f4947cf](https://github.com/genai-io/san/commit/f4947cf), [9ff746f](https://github.com/genai-io/san/commit/9ff746f), [317c9fd](https://github.com/genai-io/san/commit/317c9fd), [5f0cf63](https://github.com/genai-io/san/commit/5f0cf63), [33ebb6f](https://github.com/genai-io/san/commit/33ebb6f), [cc2fa45](https://github.com/genai-io/san/commit/cc2fa45), [befb2fa](https://github.com/genai-io/san/commit/befb2fa), [ef9b407](https://github.com/genai-io/san/commit/ef9b407), [ac90b90](https://github.com/genai-io/san/commit/ac90b90), [f262ed3](https://github.com/genai-io/san/commit/f262ed3)))
- PreToolUse hook: run hooks in the main-session tool path before permission checks and execution ([@zhfeng](https://github.com/zhfeng) in [cdaee75](https://github.com/genai-io/san/commit/cdaee75))

### Changed
- Split monolithic `on_provider.go` into convention-named siblings by concern (select, credentials, connect, nav, data, types) ([@yanmxa](https://github.com/yanmxa) in [9e239ca](https://github.com/genai-io/san/commit/9e239ca))
- Merge prompt template files to mirror the four system-prompt parts ([@yanmxa](https://github.com/yanmxa) in [a2cbaec](https://github.com/genai-io/san/commit/a2cbaec))
- Tune system-prompt content: align security stance, deduplicate reversibility rules, rename "Behavior" to "Honesty" ([@yanmxa](https://github.com/yanmxa) in [4be9d29](https://github.com/genai-io/san/commit/4be9d29))
- Drop legacy identity back-compat and remove dead identity selector / `internal/identity` package ([@yanmxa](https://github.com/yanmxa) in [029e021](https://github.com/genai-io/san/commit/029e021), [6bbdc21](https://github.com/genai-io/san/commit/6bbdc21))
- Split installation instructions by platform in README and site ([@yanmxa](https://github.com/yanmxa) in [80a452f](https://github.com/genai-io/san/commit/80a452f))
- Move persona design note from active notes into `docs/concepts` ([@yanmxa](https://github.com/yanmxa) in [0901aa9](https://github.com/genai-io/san/commit/0901aa9))

### Fixed
- PreToolUse permission edge cases: skip decider when hook forces a prompt, let "ask" reach the user, consistent "blocked:" error prefix ([@yanmxa](https://github.com/yanmxa) in [c73525e](https://github.com/genai-io/san/commit/c73525e))
- `make install` replaces the binary via a fresh inode to fix macOS AMFI code-signature cache rejection ([@yanmxa](https://github.com/yanmxa) in [6bf0277](https://github.com/genai-io/san/commit/6bf0277))
- Persona switch persists at the correct scope when a project-pinned persona is active ([@yanmxa](https://github.com/yanmxa) in [99473ab](https://github.com/genai-io/san/commit/99473ab))

## [v1.20.1] - 2026-06-11

### Added
- Volcengine Ark LLM provider ([@zhfeng](https://github.com/zhfeng) in [#178](https://github.com/genai-io/san/pull/178))
- Windows PowerShell installer and zip release artifacts ([@yanmxa](https://github.com/yanmxa) in [#159](https://github.com/genai-io/san/pull/159))
- Appearance panel to switch color theme in TUI ([@yanmxa](https://github.com/yanmxa) in [#149](https://github.com/genai-io/san/pull/149))
- Ctrl+D to remove API key with confirmation ([@zhfeng](https://github.com/zhfeng) in [#158](https://github.com/genai-io/san/pull/158))
- Ctrl+E to edit API key in place ([@laisongls](https://github.com/laisongls) in [#154](https://github.com/genai-io/san/pull/154))
- Windows builds in release artifacts ([@zhfeng](https://github.com/zhfeng) in [#148](https://github.com/genai-io/san/pull/148))
- Persona system design documentation ([@yanmxa](https://github.com/yanmxa) in [#144](https://github.com/genai-io/san/pull/144))

### Changed
- Simplify system prompt to four-part structure ([@yanmxa](https://github.com/yanmxa) in [#171](https://github.com/genai-io/san/pull/171))
- Derive provider list from LLM registry instead of embedded catalog ([@zhfeng](https://github.com/zhfeng) in [#151](https://github.com/genai-io/san/pull/151))
- Unify and deduplicate message, agent, and tool types across the codebase ([@yanmxa](https://github.com/yanmxa) in [#132](https://github.com/genai-io/san/pull/132), [#138](https://github.com/genai-io/san/pull/138), [#139](https://github.com/genai-io/san/pull/139), [#141](https://github.com/genai-io/san/pull/141))
- Remove dead fields: `Config.Color`, `Message.Meta`, `HookInput.IsInterrupt`, `SessionPermissions.IsBypassAvailable` ([@yanmxa](https://github.com/yanmxa) in [#140](https://github.com/genai-io/san/pull/140), [#137](https://github.com/genai-io/san/pull/137), [#146](https://github.com/genai-io/san/pull/146), [#142](https://github.com/genai-io/san/pull/142))
- Unify token-usage naming; report agent input/output separately ([@yanmxa](https://github.com/yanmxa) in [#136](https://github.com/genai-io/san/pull/136))
- Rename compaction `Focus` to `SummaryFocus` ([@yanmxa](https://github.com/yanmxa) in [#143](https://github.com/genai-io/san/pull/143))
- Drop `Message.From`, pass agent ID to `MessageEvent` explicitly ([@yanmxa](https://github.com/yanmxa) in [#147](https://github.com/genai-io/san/pull/147))
- Update provider documentation ([@wangke19](https://github.com/wangke19) in [#174](https://github.com/genai-io/san/pull/174))

### Fixed
- Sync install.ps1 logic with install.sh ([@lonicerae](https://github.com/lonicerae) in [cd8f81d](https://github.com/genai-io/san/commit/cd8f81d))
- Install script hardening ([@lonicerae](https://github.com/lonicerae) in [#162](https://github.com/genai-io/san/pull/162))
- Clear runtime model/provider state on credential removal ([@skeeey](https://github.com/skeeey) in [#161](https://github.com/genai-io/san/pull/161))
- Persist OLLAMA_BASE_URL across sessions ([@lonicerae](https://github.com/lonicerae) in [#122](https://github.com/genai-io/san/pull/122))
- Add Mimo provider to UI list and resolve base URL from secrets ([@zhfeng](https://github.com/zhfeng) in [#150](https://github.com/genai-io/san/pull/150))
- Update welcome header model name on model switch ([@zhfeng](https://github.com/zhfeng) in [#153](https://github.com/genai-io/san/pull/153))
- Skip pages deploy workflow in forked repos ([@zhfeng](https://github.com/zhfeng) in [#145](https://github.com/genai-io/san/pull/145))
- Fix flaky concurrency-cap retry test hang ([@yanmxa](https://github.com/yanmxa) in [#135](https://github.com/genai-io/san/pull/135))

## [v1.20.0] - 2026-06-06

### Added
- Xiaomi MiMo LLM provider ([@zhfeng](https://github.com/zhfeng) in [#106](https://github.com/genai-io/san/pull/106))
- SenseNova (商汤日日新) LLM provider ([@wangke19](https://github.com/wangke19) in [#115](https://github.com/genai-io/san/pull/115))
- Ollama as LLM provider ([@zhiweiyin](https://github.com/zhiweiyin) in [#90](https://github.com/genai-io/san/pull/90))
- Blank model selection via blank input in TUI ([@hchenxa](https://github.com/hchenxa) in [#85](https://github.com/genai-io/san/pull/85))
- Inspector user guide in English and Chinese ([@ldpliu](https://github.com/ldpliu) in [#86](https://github.com/genai-io/san/pull/86))
- WeChat 公众号 and Slack QR codes in the community section ([@yanmxa](https://github.com/yanmxa) in [#104](https://github.com/genai-io/san/pull/104))

### Changed
- **Breaking:** Rename project from gen-code/gen to san (三) ([@yanmxa](https://github.com/yanmxa) in [#96](https://github.com/genai-io/san/pull/96))
- Website: reposition as a unified agent runtime; editorial-terminal landing fused with the animated intro ([@yanmxa](https://github.com/yanmxa) in [#93](https://github.com/genai-io/san/pull/93), [#100](https://github.com/genai-io/san/pull/100))
- Rename the in-turn loop counter to "steps", reserve "turn" for the Think→Act cycle ([@yanmxa](https://github.com/yanmxa) in [#94](https://github.com/genai-io/san/pull/94))
- Merge LLM ClientFactory + Setup into a single Conn handle ([@yanmxa](https://github.com/yanmxa) in [#116](https://github.com/genai-io/san/pull/116))
- Move plugin install + marketplace-sync orchestration into internal/plugin ([@yanmxa](https://github.com/yanmxa) in [#125](https://github.com/genai-io/san/pull/125))
- Unify selector list-filter method to updateFilter ([@yanmxa](https://github.com/yanmxa) in [#124](https://github.com/genai-io/san/pull/124))
- Display timestamps in a more readable format ([@lonicerae](https://github.com/lonicerae) in [#117](https://github.com/genai-io/san/pull/117))
- Report test coverage to Codecov; add Go Report Card and Codecov badges ([@yanmxa](https://github.com/yanmxa) in [#128](https://github.com/genai-io/san/pull/128))
- Harden CI: PR commands, title lint, stale bot, and dependabot ([@ldpliu](https://github.com/ldpliu) in [#103](https://github.com/genai-io/san/pull/103))

### Fixed
- Banner shows model display name, status bar shows model ID ([@yanmxa](https://github.com/yanmxa) in [#101](https://github.com/genai-io/san/pull/101))
- Persist provider base URLs and Vertex region/project across sessions ([@yanmxa](https://github.com/yanmxa) in [#107](https://github.com/genai-io/san/pull/107))
- Disable cgo for static builds to support older glibc ([@yanmxa](https://github.com/yanmxa) in [#109](https://github.com/genai-io/san/pull/109))
- Clean up gen-code/gen legacy references in code, docs, and assets ([@yanmxa](https://github.com/yanmxa) in [#97](https://github.com/genai-io/san/pull/97))
- Drop gen backward compatibility, finish rebrand touches ([@yanmxa](https://github.com/yanmxa) in [#98](https://github.com/genai-io/san/pull/98))

## [v1.19.3] - 2026-06-03

### Added
- Scroll command suggestions in TUI ([@hchenxa](https://github.com/hchenxa) in [9dbb55a](https://github.com/genai-io/san/commit/9dbb55a))
- Quit/exit commands ([@hchenxa](https://github.com/hchenxa) in [#83](https://github.com/genai-io/san/pull/83))
- OWNERS file ([@hchenxa](https://github.com/hchenxa) in [9dbb55a](https://github.com/genai-io/san/commit/9dbb55a))

## [v1.19.2] - 2026-06-02

### Added
- Self-learning system: L1 background reviewer, project-partitioned memory store, skill_manage tool, action permission system, and runtime UI with braille progress spinner
- /config Self-Learning panel with extensible layout, scope/value controls, and persistence
- Skip `<system-reminder>` blocks during compaction and re-attach them after
- Inspector: expandable active message chain rows
- Landing page with GitHub Pages deploy, Getting Started page, and Chinese README

### Changed
- Rename compaction `BoundaryID` to `SummaryMessageID` across transcript and compact packages
- Rename provider `IsBusy`→`IsConnecting` and spinner tick→`ProviderConnectingMsg`
- Rename reminder helpers for clarity (`RefreshSystemReminders`→`RequeueSystemReminders`, etc.)
- Tighten system-reminders guideline to two bullets
- Simplify `waitForInput` with an `ingestBatch` helper
- Self-learn refactors: invert permission polarity to deny-encoded defaults, structured recap from action log, dead export cleanup

### Fixed
- Self-learn: config persistence, lifecycle hardening, CI layer violations, security and correctness fixes
- Compaction: use ≡ icon, show summary as system notice (not user turn), drop completed SESSION SUMMARY box, unify manual /compact in place, record summary + boundary in transcript, robust reminder stripping
- Provider: single-flight connect/refresh, drop dead style branch, tidy list layout with animated refresh status
- Windows: handle drive letter and backslash in session path encoding; make build compile by isolating Unix-only syscalls; handle-based kill with group-aware shutdowns
- Drop unused `path/filepath` import in session package

## [v1.19.1] - 2026-05-23

### Fixed
- Broaden @-file recall, cache scans, and smooth viewport in suggest

## [v1.19.0] - 2026-05-23

### Added
- Welcome splash screen with ❭ input prompt glyph
- "auto" theme to match terminal appearance automatically (zhujian)
- Persist thinking effort per model across launches
- Concept documentation: data-flow (en + zh), rendering (en + zh)

### Changed
- Rotate the thinking spinner and agent task indicator instead of flickering
- Cancel/interrupt flow: quiescence handshake, pending latch, defensive fixes across agent/llm/conv layers
- Refactor app model: split model.go (1103 lines) and update.go (821 lines)
- Collapse submit-path indirection; centralize agent submission into SubmitToAgent
- Drop Service interfaces across 7 packages; use concrete types (session, subagent, skill, plugin, mcp, hook)
- Rename Command* → SlashCommand*, overlay → popup, Pairs → InlinedResults, for clarity
- Restructure docs into goal-axis taxonomy with per-package contracts

### Fixed
- Drop thinking-only assistant messages before sending to LLM
- /agent and /skills tab switching skips empty tabs despite help hint (zhujian)
- Preserve old agent IDs across ResyncMessages reconciliation
- Skip interrupt marker in ExtractLastUserText
- Wake Update loop on background hub events
- Remove dead autoTheme variable from theme init (zhujian)

## [v1.18.0] - 2026-05-17

### Added
- Add native DeepSeek provider with updated model catalog and V4 readiness checks (zhujian)
- Add Claude model catalog updates including 1M context support (zhujian)
- Add trace recorder for inference, system, tools, and content provenance (Meng Yan)
- Add web viewer for session tracing and inspection (Meng Yan)

### Changed
- Rename trace concepts to inspector and update related system prompts (Meng Yan)
- Unify record/payload naming and append-only transcript persistence with fsync batching (Meng Yan)
- Refine README feature, usage, configuration, skills, extensions, and open-architecture documentation (Meng Yan)

### Fixed
- Canonicalize `/model` command usage and remove the `/provider` alias (zhujian, Meng Yan)
- Fix stable message IDs, unconditional state patches, and early `session.started` telemetry writes (Meng Yan)
- Escape session IDs in the viewer and deduplicate SSE records across reconnects (Meng Yan)
- Address DeepSeek provider review feedback (zhujian)

## [v1.17.4] - 2026-05-06

### Changed
- Simplify input queue to single-source-of-truth FIFO model, removing SentToInbox/WaitingCount tracking and the "waiting" badge

## [v1.17.3] - 2026-05-06

### Added
- Tavily search provider

### Changed
- Rename BigModel provider display to Z.ai (GLM series)

### Fixed
- Restore Exa search provider after MCP endpoint changes

## [v1.17.2] - 2026-05-06

### Added
- BigModel (Zhipu GLM) LLM provider

### Changed
- Add queue depth metrics and improve queue processing

## [v1.17.1] - 2026-05-05

### Added
- Manual feature documentation for v1.17

### Changed
- Remove dead code and modernize Go patterns

## [v1.17.0] - 2026-05-04

### Added
- Reminder system for proactive context injection during agent execution

### Changed
- Streamlined extensibility documentation in README
- Updated benchmark documentation title
- Updated CHANGELOG with latest changes

## [v1.16.0] - 2026-05-04

### Added
- Open Identity: configurable assistant personas as markdown files at user or project scope; switch with `/identity`. Built-in `identity create` / `identity edit` workflows and auto-generated user-level template.
- Structured system prompt catalog: layered Slot/Section model with hot-patching (`Use` / `Drop` / `Refresh`).
- Reusable panel rendering for input-view selectors.

### Changed
- System prompt assembly refactored around `Section` and `Scope` types; subagent identity is replaced rather than overlaid.
- Documentation reorganized; new `docs/system-prompt.md` consolidates prompt design.

### Removed
- Agent fork mode (`Agent(fork: true)`) — subagents always start with fresh context.
- Legacy prompt template files (`base.txt`, `tools-*.txt`); replaced by `prompts/identity.txt`, `prompts/policy.txt`, `prompts/guidelines/*.txt`.

## [v1.15.14] - 2026-05-02

### Fixed
- Operation mode indicator icon and hint text.

## [v1.15.13] - 2026-05-02

### Removed
- Obsolete permission documentation.

## [v1.15.12] - 2026-05-02

### Added
- Permission system with mode-based access control for agents and tools.
- Subagent matching and routing logic.
- Permission docs (`docs/claude-permission.md`, `docs/san-permission.md`).

### Changed
- Subagent executor / loader / registry refactored for type safety.
- Improved bash AST parsing and settings merger.

## [v1.15.11] - 2026-05-01

### Added
- Permission modes for agent execution: `explore`, `edit`, `default`.
- Agent name display logic with generic vs. custom name handling.

### Changed
- Renamed `continueagent` to `continuation`; removed deferred tool.
- Improved progress tracking and queue preview UX.

## [v1.15.10] - 2026-05-01

### Fixed
- Test signatures aligned with updated `renderTask` and queue preview design.

## [v1.15.9] - 2026-05-01

### Added
- Queue methods `DequeuePending` and `RemoveSentToInbox` for precise sent-item lifecycle.
- `HandleAgentMessage` for processing agent-injected user messages.

### Fixed
- Queue input injection: properly remove injected queued items and hold turn boundary until agent confirms.

## [v1.15.8] - 2026-04-30

### Added
- Queue selection: `Up` / `Down` navigate between queue items and history entries.
- OpenAI model token limits fetched from official docs with caching.

### Changed
- Tool execution: parallel only for read-only batches; sequential when side effects are possible.
- Edit tool: clearer error messages when `old_string` is missing or non-unique.
- System prompts: clarify that dependent tool calls must not be batched.
- Queue selected-item styling.

### Fixed
- Release workflow: full git history checkout for CHANGELOG section parsing.

## [v1.15.7] - 2026-04-30

### Changed
- Bind thinking effort to `Ctrl+T`.

### Fixed
- Conversation message handling.

## [v1.15.6] - 2026-04-29

### Fixed
- Min / max item constraints in `AskUserQuestion` schemas.

### Changed
- Release metadata.

## [v1.15.5] - 2026-04-26

### Removed
- Timer model render.

## [v1.15.4] - 2026-04-25

### Added
- MiniMax LLM provider (M2.x family, including Highspeed variants).

### Changed
- README updated with MiniMax provider information.

## [v1.15.3] - 2026-04-25

### Changed
- Refactored Anthropic and OpenAI clients with catalog support.
- Added catalog tests for Anthropic and OpenAI providers.

### Removed
- Thinking-level handling and related model configuration.

### Fixed
- Vertex AI integration for Anthropic models.

## [v1.15.2] - 2026-04-24

### Changed
- CI: use the current changelog section as release notes.
- Build: add `release-push` make target.

### Fixed
- v1.15.1 release notes show only the current version section.

## [v1.15.1] - 2026-04-24

### Fixed
- Hide queue badges and preview entries for items already sent.
- Keep queue selection focused on the last pending item; exit selection when no longer pending.
- Preserve assistant tool-call rendering while tool results are still arriving.
- Summarize repeated tool calls instead of duplicating output.
- Attach `CHANGELOG.md` to GitHub release artifacts.

## [v1.15.0] - 2026-04-24

### Added
- MiniMax provider (initial integration: API key, catalog, client).
- LLM cost tracking via `Money` and `Cost` types.
- Per-message cost tracking in conversations.
- Provider selection and model enrichment.

### Fixed
- API compatibility error handling.
