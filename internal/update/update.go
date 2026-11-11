package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repository is the public home of the server; every release there carries the bundle. A panel
// needs no token to read it.
const Repository = "divezh/netlane-server"

// Channel is the latest published release of Repository (drafts and pre-releases are never «latest»).
const Channel = "https://api.github.com/repos/" + Repository + "/releases/latest"

// Bundle is the release asset the updater installs.
const Bundle = "trusttunnel-dev.tar.gz"

// RequestFile is written by the panel and consumed by the root timer: the console runs
// unprivileged (NoNewPrivileges), so it can only ask for the published version, never
// install anything itself.
const RequestFile = "update-requested"

// TimerUnit is installed with the server. Without it a request would stay unread, so the
// console offers the button only when the timer exists.
const TimerUnit = "/etc/systemd/system/trusttunnel-update.timer"

// PathUnit starts the updater as soon as the request appears (0.5.19+). A server updated from an
// older updater gets it on the next run of the new updater.
const PathUnit = "/etc/systemd/system/trusttunnel-update.path"

// Manifest describes one published version of the server bundle.
type Manifest struct {
	Version  string `json:"version"`
	Released string `json:"released_at"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Notes    string `json:"notes"`
	NotesURL string `json:"notes_url"`
}

// ErrChannel means the release could not be read; the panel stays usable without updates.
var ErrChannel = errors.New("update channel is unavailable")

var (
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

const maxRelease = 1 << 20

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Published  string `json:"published_at"`
	Body       string `json:"body"`
	Page       string `json:"html_url"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

// Parse reads GitHub's answer about the latest release: tag netlane-X.Y.Z (or vX.Y.Z), the bundle
// asset from this repository's release downloads and the SHA-256 digest GitHub computed for it.
// A release that cannot pin the archive is rejected.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) > maxRelease {
		return m, fmt.Errorf("release answer is too large")
	}
	var r release
	if err := json.Unmarshal(data, &r); err != nil {
		return m, fmt.Errorf("release answer is not json: %w", err)
	}
	if r.Draft || r.Prerelease {
		return m, fmt.Errorf("the latest release is not published")
	}
	m.Version = strings.TrimPrefix(strings.TrimPrefix(r.Tag, "netlane-"), "v")
	if !versionPattern.MatchString(m.Version) {
		return m, fmt.Errorf("release tag %q is not a release version", r.Tag)
	}
	for _, a := range r.Assets {
		if a.Name != Bundle {
			continue
		}
		m.URL = a.URL
		if digest := strings.ToLower(a.Digest); strings.HasPrefix(digest, "sha256:") {
			m.SHA256 = strings.TrimPrefix(digest, "sha256:")
		}
	}
	u, err := url.Parse(m.URL)
	if m.URL == "" || err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(u.Path, "/"+Repository+"/releases/download/") {
		return m, fmt.Errorf("release has no %s from %s", Bundle, Repository)
	}
	if !sha256Pattern.MatchString(m.SHA256) {
		return m, fmt.Errorf("release bundle has no checksum")
	}
	m.Released = r.Published
	m.Notes = Notes(r.Body)
	if n, err := url.Parse(r.Page); err == nil && n.Scheme == "https" && n.Host == "github.com" && len(r.Page) <= 300 {
		m.NotesURL = r.Page
	}
	return m, nil
}

// Notes turns the release description (the CHANGELOG section) into one short line of its points.
func Notes(body string) string {
	var points []string
	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r", ""), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			point := strings.NewReplacer("`", "", "**", "").Replace(line[2:])
			points = append(points, strings.TrimSpace(point))
		}
	}
	notes := strings.TrimSpace(strings.Join(points, " "))
	if r := []rune(notes); len(r) > 600 {
		notes = strings.TrimSpace(string(r[:600])) + "…"
	}
	return notes
}

// Fetch asks GitHub for the latest release. Only api.github.com is asked, without redirects: the
// answer decides what gets installed as root.
func Fetch(ctx context.Context, client *http.Client, channel string) (Manifest, error) {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	u, err := url.Parse(channel)
	if err != nil || u.Scheme != "https" || u.Host != "api.github.com" {
		return Manifest{}, fmt.Errorf("%w: channel must be https on api.github.com", ErrChannel)
	}
	copied := *client
	copied.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("%w: the channel must not redirect", ErrChannel)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", channel, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrChannel, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := copied.Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrChannel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("%w: HTTP %d", ErrChannel, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRelease+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrChannel, err)
	}
	return Parse(data)
}

// Status is the updater's own report: which step it is on and how far it got. The console
// only reads it, so a server without the timer shows «ждём таймер» instead of silence.
type Status struct {
	Stage   string `json:"stage"`
	Percent *int   `json:"percent"`
	Message string `json:"message"`
	// Detail is the specifics of the current step: megabytes downloaded, the backup file.
	Detail  string `json:"detail,omitempty"`
	Version string `json:"version,omitempty"`
	At      int64  `json:"at"`
	// Steps is the history of this run, oldest first; FailedStage names the step that failed.
	Steps       []Step `json:"steps,omitempty"`
	FailedStage string `json:"failed_stage,omitempty"`
}

// Step is one stage of an update run as the updater reported it.
type Step struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	At      int64  `json:"at"`
}

// ClearStatus forgets the report of a previous run before a new request.
func ClearStatus(dir string) {
	_ = os.Remove(filepath.Join(dir, StatusFile))
}

// StatusFile holds the updater's report next to the request.
const StatusFile = "update-status.json"

// Clear removes the request and the report once the console has shown them.
func Clear(dir string) {
	for _, name := range []string{RequestFile, StatusFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return
		}
	}
}

// ReadStatus returns the updater's report, if it wrote one.
func ReadStatus(dir string) (Status, bool) {
	data, err := os.ReadFile(filepath.Join(dir, StatusFile))
	if err != nil {
		return Status{}, false
	}
	var s Status
	if json.Unmarshal(data, &s) != nil {
		return Status{}, false
	}
	// The console shows these lines as they are: keep a damaged report from flooding it.
	if len(s.Steps) > 40 {
		s.Steps = s.Steps[len(s.Steps)-40:]
	}
	return s, true
}

// Request leaves a marker in the panel data directory. The root timer picks it up and runs
// the updater; the version is recorded only for the journal.
func Request(dir, version string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := filepath.Join(dir, RequestFile)
	temporary := name + ".tmp"
	body := fmt.Sprintf("{\"version\":%q,\"requested_at\":%d}\n", version, time.Now().Unix())
	if err := os.WriteFile(temporary, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, name)
}

// Pending is what the console asked for, kept for the progress report.
type Pending struct {
	Version     string `json:"version"`
	RequestedAt int64  `json:"requested_at"`
}

// ReadRequest reports the pending request, if the console left one.
func ReadRequest(dir string) (Pending, bool) {
	data, err := os.ReadFile(filepath.Join(dir, RequestFile))
	if err != nil {
		return Pending{}, false
	}
	var r Pending
	if json.Unmarshal(data, &r) != nil {
		return Pending{}, false
	}
	return r, true
}

// Compare orders published versions. A build without a release number ("dev", "dev-abc1234")
// sorts before every release, so an unpublished panel always offers the published version.
func Compare(current, latest string) int {
	a, okA := parseVersion(current)
	b, okB := parseVersion(latest)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	if !versionPattern.MatchString(v) {
		return out, false
	}
	for i, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
