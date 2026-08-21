package setting

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// destructiveCommands are patterns that should always require user confirmation,
// even when session permissions like AllowAllBash are enabled.
// These commands can cause irreversible data loss or system damage, and no
// judge may lift them — see gitDiscardingCommands for the recoverable tier.
var destructiveCommands = []string{
	"rm:-rf",
	"rm:-fr",
	"rm:-r",
	"chmod:777",
	"chmod:-R 777",
	":(){ :|:& };:", // fork bomb
	"> /dev/",       // device writes
	"dd:if=",        // direct disk access
	"mkfs",          // filesystem creation
	"fdisk",         // disk partitioning
	// Privilege escalation & persistence — confirm even in auto-review, since a
	// wrong auto-approval here installs a backdoor or survives the session.
	"sudo:",      // run as another user
	"visudo",     // edit sudoers
	"chsh:",      // change the login shell
	"crontab:",   // schedule persistent jobs
	"launchctl:", // macOS launch agents / daemons
}

// gitDiscardingCommands are the git operations that throw work away or rewrite
// history. They are never silently allowed either — but unlike the list above,
// git can bring their effect back (the reflog, other clones), and inside a
// repository they are ordinary tools of the trade. That makes them the one tier
// the AutoPilot judge may weigh against the session's intent instead of costing
// a human interrupt every time; every other gate treats them exactly like the
// destructive list, since none of those has a judge in the loop.
var gitDiscardingCommands = []string{
	"git:reset --hard",
	"git:clean -fd",
	"git:clean -f",
	"git:push -f",
	"git:checkout --",
	"git:stash drop",
	"git:stash clear",
	"git:branch -D",
	"git:branch -d -f",
}

// isDestructiveCommand checks if a bash command matches any destructive pattern.
// Returns true if the command should always require user confirmation.
func isDestructiveCommand(cmd string) bool {
	return matchesBashPatterns(cmd, destructiveCommands)
}

// isRootOrHomeRemoval reports whether the command contains a recursive rm
// aimed at the filesystem root or the home directory — the circuit breaker
// that even bypass mode cannot skip (CircuitBreakerReason). Substitution
// forms ($(rm -rf ~), backticks) are caught because extractCommandsAST
// descends into them; a command that fails to parse falls back to a coarse
// token scan rather than passing silently.
func isRootOrHomeRemoval(cmd string) bool {
	file := parseBashAST(cmd)
	if file == nil {
		return crudeRootOrHomeRemovalScan(cmd)
	}
	for _, c := range extractCommandsAST(file) {
		name, args := c.Name, c.Args
		// Unwrap sudo, and after it any neutral wrapper it exposed, so
		// "sudo rm -rf /" and "sudo timeout 5 rm -rf /" trip too. Arg skipping
		// reuses looksLikeCommand — the same heuristic extractFromCall applies
		// when it strips wrappers — so the two paths cannot drift.
		for (name == "sudo" || safeWrapperCommands[name]) && len(args) > 0 {
			for len(args) > 0 && !looksLikeCommand(args[0]) {
				args = args[1:]
			}
			if len(args) == 0 {
				break
			}
			name, args = filepath.Base(args[0]), args[1:]
		}
		if name != "rm" {
			continue
		}
		recursive := false
		var targets []string
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				if a == "--recursive" || (!strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR")) {
					recursive = true
				}
				continue
			}
			targets = append(targets, a)
		}
		if recursive && slices.ContainsFunc(targets, isRootOrHomePath) {
			return true
		}
	}
	return false
}

// crudeRootOrHomeRemovalScan is the parse-failure fallback for
// isRootOrHomeRemoval: a flat token scan that trades precision for never
// letting an unparseable root/home removal through.
func crudeRootOrHomeRemovalScan(cmd string) bool {
	hasRM, recursive, rootHome := false, false, false
	for f := range strings.FieldsSeq(cmd) {
		switch {
		case f == "rm" || strings.HasSuffix(f, "/rm"):
			hasRM = true
		case strings.HasPrefix(f, "-") && strings.ContainsAny(f, "rR"):
			recursive = true
		case isRootOrHomePath(f):
			rootHome = true
		}
	}
	return hasRM && recursive && rootHome
}

// isRootOrHomePath reports whether an rm target denotes the filesystem root
// or the home directory itself (including ~/*-style glob forms). Paths BELOW
// home ("~/project") are ordinary destructive targets, not circuit-breaker ones.
func isRootOrHomePath(target string) bool {
	if target == "/" || target == "/*" {
		return true
	}
	t := strings.TrimSuffix(target, "/*")
	t = strings.TrimSuffix(t, "/")
	switch t {
	case "~", "$HOME", "${HOME}":
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if t == strings.TrimSuffix(home, "/") {
			return true
		}
	}
	return false
}

// isGitDiscardingCommand reports whether the command is a git operation that
// discards work — confirmation-worthy, but recoverable, so the judge may weigh
// it. "git push --force" is matched by parsing the flags rather than by pattern,
// so "--force-with-lease" and "--force-if-includes" stay out of it. One parse of
// the command line covers both checks; this runs on every tool call.
func isGitDiscardingCommand(cmd string) bool {
	for _, normalized := range normalizedBashCommands(cmd) {
		if matchesAnyPattern(normalized, gitDiscardingCommands) {
			return true
		}
		if after, ok := strings.CutPrefix(normalized, "git:push "); ok &&
			slices.Contains(strings.Fields(after), "--force") {
			return true
		}
	}
	return false
}

func matchesBashPatterns(cmd string, patterns []string) bool {
	for _, normalized := range normalizedBashCommands(cmd) {
		if matchesAnyPattern(normalized, patterns) {
			return true
		}
	}
	return false
}

func matchesAnyPattern(normalized string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

// readOnlyBashCommands are commands that only inspect files or system state.
// A Bash invocation passes IsReadOnlyBashCommand when every command in the
// chain is on this list (or is a read-only git command), subject to the
// structural guards below. Such invocations are treated like the dedicated
// read-only tools: auto-permitted in every mode, including read-only/explore.
var readOnlyBashCommands = map[string]bool{
	"rg":       true,
	"grep":     true,
	"egrep":    true,
	"fgrep":    true,
	"find":     true,
	"fd":       true,
	"ls":       true,
	"tree":     true,
	"cat":      true,
	"head":     true,
	"tail":     true,
	"wc":       true,
	"stat":     true,
	"file":     true,
	"du":       true,
	"which":    true,
	"pwd":      true,
	"cd":       true,
	"basename": true,
	"dirname":  true,
	"realpath": true,
	"readlink": true,
}

// readOnlyBashUnsafeFlags marks flags that let an otherwise read-only command
// execute subprocesses or write files. Matched by prefix on each argument.
var readOnlyBashUnsafeFlags = map[string][]string{
	"find": {"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprintf", "-fls"},
	"fd":   {"-x", "--exec", "-X", "--exec-batch"},
	"rg":   {"--pre", "--hostname-bin"},
	"tree": {"-o"},
}

// IsReadOnlyBashCommand reports whether a bash invocation is provably
// read-only: every command in the chain is on the read-only list (or is a
// read-only git command), with no output redirection (except /dev/null) and
// no command or process substitution. Conservative by design — a parse
// failure or any unrecognized construct means false, never a guess.
func IsReadOnlyBashCommand(cmd string) bool {
	// Substitution can hide arbitrary execution inside arguments, and the AST
	// extraction does not descend into $(), backticks, <() or >().
	if strings.Contains(cmd, "$(") || strings.Contains(cmd, "`") ||
		strings.Contains(cmd, "<(") || strings.Contains(cmd, ">(") {
		return false
	}
	if isDestructiveCommand(cmd) || checkBashSecurity(cmd) != "" {
		return false
	}
	file := parseBashAST(cmd)
	if file == nil {
		return false
	}
	commands := extractCommandsAST(file)
	if len(commands) == 0 {
		return false
	}
	for _, c := range commands {
		if !isReadOnlyParsedCommand(c) {
			return false
		}
	}
	return true
}

func isReadOnlyParsedCommand(c parsedCommand) bool {
	// Env-var prefixes can redirect a binary's behavior to arbitrary code
	// (GIT_EXTERNAL_DIFF=evil git diff), so any assignment disqualifies.
	if c.HasAssign {
		return false
	}
	for _, path := range c.RedirPaths {
		if path != "/dev/null" {
			return false
		}
	}
	if c.Name == "git" {
		return isReadOnlyGitCommand(c)
	}
	if !readOnlyBashCommands[c.Name] {
		return false
	}
	for _, flag := range readOnlyBashUnsafeFlags[c.Name] {
		for _, arg := range c.Args {
			if strings.HasPrefix(arg, flag) {
				return false
			}
		}
	}
	return true
}

// CommonAllowPatterns contains commonly allowed patterns.
var CommonAllowPatterns = []string{
	"Bash(git:*)",
	"Bash(npm:*)",
	"Bash(yarn:*)",
	"Bash(pnpm:*)",
	"Bash(go:*)",
	"Bash(make:*)",
	"Bash(ls:*)",
	"Bash(cat:*)",
	"Bash(head:*)",
	"Bash(tail:*)",
	"Bash(pwd)",
}

// ---------------------------------------------------------------------------
// Bypass-immune path safety checks
// Inspired by Claude Code's checkPathSafetyForAutoEdit — these checks cannot
// be bypassed by session permissions or allow rules.
// ---------------------------------------------------------------------------

// sensitiveDirectories are directory names that should always require
// confirmation when editing files within them. They contain configuration or
// metadata that, if tampered with, can execute code or break tooling.
var sensitiveDirectories = []string{
	".git",    // Git hooks can execute arbitrary code
	".claude", // Claude Code configuration
	".san",    // San configuration
	".vscode", // VS Code extensions, launch configs
	".idea",   // JetBrains IDE configs
	".ssh",    // SSH keys and config
	".aws",    // AWS credentials
	".gnupg",  // GPG keys
	".kube",   // Kubernetes configs
}

// sensitiveFiles are specific filenames (basenames) that should always require
// confirmation because they can execute code on shell startup or contain
// credentials.
var sensitiveFiles = map[string]string{
	".bashrc":             "shell startup script",
	".bash_profile":       "shell startup script",
	".zshrc":              "shell startup script",
	".zprofile":           "shell startup script",
	".profile":            "shell startup script",
	".zshenv":             "shell startup script",
	".login":              "shell startup script",
	".gitconfig":          "git configuration (hooks, aliases)",
	".gitmodules":         "git submodule config",
	".npmrc":              "npm config (may contain auth tokens)",
	".pypirc":             "PyPI config (may contain auth tokens)",
	".netrc":              "network credentials",
	".docker/config.json": "Docker credentials",
}

// isSensitivePath checks if a file path points to a sensitive location that
// requires user confirmation (unrecoverable tier; bypass mode skips it).
// Returns a human-readable reason if sensitive, or empty string if safe.
func isSensitivePath(filePath string) string {
	// Resolve symlinks to prevent bypass via symlink chains
	resolved, err := filepath.EvalSymlinks(filepath.Dir(filePath))
	if err == nil {
		filePath = filepath.Join(resolved, filepath.Base(filePath))
	}

	// Normalize to absolute path
	if !filepath.IsAbs(filePath) {
		if abs, err := filepath.Abs(filePath); err == nil {
			filePath = abs
		}
	}

	// Check each path component for sensitive directories
	parts := strings.Split(filePath, string(os.PathSeparator))
	for _, part := range parts {
		for _, dir := range sensitiveDirectories {
			if part == dir {
				return dir + "/ directory"
			}
		}
	}

	// Check basename against sensitive files
	basename := filepath.Base(filePath)
	if reason, ok := sensitiveFiles[basename]; ok {
		return basename + " (" + reason + ")"
	}

	// Check two-level paths like ".docker/config.json"
	if len(parts) >= 2 {
		twoLevel := parts[len(parts)-2] + "/" + basename
		if reason, ok := sensitiveFiles[twoLevel]; ok {
			return twoLevel + " (" + reason + ")"
		}
	}

	return ""
}

// ---------------------------------------------------------------------------
// Enhanced bash security checks
// Inspired by Claude Code's bashSecurity.ts — detects obfuscation, injection,
// and other shell security issues beyond simple destructive patterns.
// ---------------------------------------------------------------------------

// zshDangerousCommands are Zsh-specific builtins that can bypass restrictions
// or access system resources directly.
var zshDangerousCommands = []string{
	"zmodload", // Load kernel modules
	"emulate",  // Change shell emulation mode
	"sysopen",  // Direct file descriptor access
	"sysread",  // Direct system read
	"syswrite", // Direct system write
	"sysseek",  // Direct seek
	"zpty",     // Pseudo-terminal control
	"ztcp",     // Raw TCP connections
	"zsocket",  // Unix socket access
	"zf_rm",    // Bypass safe rm
	"zf_mv",    // Bypass safe mv
	"zf_ln",    // Bypass safe ln
	"zf_chmod", // Direct chmod
	"zf_chown", // Direct chown
}

// bashSecurityPatterns defines patterns that indicate potential shell injection
// or obfuscation attempts.
var bashSecurityPatterns = []struct {
	check  func(string) bool
	reason string
}{
	{hasCommandSubstitution, "command substitution detected"},
	{hasObfuscatedFlags, "obfuscated flags detected"},
	{hasControlCharacters, "control characters detected"},
	{hasIFSInjection, "IFS injection detected"},
	{hasZshDangerousCommand, "zsh dangerous command"},
	{hasProcEnvironAccess, "/proc/environ access"},
	{hasSuspiciousRedirection, "suspicious redirection"},
}

// checkBashSecurity performs security analysis on a bash command beyond simple
// destructive pattern matching. Returns a reason string if the command is
// suspicious, or empty string if it appears safe.
func checkBashSecurity(cmd string) string {
	// AST-based checks first (more accurate, structural analysis)
	if file := parseBashAST(cmd); file != nil {
		if reason := checkASTSecurity(file); reason != "" {
			return reason
		}
	}

	// Regex-based checks as fallback / catch-all
	for _, p := range bashSecurityPatterns {
		if p.check(cmd) {
			return p.reason
		}
	}
	return ""
}

func hasCommandSubstitution(cmd string) bool {
	// Detect $() and backtick substitution in dangerous contexts
	// Allow simple $(cmd) but flag nested/complex patterns
	depth := 0
	for i := 0; i < len(cmd)-1; i++ {
		if cmd[i] == '$' && cmd[i+1] == '(' {
			depth++
			if depth > 1 {
				return true // Nested command substitution
			}
		}
		if cmd[i] == ')' && depth > 0 {
			depth--
		}
	}
	// Backtick substitution inside variable assignments
	if strings.Contains(cmd, "eval ") && (strings.Contains(cmd, "$(") || strings.Contains(cmd, "`")) {
		return true
	}
	return false
}

func hasObfuscatedFlags(cmd string) bool {
	// Detect backslash-escaped whitespace between flag characters
	// e.g., "r\m -r\f" to bypass pattern matching
	for i := 0; i < len(cmd)-1; i++ {
		if cmd[i] == '\\' {
			next := cmd[i+1]
			// Backslash followed by a letter mid-word (obfuscation attempt)
			if next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z' {
				// Check if this is within a flag-like context (after -)
				before := strings.TrimRight(cmd[:i], " \t")
				if len(before) > 0 && before[len(before)-1] == '-' {
					return true
				}
			}
		}
	}
	return false
}

func hasControlCharacters(cmd string) bool {
	for _, r := range cmd {
		// ASCII control chars except common ones (tab, newline, carriage return)
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
		// Unicode zero-width characters used for obfuscation
		if r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF {
			return true
		}
	}
	return false
}

func hasIFSInjection(cmd string) bool {
	return strings.Contains(cmd, "IFS=") || strings.Contains(cmd, "IFS =")
}

func hasZshDangerousCommand(cmd string) bool {
	// Split on &&, ;, then split each result on | for pipe segments
	segments := extractBashCommands(cmd)
	var expanded []string
	for _, seg := range segments {
		for pipePart := range strings.SplitSeq(seg, "|") {
			if s := strings.TrimSpace(pipePart); s != "" {
				expanded = append(expanded, s)
			}
		}
	}

	for _, c := range expanded {
		parts := strings.Fields(c)
		if len(parts) == 0 {
			continue
		}
		if slices.Contains(zshDangerousCommands, filepath.Base(parts[0])) {
			return true
		}
	}
	return false
}

func hasProcEnvironAccess(cmd string) bool {
	return strings.Contains(cmd, "/proc/") && strings.Contains(cmd, "environ")
}

func hasSuspiciousRedirection(cmd string) bool {
	// Detect output redirection to sensitive system paths
	suspiciousPaths := []string{
		"> /etc/", ">> /etc/",
		"> /dev/sd", ">> /dev/sd",
		"> /dev/nvme", ">> /dev/nvme",
		"> ~/.ssh/", ">> ~/.ssh/",
		"> ~/.bashrc", ">> ~/.bashrc",
		"> ~/.zshrc", ">> ~/.zshrc",
		"> ~/.profile", ">> ~/.profile",
	}
	lower := strings.ToLower(cmd)
	for _, p := range suspiciousPaths {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Denial tracking — prevents infinite denial loops and surfaces potential
// classifier or rule misconfiguration.
// ---------------------------------------------------------------------------

// denialLimits configures when the system falls back to prompting the user
// instead of auto-denying.
var denialLimits = struct {
	MaxConsecutive int // Fall back to prompting after N consecutive denials
	MaxTotal       int // Fall back to prompting after N total denials in session
}{
	MaxConsecutive: 3,
	MaxTotal:       20,
}

// DenialTracking tracks permission denials during a session.
type DenialTracking struct {
	ConsecutiveDenials int
	TotalDenials       int
}

// RecordDenial records a denial and returns true if the system should fall
// back to prompting the user.
func (d *DenialTracking) RecordDenial() bool {
	d.ConsecutiveDenials++
	d.TotalDenials++
	return d.ShouldFallbackToPrompting()
}

// RecordSuccess resets the consecutive denial counter.
func (d *DenialTracking) RecordSuccess() {
	d.ConsecutiveDenials = 0
}

// ShouldFallbackToPrompting returns true if denial limits are exceeded.
func (d *DenialTracking) ShouldFallbackToPrompting() bool {
	return d.ConsecutiveDenials >= denialLimits.MaxConsecutive ||
		d.TotalDenials >= denialLimits.MaxTotal
}
