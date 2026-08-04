package input

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/core"
)

// The box has to cover every row the textarea actually draws, or the viewport
// silently clips the overflow. Predicting that by counting characters is what
// the textarea's own soft-wrap already does exactly — including word-wrap
// breaks and CJK, where one rune occupies two columns — so this asserts the
// thing that matters: nothing typed goes missing on screen.
func TestTextareaGrowsToFitWrappedContent(t *testing.T) {
	tests := []struct {
		name  string
		width int
		value string
	}{
		{"explicit newlines", 10, "first\nsecond"},
		{"wrapped ascii", 10, "12345678901"},
		{"word wrap near the edge", 12, "aaa aaa aaa aaa"},
		{"wrapped cjk", 74, strings.Repeat("中", 40) + "\n" + strings.Repeat("文", 40)},
		{"mixed width", 20, "hello 世界你好吗今天天气很好"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTextarea(tt.width)
			ta.SetValue(tt.value)

			// Soft wrapping inserts row breaks, so compare with all
			// whitespace squeezed out of both sides.
			shown := strings.Join(strings.Fields(xansi.Strip(ta.View())), "")
			want := strings.Join(strings.Fields(tt.value), "")
			if !strings.Contains(shown, want) {
				t.Fatalf("height %d clips content:\n shown %q\n want  %q", ta.Height(), shown, want)
			}
		})
	}
}

// A newline at the end opens a row the cursor sits on but no text occupies yet;
// the box still has to grow to show it.
func TestTextareaKeepsCursorRowVisible(t *testing.T) {
	ta := newTextarea(20)
	ta.SetValue("first\n")

	cursor := ta.Cursor()
	if cursor == nil {
		t.Fatal("focused composer reported no cursor")
	}
	if ta.Height() <= cursor.Position.Y {
		t.Fatalf("height %d hides cursor row %d", ta.Height(), cursor.Position.Y)
	}
}

// The box tops out at half the screen, but the buffer behind it must keep
// accepting input and scroll — MaxHeight alone would refuse keystrokes there.
func TestTextareaAcceptsInputPastVisibleHeight(t *testing.T) {
	m := New("", 40, nil, SelectorDeps{})
	m.SetTerminalHeight(24)

	value := strings.TrimSuffix(strings.Repeat("line\n", 40), "\n")
	m.Textarea.SetValue(value)

	if got := m.Textarea.Value(); got != value {
		t.Fatalf("buffer truncated: got %d lines, want 40", strings.Count(got, "\n")+1)
	}
	if h := m.Textarea.Height(); h > m.maxTextareaHeight() {
		t.Fatalf("visible height %d exceeds cap %d", h, m.maxTextareaHeight())
	}
}

// Newlines are the composer's own binding, not a case in the key router, so the
// keys users press have to actually reach it and split the line.
func TestNewlineKeysInsertNewline(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyPressMsg
		want string
	}{
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}, "first\n"},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, "first\n"},
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, "first\n"},
		// Enter belongs to the key router, which submits before the textarea sees it.
		{"enter", tea.KeyPressMsg{Code: tea.KeyEnter}, "first"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTextarea(40)
			ta.SetValue("first")
			ta.CursorEnd()

			ta, _ = ta.Update(tt.msg)

			if got := ta.Value(); got != tt.want {
				t.Fatalf("%s (%q) gave %q, want %q", tt.name, tt.msg.String(), got, tt.want)
			}
		})
	}
}

// The /autopilot and /evolve editors share newChromelessTextarea but appear in
// overlays, where View reports no cursor position — so they must keep painting
// their own.
func TestOverlayEditorsKeepVirtualCursor(t *testing.T) {
	if ta := newChromelessTextarea(); !ta.VirtualCursor() {
		t.Fatal("overlay editors lost their virtual cursor; they would render none")
	}
	if ta := newTextarea(40); ta.VirtualCursor() {
		t.Fatal("composer should drive the real terminal cursor")
	}
}

func Test_imageRefPattern(t *testing.T) {
	tests := []struct {
		input    string
		expected [][]string
	}{
		{
			input:    "describe @image.png",
			expected: [][]string{{"@image.png", "image.png", "png"}},
		},
		{
			input:    "@photo.jpg analyze this",
			expected: [][]string{{"@photo.jpg", "photo.jpg", "jpg"}},
		},
		{
			input:    "compare @a.png with @b.jpeg",
			expected: [][]string{{"@a.png", "a.png", "png"}, {"@b.jpeg", "b.jpeg", "jpeg"}},
		},
		{
			input:    "no images here",
			expected: nil,
		},
		{
			input:    "@path/to/image.webp",
			expected: [][]string{{"@path/to/image.webp", "path/to/image.webp", "webp"}},
		},
		{
			input:    "@animated.gif",
			expected: [][]string{{"@animated.gif", "animated.gif", "gif"}},
		},
		{
			input:    "@document.md is not an image",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			matches := imageRefPattern.FindAllStringSubmatch(tt.input, -1)
			if len(matches) != len(tt.expected) {
				t.Errorf("FindAllStringSubmatch(%q) got %d matches, want %d", tt.input, len(matches), len(tt.expected))
				return
			}
			for i, match := range matches {
				for j, part := range match {
					if j < len(tt.expected[i]) && part != tt.expected[i][j] {
						t.Errorf("match[%d][%d] = %q, want %q", i, j, part, tt.expected[i][j])
					}
				}
			}
		})
	}
}

func Test_bareImagePathRe(t *testing.T) {
	tests := []struct {
		input    string
		expected [][]string // nil means no match
	}{
		{
			input:    "check /home/user/image.png",
			expected: [][]string{{"/home/user/image.png", "/home/user/image.png", "png"}},
		},
		{
			input:    "/absolute/path/photo.jpg analyze this",
			expected: [][]string{{"/absolute/path/photo.jpg", "/absolute/path/photo.jpg", "jpg"}},
		},
		{
			input:    "compare relative/path/a.png with ../other/b.jpeg",
			expected: [][]string{{"relative/path/a.png", "relative/path/a.png", "png"}, {"../other/b.jpeg", "../other/b.jpeg", "jpeg"}},
		},
		{
			input:    "no images here",
			expected: nil,
		},
		{
			input:    "just a bare name.png with no path",
			expected: nil, // no path separator
		},
		{
			input:    "image.png at start",
			expected: nil, // no path separator
		},
		{
			input:    "/path/to/image.webp end",
			expected: [][]string{{"/path/to/image.webp", "/path/to/image.webp", "webp"}},
		},
		{
			input:    "./animated.gif nearby",
			expected: [][]string{{"./animated.gif", "./animated.gif", "gif"}},
		},
		{
			input:    "C:\\Users\\me\\photo.jpeg here",
			expected: [][]string{{"C:\\Users\\me\\photo.jpeg", "C:\\Users\\me\\photo.jpeg", "jpeg"}},
		},
		{
			input:    "@prefix.png has no path separator",
			expected: nil, // @ at start but no / in rest of path
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			matches := bareImagePathRe.FindAllStringSubmatch(tt.input, -1)
			if len(matches) != len(tt.expected) {
				t.Errorf("FindAllStringSubmatch(%q) got %d matches, want %d", tt.input, len(matches), len(tt.expected))
				return
			}
			for i, match := range matches {
				if len(match) < 3 {
					t.Errorf("match[%d] has %d groups, want 3", i, len(match))
					continue
				}
				if match[0] != tt.expected[i][0] {
					t.Errorf("match[%d][0] = %q, want %q", i, match[0], tt.expected[i][0])
				}
				if match[1] != tt.expected[i][1] {
					t.Errorf("match[%d][1] = %q, want %q", i, match[1], tt.expected[i][1])
				}
				if match[2] != tt.expected[i][2] {
					t.Errorf("match[%d][2] = %q, want %q", i, match[2], tt.expected[i][2])
				}
			}
		})
	}
}

func TestProcessImageRefs(t *testing.T) {
	// Create a real image file for testing (1x1 transparent PNG)
	tmpDir := t.TempDir()
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG header
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, // IDAT chunk
		0x54, 0x08, 0xD7, 0x63, 0x60, 0x60, 0x00, 0x00,
		0x00, 0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC, 0x33, // IEND chunk
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
		0xAE, 0x42, 0x60, 0x82,
	}
	pngPath := tmpDir + "/test.png"
	if err := os.WriteFile(pngPath, pngData, 0o644); err != nil {
		t.Fatal(err)
	}
	badPath := tmpDir + "/corrupt.png"
	if err := os.WriteFile(badPath, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		input     string
		wantText  string // expected output text
		wantImage int    // expected number of images loaded
		wantErr   bool
	}{
		{
			name:      "@-prefixed path loads image and strips text",
			input:     "describe @" + pngPath,
			wantText:  "describe",
			wantImage: 1,
		},
		{
			name:      "bare absolute path loads image and keeps text",
			input:     "check " + pngPath + " for details",
			wantText:  "check " + pngPath + " for details",
			wantImage: 1,
		},
		{
			name:      "leading bare absolute path loads image and strips text",
			input:     pngPath + " describe it",
			wantText:  "describe it",
			wantImage: 1,
		},
		{
			name:      "leading bare absolute path alone leaves empty text",
			input:     pngPath,
			wantText:  "",
			wantImage: 1,
		},
		{
			name:      "leading bare corrupt path is consumed with error",
			input:     badPath + " explain",
			wantText:  "explain",
			wantImage: 0,
			wantErr:   true,
		},
		{
			name:      "bare path with corrupt image skips silently",
			input:     "see " + badPath,
			wantText:  "see " + badPath,
			wantImage: 0,
			wantErr:   false,
		},
		{
			name:      "non-existent bare path is left as-is",
			input:     "missing /nonexistent/image.png file",
			wantText:  "missing /nonexistent/image.png file",
			wantImage: 0,
		},
		{
			// The error aborts the send, but the text comes back with it: nothing
			// was consumed before the failure, so a caller that has to send
			// something anyway sends what the user wrote rather than an empty turn.
			name:      "@ with corrupt image returns error and the untouched text",
			input:     "@" + badPath,
			wantText:  "@" + badPath,
			wantImage: 0,
			wantErr:   true,
		},
		{
			// What survived: the reference that did load is consumed, so the text
			// no longer names an image the caller is also attaching.
			name:      "@ failure after a good one returns the surviving text",
			input:     "@" + pngPath + " and @" + badPath,
			wantText:  "and @" + badPath,
			wantImage: 1,
			wantErr:   true,
		},
		{
			name:      "no image references",
			input:     "just text no images",
			wantText:  "just text no images",
			wantImage: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, images, err := ProcessImageRefs(tmpDir, tt.input)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantText {
				t.Errorf("ProcessImageRefs() text = %q, want %q", got, tt.wantText)
			}
			if len(images) != tt.wantImage {
				t.Errorf("ProcessImageRefs() got %d images, want %d", len(images), tt.wantImage)
			}
		})
	}
}

func TestLeadingImagePath(t *testing.T) {
	tmpDir := t.TempDir()
	pngPath := tmpDir + "/photo.png"
	if err := os.WriteFile(pngPath, []byte("anything"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := tmpDir + "/sub"
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	relPath := "sub/photo.png"
	if err := os.WriteFile(subDir+"/photo.png", []byte("anything"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		cwd   string
		input string
		want  string
	}{
		{
			name:  "absolute existing image path first",
			cwd:   tmpDir,
			input: pngPath + " describe it",
			want:  pngPath,
		},
		{
			name:  "absolute existing image path alone",
			cwd:   tmpDir,
			input: pngPath,
			want:  pngPath,
		},
		{
			name:  "relative existing image path first",
			cwd:   tmpDir,
			input: relPath + " explain",
			want:  relPath,
		},
		{
			name:  "leading whitespace is ignored",
			cwd:   tmpDir,
			input: "  " + pngPath + " hi",
			want:  pngPath,
		},
		{
			name:  "path mid-sentence is not leading",
			cwd:   tmpDir,
			input: "check " + pngPath + " for details",
			want:  "",
		},
		{
			name:  "non-existent absolute path",
			cwd:   tmpDir,
			input: "/nonexistent/photo.png explain",
			want:  "",
		},
		{
			name:  "trailing punctuation disqualifies the token",
			cwd:   tmpDir,
			input: pngPath + ", what is this",
			want:  "",
		},
		{
			name:  "bare filename is not a path",
			cwd:   tmpDir,
			input: "photo.png describe",
			want:  "",
		},
		{
			name:  "plain text",
			cwd:   tmpDir,
			input: "just some words",
			want:  "",
		},
		{
			name:  "empty input",
			cwd:   tmpDir,
			input: "   ",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LeadingImagePath(tt.cwd, tt.input); got != tt.want {
				t.Errorf("LeadingImagePath(%q, %q) = %q, want %q", tt.cwd, tt.input, got, tt.want)
			}
		})
	}
}

func TestPendingImageMatchesAndExtractInlineImages(t *testing.T) {
	m := New("", 80, nil, SelectorDeps{})
	first := m.AddPendingImage(core.Image{FileName: "a.png"})
	second := m.AddPendingImage(core.Image{FileName: "b.png"})

	m.Textarea.SetValue(second + " alpha " + first + " omega")

	matches := m.PendingImageMatches()
	if len(matches) != 2 {
		t.Fatalf("expected 2 inline image matches, got %d", len(matches))
	}
	if matches[0].ID != 2 || matches[1].ID != 1 {
		t.Fatalf("expected matches in text order, got %#v", matches)
	}

	content, images := m.ExtractInlineImages(m.Textarea.Value())
	if content != "alpha  omega" {
		t.Fatalf("unexpected content after extraction: %q", content)
	}
	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
	if images[0].FileName != "b.png" || images[1].FileName != "a.png" {
		t.Fatalf("unexpected image extraction order: %#v", images)
	}
}

func TestExtractInlineImagesUsesSubmittedBufferOffsets(t *testing.T) {
	m := New("", 80, nil, SelectorDeps{})
	label := m.AddPendingImage(core.Image{FileName: "a.png"})

	raw := "  " + label + " hi"
	m.Textarea.SetValue(raw)

	content, images := m.ExtractInlineImages("[" + raw[2:])
	if content != "[ hi" {
		t.Fatalf("unexpected content after extraction: %q", content)
	}
	if len(images) != 1 || images[0].FileName != "a.png" {
		t.Fatalf("unexpected extracted images: %#v", images)
	}
}

func TestRemoveImageToken(t *testing.T) {
	m := New("", 80, nil, SelectorDeps{})
	label := m.AddPendingImage(core.Image{FileName: "clip.png"})
	m.Textarea.SetValue("hello " + label + " world")

	match, ok := m.MatchAdjacentToCursor(len([]rune("hello "+label)), false)
	if !ok {
		t.Fatal("expected image token match at cursor")
	}

	m.RemoveImageToken(match, len([]rune("hello ")))

	if got := m.Textarea.Value(); got != "hello  world" {
		t.Fatalf("unexpected textarea value after token removal: %q", got)
	}
	if len(m.Images.Pending) != 0 {
		t.Fatalf("expected pending images to be cleared, got %d", len(m.Images.Pending))
	}
	if m.CursorIndex() != len([]rune("hello ")) {
		t.Fatalf("unexpected cursor position after removal: %d", m.CursorIndex())
	}
}
