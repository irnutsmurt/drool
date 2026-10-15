//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// renderVolume is the data volume's name and the DROOL_DATA_VOLUME DROOL is
// told, as `docker compose config` renders compose.yaml for a project.
func renderVolume(t *testing.T, dir, project string, env ...string) (volume, told string) {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-p", project, "config", "--format", "json")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DROOL_") && !strings.HasPrefix(kv, "COMPOSE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
	var cfg struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
			Volumes     []struct {
				Source string `json:"source"`
				Target string `json:"target"`
			} `json:"volumes"`
		} `json:"services"`
		Volumes map[string]struct {
			Name string `json:"name"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.Services["drool"]
	expect(t, len(s.Volumes) == 1 && s.Volumes[0].Source == "drool-data" && s.Volumes[0].Target == "/data", "mounts: %+v", s.Volumes)
	return cfg.Volumes["drool-data"].Name, s.Environment["DROOL_DATA_VOLUME"]
}

// TestComposeDataVolume (final After-M17 batch): compose.yaml names DROOL's
// data volume itself, drool_drool-data or <DROOL_INSTANCE>_drool-data,
// whatever the folder or Synology project is called. Rendered for any
// project name; then, with this tree's image (and only names of the test's
// own, never the default a real installation here could use): an
// installation from before, whose volume was named after its folder, keeps
// its data through the documented migration in a moved folder; a DROOL
// pointed at a new volume by mistake starts empty and says so; two DROOLs
// with their own names run side by side; and a second DROOL given the same
// name by mistake refuses to use the first one's data while it runs.
func TestComposeDataVolume(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), src, 0o644)
	for _, project := range []string{"drool", "drool-moved", "nas-project"} {
		v, told := renderVolume(t, dir, project)
		expect(t, v == "drool_drool-data" && told == v, "project %s: volume %q, DROOL told %q", project, v, told)
	}
	v, told := renderVolume(t, dir, "drool", "DROOL_INSTANCE=lab")
	expect(t, v == "lab_drool-data" && told == v, "DROOL_INSTANCE=lab: volume %q, DROOL told %q", v, told)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("DROOL_INSTANCE=home\n"), 0o644)
	v, told = renderVolume(t, dir, "anything")
	expect(t, v == "home_drool-data" && told == v, ".env: volume %q, DROOL told %q", v, told)

	image, composeFile := imageUnderTest(t)
	volumeOf := func(p *composeProject) string {
		return strings.TrimSpace(mustDocker(t, "inspect", "-f", `{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}`, p.container()))
	}
	setupStatus := func(c *controller) (required bool, volume string) {
		var st struct {
			SetupRequired bool   `json:"setup_required"`
			DataVolume    string `json:"data_volume"`
		}
		c.must(t, "GET", "/api/v1/setup/status", nil, &st)
		return st.SetupRequired, st.DataVolume
	}

	// An installation from before compose.yaml named the volume, in a
	// folder that isn't called drool: Docker named its volume after the
	// project, <project>_drool-data.
	old := newComposeProject(t, image, composeFile, "vol-old")
	before := strings.Replace(strings.Replace(string(src), "    name: *data-volume\n", "", 1), "      DROOL_DATA_VOLUME: *data-volume\n", "", 1)
	expect(t, len(before) < len(src)-40, "compose.yaml has no volume name lines")
	os.WriteFile(filepath.Join(old.dir, "compose.yaml"), []byte(before), 0o644)
	old.env = nil
	old.must("up", "-d")
	old.waitHealthy()
	expect(t, volumeOf(old) == old.name+"_drool-data", "the old installation's volume: %s", volumeOf(old))
	c := old.controller(old.setupCode())
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}
	kept := createSecretOn(t, c, "from-before")
	old.must("stop")

	// The documented migration, in a moved folder: the new compose.yaml
	// with DROOL_INSTANCE set to the part before _drool-data.
	moved := newComposeProject(t, image, composeFile, "vol-moved")
	moved.env = []string{"DROOL_INSTANCE=" + old.name}
	moved.must("up", "-d")
	moved.waitHealthy()
	expect(t, volumeOf(moved) == old.name+"_drool-data", "after the migration: volume %s", volumeOf(moved))
	c2 := moved.controller("")
	if err := c2.login(); err != nil {
		t.Fatalf("the administrator from before: %v", err)
	}
	var secrets struct {
		Items []struct{ ID string } `json:"items"`
	}
	c2.must(t, "GET", "/api/v1/secrets", nil, &secrets)
	found := false
	for _, s := range secrets.Items {
		found = found || s.ID == kept
	}
	expect(t, found, "the credential from before isn't there after the migration")
	moved.must("stop")

	// The name forgotten or mistyped: a new, empty volume, and DROOL says so
	// in its log and on the setup page instead of quietly looking new.
	wrong := newComposeProject(t, image, composeFile, "vol-wrong")
	wrong.must("up", "-d")
	wrong.waitHealthy()
	want := wrong.name + "_drool-data"
	logs := wrong.must("logs", "--no-log-prefix", "drool")
	expect(t, strings.Contains(logs, "This DROOL has no data yet: it starts new in the volume "+want) && strings.Contains(logs, "Compose data volume"),
		"the log doesn't warn about the new volume:\n%s", tail(logs, 30))
	required, named := setupStatus(wrong.controller(""))
	expect(t, required && named == want, "setup status: required %v, volume %q", required, named)
	wrong.must("down", "-v")

	// Two DROOLs on one machine, each with its own name.
	a := newComposeProject(t, image, composeFile, "vol-a")
	b := newComposeProject(t, image, composeFile, "vol-b")
	a.must("up", "-d")
	b.must("up", "-d")
	a.waitHealthy()
	b.waitHealthy()
	expect(t, volumeOf(a) == a.name+"_drool-data" && volumeOf(b) == b.name+"_drool-data", "volumes %s and %s", volumeOf(a), volumeOf(b))

	// The same name given to a third by mistake: it won't run on the first
	// one's data while that one runs.
	same := newComposeProject(t, image, composeFile, "vol-same")
	same.env = []string{"DROOL_INSTANCE=" + a.name}
	same.run("up", "-d")
	waitFor(t, 2*time.Minute, "the second DROOL refusing the shared volume", func() bool {
		out, _ := same.run("logs", "--no-log-prefix", "drool")
		return strings.Contains(out, "in use by a running DROOL controller")
	})
	same.run("down")
	if a.controller("").login() == nil {
		t.Fatal("logged in to an installation nobody set up")
	}
	required, _ = setupStatus(a.controller(""))
	expect(t, required, "the first DROOL changed under the second")
}
