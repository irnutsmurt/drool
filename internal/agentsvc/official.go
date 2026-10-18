package agentsvc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"drool/internal/agentrpc"
	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/release"
	"drool/internal/store/db"
	"drool/internal/version"
)

// Official Shepherd releases (ARCHITECTURE-ALPHA §42). Shepherds follow
// DROOL: the official update for a fleet is the Shepherd that ships with the
// controller, when the controller is itself an official release. Its signed
// descriptions (drool-agent_<v>_linux_<arch>.release.json, signed offline
// with the DROOL project's Shepherd release key) are published with the
// release; the controller fetches them from the release source, verifies
// them against the project's keys built into it and against its own
// Shepherd binaries, and stores the result as an ordinary release. Nothing
// executable is ever downloaded: the binary is the one the operator
// installed with DROOL, so the source decides only whether an update is
// available, never what runs. Each Shepherd verifies the signature again
// with the keys built into it before it installs anything.
//
// The same check looks at the source's newest release: a newer DROOL is
// reported (verified the same way), for the operator to install. Shepherds
// never move past the controller: update DROOL first, and its Shepherds
// follow.

// DefaultUpdateSource is where DROOL's releases are published.
const DefaultUpdateSource = "https://github.com/irnutsmurt/drool/releases"

// Official release states (OfficialStatus.State).
const (
	OfficialPending      = "pending"       // not checked yet
	OfficialVerified     = "verified"      // this DROOL's Shepherd release is verified and stored
	OfficialNotPublished = "not_published" // the source has no signed description of it (yet)
	OfficialRejected     = "rejected"      // a description was found and failed verification
	OfficialUnreachable  = "unreachable"   // the source couldn't be reached
	OfficialDevelopment  = "development"   // this DROOL isn't an official release
	OfficialNoKey        = "no_key"        // this build has no DROOL release key to verify with
	OfficialOff          = "off"           // checks are turned off (agent.update_source: off)
	OfficialNoBinaries   = "no_binaries"   // this controller has no Shepherd programs to hand out
)

// maxManifest bounds a downloaded release description.
const maxManifest = 64 << 10

// ErrChecking is returned when a release check is already running.
var ErrChecking = errors.New("already checking for releases")

// ReleaseCheckOptions configure the official release check.
type ReleaseCheckOptions struct {
	// Source is agent.update_source: "" for DefaultUpdateSource, "off".
	Source string
	Client *http.Client
	// StartDelay before the first check; Every between checks; Retry while
	// this DROOL's own Shepherd release couldn't be verified yet.
	StartDelay, Every, Retry time.Duration
}

type releaseChecker struct {
	opt  ReleaseCheckOptions
	kick chan struct{}

	checkMu sync.Mutex // held for a whole check

	mu       sync.Mutex
	checking bool
	next     time.Time
}

// releaseCheckState is the last check's result (setting agent_release_check).
type releaseCheckState struct {
	LastAttempt time.Time `json:"last_attempt,omitzero"`
	LastSuccess time.Time `json:"last_success,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
	// This controller's official Shepherd release.
	OwnVersion string `json:"own_version,omitempty"`
	OwnState   string `json:"own_state,omitempty"`
	OwnDetail  string `json:"own_detail,omitempty"`
	// The newest official release the source names, when it is newer than
	// this controller and its description verified.
	LatestVersion  string    `json:"latest_version,omitempty"`
	LatestIssuedAt time.Time `json:"latest_issued_at,omitzero"`
	LatestURL      string    `json:"latest_url,omitempty"`
}

const releaseCheckKey = "agent_release_check"

// ConfigureReleaseCheck sets up the official release check; RunReleaseChecks
// runs it.
func (s *Service) ConfigureReleaseCheck(o ReleaseCheckOptions) {
	if o.Source == "" {
		o.Source = DefaultUpdateSource
	}
	o.Source = strings.TrimSuffix(o.Source, "/")
	if o.Client == nil {
		o.Client = releaseClient()
	}
	if o.StartDelay == 0 {
		o.StartDelay = 30 * time.Second
	}
	if o.Every == 0 {
		o.Every = 6 * time.Hour
	}
	if o.Retry == 0 {
		o.Retry = time.Hour
	}
	s.mu.Lock()
	s.rc = &releaseChecker{opt: o, kick: make(chan struct{}, 1)}
	s.mu.Unlock()
}

func (s *Service) checker() *releaseChecker {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rc == nil {
		s.rc = &releaseChecker{opt: ReleaseCheckOptions{Source: DefaultUpdateSource, Client: releaseClient(), StartDelay: 30 * time.Second,
			Every: 6 * time.Hour, Retry: time.Hour}, kick: make(chan struct{}, 1)}
	}
	return s.rc
}

func releaseClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("redirected away from HTTPS")
			}
			return nil
		}}
}

// RunReleaseChecks checks shortly after it starts, then every few hours
// (hourly while this DROOL's own Shepherd release couldn't be verified),
// and whenever TriggerReleaseCheck asks, until ctx ends.
func (s *Service) RunReleaseChecks(ctx context.Context) {
	c := s.checker()
	wait := c.opt.StartDelay
	for {
		c.mu.Lock()
		c.next = s.now().Add(wait)
		c.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-c.kick:
			t.Stop()
		}
		if err := s.CheckReleases(ctx); err != nil && !errors.Is(err, ErrChecking) && ctx.Err() == nil {
			s.log.Warn("checking for official DROOL releases failed; Shepherds keep their version", "source", c.opt.Source, "err", err)
		}
		wait = c.opt.Every
		if st := s.releaseCheck(ctx); st.OwnState == OfficialNotPublished || st.OwnState == OfficialUnreachable {
			wait = c.opt.Retry
		}
	}
}

// TriggerReleaseCheck asks RunReleaseChecks for a check now. It never blocks.
func (s *Service) TriggerReleaseCheck() {
	c := s.checker()
	c.mu.Lock()
	c.checking = true // until the check says otherwise: the page shows it at once
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (s *Service) releaseCheck(ctx context.Context) releaseCheckState {
	var st releaseCheckState
	if row, err := s.st.R.GetSetting(ctx, releaseCheckKey); err == nil {
		json.Unmarshal([]byte(row.ValueJson), &st)
	}
	return st
}

func (s *Service) saveReleaseCheck(ctx context.Context, st releaseCheckState) {
	b, _ := json.Marshal(st)
	s.st.W.UpsertSetting(ctx, db.UpsertSettingParams{Key: releaseCheckKey, ValueJson: string(b), UpdatedAt: s.now().UnixMilli()})
}

// CheckReleases looks for this DROOL's official Shepherd release and the
// newest DROOL release now. It returns ErrChecking if a check is running,
// and what went wrong otherwise; nothing it fails to verify is stored.
func (s *Service) CheckReleases(ctx context.Context) error {
	c := s.checker()
	if !c.checkMu.TryLock() {
		return ErrChecking
	}
	defer c.checkMu.Unlock()
	c.mu.Lock()
	c.checking = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.checking = false
		c.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	st := s.releaseCheck(ctx)
	prevOwn := st.OwnState + "|" + st.OwnDetail
	b := version.Get()
	st.OwnVersion = b.Version
	switch {
	case c.opt.Source == "off":
		st.OwnState, st.OwnDetail = OfficialOff, "Checking for official releases is turned off (agent.update_source: off)."
		st.LatestVersion = ""
		s.saveReleaseCheck(ctx, st)
		return nil
	case len(release.OfficialKeys()) == 0:
		st.OwnState, st.OwnDetail = OfficialNoKey, "This DROOL build has no DROOL release key built in, so it can't verify official Shepherd releases."
		st.LatestVersion = ""
		s.saveReleaseCheck(ctx, st)
		return nil
	}
	st.LastAttempt = s.now()
	var errs []error
	own, err := s.checkOwnRelease(ctx, b)
	st.OwnState, st.OwnDetail = own.state, own.detail
	if err != nil {
		errs = append(errs, err)
	}
	if cur := st.OwnState + "|" + st.OwnDetail; cur != prevOwn && (st.OwnState == OfficialRejected || st.OwnState == OfficialVerified) {
		res, action := audit.Success, "agent.release.official"
		if st.OwnState == OfficialRejected {
			res, action = audit.Failure, "agent.release.official_rejected"
		}
		s.audit.Record(ctx, audit.Event{Action: action, TargetType: "agent_release", TargetID: b.Version, Actor: &audit.System, Result: res,
			Details: map[string]any{"version": b.Version, "detail": st.OwnDetail, "source": c.opt.Source}})
	}
	latest, issued, latestURL, err := s.checkLatest(ctx, b)
	if err != nil {
		errs = append(errs, err)
	} else {
		st.LatestVersion, st.LatestIssuedAt, st.LatestURL = latest, issued, latestURL
	}
	if len(errs) > 0 {
		st.LastError, st.LastErrorAt = errors.Join(errs...).Error(), s.now()
	} else {
		st.LastError, st.LastErrorAt = "", time.Time{}
		st.LastSuccess = s.now()
	}
	s.saveReleaseCheck(ctx, st)
	if st.OwnState == OfficialVerified {
		go s.advanceAfterCheck()
	}
	return errors.Join(errs...)
}

// advanceAfterCheck lets the automatic policy act on a newly verified
// release without waiting for its next tick.
func (s *Service) advanceAfterCheck() { s.RunUpdatePass(context.Background()) }

type ownResult struct{ state, detail string }

// checkOwnRelease makes sure the official release of this controller's own
// Shepherd binaries is stored: fetched, verified and recorded once per
// platform. The error is a failure to reach the source.
func (s *Service) checkOwnRelease(ctx context.Context, b version.Build) (ownResult, error) {
	if b.Channel != version.Stable && b.Channel != version.Prerelease {
		return ownResult{OfficialDevelopment, fmt.Sprintf("This DROOL (%s) is a development build, not an official release: there are no official Shepherd releases for it.", b.Version)}, nil
	}
	var have, missing []string
	var notPublished []string
	for _, arch := range []string{"amd64", "arm64"} {
		bin, err := s.AgentBinary(arch)
		if err != nil {
			continue // not shipped with this controller
		}
		if rel, err := s.st.R.GetReleaseFor(ctx, db.GetReleaseForParams{Version: b.Version, Os: "linux", Arch: arch}); err == nil {
			if release.IsOfficialKey(rel.KeyID) && rel.Sha256 == bin.SHA256 {
				have = append(have, arch)
				continue
			}
			// Stored, but not the official description of this binary (a
			// custom release of the same version): keep it, say so.
			missing = append(missing, fmt.Sprintf("linux/%s is a custom release of the same version (key %s)", arch, rel.KeyID))
			continue
		}
		raw, code, err := s.fetchSource(ctx, ownManifestPath(b.Version, arch))
		if err != nil {
			return ownResult{OfficialUnreachable, fmt.Sprintf("The release source couldn't be reached: %v", err)}, err
		}
		if code == http.StatusNotFound {
			notPublished = append(notPublished, "linux/"+arch)
			continue
		}
		if code != http.StatusOK {
			err := fmt.Errorf("the release source answered %d for %s", code, ownManifestPath(b.Version, arch))
			return ownResult{OfficialUnreachable, "The release source couldn't be read: " + err.Error()}, err
		}
		signed, err := ParseSignedRelease(raw)
		if err == nil {
			err = s.storeOfficial(ctx, signed, b, arch, bin)
		}
		if err != nil {
			s.log.Warn("an official Shepherd release failed verification; not stored", "version", b.Version, "arch", arch, "err", err)
			return ownResult{OfficialRejected, fmt.Sprintf("The signed description of the %s Shepherd for linux/%s failed verification and was not used: %v", b.Version, arch, err)}, nil
		}
		have = append(have, arch)
	}
	switch {
	case len(have) == 0 && len(notPublished) == 0 && len(missing) == 0:
		return ownResult{OfficialNoBinaries, "This controller has no Shepherd programs to hand out (agent.binaries_dir), so it has no official Shepherd release."}, nil
	case len(notPublished) > 0 && len(have) == 0:
		return ownResult{OfficialNotPublished, fmt.Sprintf("The signed Shepherd release of DROOL %s isn't published yet; DROOL checks again every hour.", b.Version)}, nil
	case len(missing) > 0 && len(have) == 0:
		return ownResult{OfficialRejected, strings.Join(missing, "; ")}, nil
	}
	detail := fmt.Sprintf("Shepherd %s for %s: signed by the DROOL project and verified.", b.Version, "linux/"+strings.Join(have, ", linux/"))
	if len(notPublished) > 0 {
		detail += " Not published yet for " + strings.Join(notPublished, ", ") + "."
	}
	return ownResult{OfficialVerified, detail}, nil
}

func ownManifestPath(v, arch string) string {
	return "download/" + url.PathEscape(v) + "/drool-agent_" + strings.TrimPrefix(v, "v") + "_linux_" + arch + ".release.json"
}

// storeOfficial verifies a signed description against the project's keys,
// this controller's version, channel and protocol range and its own
// Shepherd binary, and stores it as a release.
func (s *Service) storeOfficial(ctx context.Context, signed *release.Signed, b version.Build, arch string, bin *Binary) error {
	m, err := release.Verify(signed, release.OfficialKeys())
	if err != nil {
		return err
	}
	switch {
	case m.Version != b.Version:
		return fmt.Errorf("it describes version %s, not %s", m.Version, b.Version)
	case m.Channel != b.Channel:
		return fmt.Errorf("it is for the %s channel, this DROOL is %s", m.Channel, b.Channel)
	case m.OS != "linux" || m.Arch != arch:
		return fmt.Errorf("it is for %s/%s, not linux/%s", m.OS, m.Arch, arch)
	case agentrpc.Negotiate(s.ControllerRange, agentrpc.Range{Min: m.ProtocolMin, Max: m.ProtocolMax}) == 0:
		return fmt.Errorf("it speaks protocol %d–%d, this controller %d–%d", m.ProtocolMin, m.ProtocolMax, s.ControllerRange.Min, s.ControllerRange.Max)
	case m.SHA256 != bin.SHA256 || m.Size != bin.Size:
		return fmt.Errorf("it describes another binary (sha256 %.16s, %d bytes) than the one installed with this controller (%.16s, %d bytes)", m.SHA256, m.Size, bin.SHA256, bin.Size)
	}
	f, err := os.Open(bin.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	rel, err := s.storeRelease(ctx, signed, m, f, "")
	if err != nil {
		return err
	}
	s.log.Info("official Shepherd release verified", "version", m.Version, "platform", m.OS+"/"+m.Arch, "key", signed.KeyID, "release", rel.ID)
	return nil
}

// checkLatest asks the source for its newest (stable) release and returns
// it when it is newer than this controller and its description verifies.
func (s *Service) checkLatest(ctx context.Context, b version.Build) (string, time.Time, string, error) {
	raw, code, loc, err := s.fetchLatest(ctx)
	if err != nil {
		return "", time.Time{}, "", err
	}
	tag := ""
	switch {
	case code >= 300 && code < 400 && strings.Contains(loc, "/tag/"):
		tag, _ = url.PathUnescape(path.Base(loc))
	case code == http.StatusOK:
		tag = strings.TrimSpace(string(raw)) // a mirror's plain answer
	case code >= 300 && code < 400, code == http.StatusNotFound:
		return "", time.Time{}, "", nil // no release published yet
	default:
		return "", time.Time{}, "", fmt.Errorf("the release source answered %d for its newest release", code)
	}
	if !strings.HasPrefix(tag, "v") || release.CompareVersions(tag, "v0.0.0") <= 0 || !versionTag(tag) {
		return "", time.Time{}, "", nil
	}
	if b.Channel != version.Development && release.CompareVersions(tag, b.Version) <= 0 {
		return "", time.Time{}, "", nil
	}
	mraw, mcode, err := s.fetchSource(ctx, ownManifestPath(tag, "amd64"))
	if err != nil {
		return "", time.Time{}, "", err
	}
	if mcode != http.StatusOK {
		return "", time.Time{}, "", nil // not complete yet: say nothing
	}
	signed, err := ParseSignedRelease(mraw)
	if err != nil {
		return "", time.Time{}, "", fmt.Errorf("the newest release's description: %w", err)
	}
	m, err := release.Verify(signed, release.OfficialKeys())
	if err != nil || m.Version != tag {
		return "", time.Time{}, "", fmt.Errorf("the newest release (%s) didn't verify against the DROOL release key; it is ignored", tag)
	}
	if m.Channel != version.Stable {
		return "", time.Time{}, "", nil // a mirror's newest may be a candidate: the notice is for releases
	}
	if b.Channel == version.Development && release.CompareVersions(tag, b.Version) <= 0 {
		return "", time.Time{}, "", nil
	}
	return tag, m.IssuedAt, s.checker().opt.Source + "/tag/" + url.PathEscape(tag), nil
}

func versionTag(t string) bool { return release.ValidVersion(t) }

// fetchSource GETs a path under the source.
func (s *Service) fetchSource(ctx context.Context, p string) ([]byte, int, error) {
	c := s.checker()
	req, err := http.NewRequestWithContext(ctx, "GET", c.opt.Source+"/"+p, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "DROOL/"+version.Version)
	resp, err := c.opt.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > maxManifest {
		return nil, 0, fmt.Errorf("%s is larger than a release description", p)
	}
	return body, resp.StatusCode, nil
}

// fetchLatest asks for <source>/latest without following its redirect:
// GitHub answers with the newest release's tag page.
func (s *Service) fetchLatest(ctx context.Context) ([]byte, int, string, error) {
	c := s.checker()
	req, err := http.NewRequestWithContext(ctx, "GET", c.opt.Source+"/latest", nil)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("User-Agent", "DROOL/"+version.Version)
	cl := *c.opt.Client
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return body, resp.StatusCode, resp.Header.Get("Location"), nil
}

// storeRelease records a verified release: the binary into the artifact
// store, checked against the manifest, then the release row.
func (s *Service) storeRelease(ctx context.Context, signed *release.Signed, m *release.Manifest, bin io.Reader, userID string) (*db.AgentRelease, error) {
	if _, err := s.st.R.GetReleaseFor(ctx, db.GetReleaseForParams{Version: m.Version, Os: m.OS, Arch: m.Arch}); err == nil {
		return nil, &ReleaseUploadError{Msg: fmt.Sprintf("release %s for %s/%s already exists", m.Version, m.OS, m.Arch)}
	}
	a, err := s.arts.Put(ctx, bin, fmt.Sprintf("drool-agent-%s-%s-%s", m.Version, m.OS, m.Arch), userID)
	if err != nil {
		return nil, err
	}
	if a.SHA256 != m.SHA256 || a.Size != m.Size {
		return nil, &ReleaseUploadError{Msg: fmt.Sprintf("the binary (sha256 %.16s, %d bytes) is not the one the manifest signs (%.16s, %d bytes)", a.SHA256, a.Size, m.SHA256, m.Size)}
	}
	r := db.InsertReleaseParams{ID: ids.New(ids.AgentRelease), Version: m.Version, Channel: m.Channel, Os: m.OS, Arch: m.Arch,
		ProtocolMin: int64(m.ProtocolMin), ProtocolMax: int64(m.ProtocolMax), Sha256: m.SHA256, Size: m.Size, KeyID: signed.KeyID,
		ManifestPayload: signed.Payload, Signature: signed.Signature, UploadedBy: sql.NullString{String: userID, Valid: userID != ""},
		CreatedAt: s.now().UnixMilli()}
	if err := s.st.W.InsertRelease(ctx, r); err != nil {
		return nil, err
	}
	rel, err := s.st.R.GetRelease(ctx, r.ID)
	return &rel, err
}

func openBinary(b *Binary) (*os.File, error) { return os.Open(b.Path) }

// bundledBinaryFor finds this controller's own Shepherd binary with the
// given sha256: a signed description can then be uploaded without it.
func (s *Service) bundledBinaryFor(sha string) *Binary {
	for _, arch := range []string{"amd64", "arm64"} {
		if b, err := s.AgentBinary(arch); err == nil && b.SHA256 == sha {
			return b
		}
	}
	return nil
}

// manifestOf parses a stored release's (already verified) manifest.
func manifestOf(rel db.AgentRelease) (*release.Manifest, error) {
	var m release.Manifest
	if err := json.NewDecoder(bytes.NewReader(rel.ManifestPayload)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}
