package services

import (
	"context"
	"testing"

	"drool/internal/monitor/source"
)

const show = `Id=nginx.service
Description=A high performance web server
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
ActiveEnterTimestampMonotonic=5000000
StateChangeTimestampMonotonic=5000000
NRestarts=2
Result=success
Transient=no

Id=backup.service
Description=Nightly backup
LoadState=loaded
ActiveState=failed
SubState=failed
UnitFileState=static
ActiveEnterTimestampMonotonic=0
StateChangeTimestampMonotonic=90000000
NRestarts=0
Result=exit-code
Transient=no

Id=systemd-journald.service
Description=Journal Service
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=static
ActiveEnterTimestampMonotonic=1000000
StateChangeTimestampMonotonic=1000000
NRestarts=0
Result=success
Transient=no

Id=run-r1234.service
Description=/usr/bin/true
LoadState=loaded
ActiveState=inactive
SubState=dead
UnitFileState=transient
NRestarts=0
Result=success
Transient=yes

Id=getty@tty1.service
Description=Getty on tty1
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
ActiveEnterTimestampMonotonic=2000000
NRestarts=0
Transient=no

Id=old.service
Description=old
LoadState=masked
ActiveState=inactive
SubState=dead
UnitFileState=masked
NRestarts=
Transient=no
`

const files = `nginx.service enabled enabled
jellyfin.service disabled enabled
getty@.service enabled enabled
backup.service static -
`

func TestParseAndNotable(t *testing.T) {
	f := &source.Fake{Tools: map[string]bool{"systemctl": true}, Cmds: map[string]*source.Result{
		source.Key(Commands[0][0], Commands[0][1:]...): {Stdout: []byte(show)},
		source.Key(Commands[1][0], Commands[1][1:]...): {Stdout: []byte(files)},
	}}
	rep, fs, err := Collect(context.Background(), f, 1700000000, nil)
	if err != nil || !rep.Supported {
		t.Fatal(err, rep)
	}
	byName := map[string]Unit{}
	for _, u := range rep.Units {
		byName[u.Name] = u
	}
	n := byName["nginx.service"]
	if n.Since != 1700000005 || *n.Restarts != 2 || n.FileState != "enabled" || State(n) != "running" {
		t.Fatalf("%+v", n)
	}
	b := byName["backup.service"]
	if b.Since != 1700000090 || State(b) != "failed" || b.Result != "exit-code" {
		t.Fatalf("a failed unit's time is when it failed: %+v", b)
	}
	j, ok := byName["jellyfin.service"]
	if !ok || j.Load != "" || State(j) != "stopped" || j.FileState != "disabled" {
		t.Fatalf("installed but never started units are listed: %+v", j)
	}
	if _, ok := byName["getty@.service"]; ok {
		t.Fatal("templates are not units")
	}
	if byName["old.service"].Restarts != nil {
		t.Fatal("an empty NRestarts is unknown, not 0")
	}
	want := map[string]bool{"nginx.service": true, "backup.service": true, "getty@tty1.service": true,
		"systemd-journald.service": false, "run-r1234.service": false, "old.service": false, "jellyfin.service": false}
	for name, w := range want {
		if Notable(byName[name]) != w {
			t.Errorf("Notable(%s) = %v, want %v", name, !w, w)
		}
	}
	if len(fs) != 4 {
		t.Fatalf("unit files: %v", fs)
	}
}

func TestNoSystemd(t *testing.T) {
	rep, _, _ := Collect(context.Background(), &source.Fake{Tools: map[string]bool{}}, 0, nil)
	if rep.Supported || rep.Note == "" {
		t.Fatalf("%+v", rep)
	}
	f := &source.Fake{Tools: map[string]bool{"systemctl": true}, Cmds: map[string]*source.Result{
		source.Key(Commands[0][0], Commands[0][1:]...): {Exit: 1}}}
	rep, _, _ = Collect(context.Background(), f, 0, nil)
	if rep.Supported || rep.Note == "" {
		t.Fatalf("container without systemd: %+v", rep)
	}
}

func TestDiff(t *testing.T) {
	prev := []Unit{{Name: "a", Active: "active", Sub: "running"}, {Name: "b", Active: "active", Sub: "running"}, {Name: "gone", Active: "active", Sub: "running"}}
	cur := []Unit{{Name: "a", Active: "active", Sub: "running"}, {Name: "b", Active: "failed", Sub: "failed", Result: "signal"}, {Name: "new", Active: "inactive", Sub: "dead"}}
	ch := Diff(prev, cur)
	if len(ch) != 3 {
		t.Fatalf("%+v", ch)
	}
	if ch[0].Unit != "b" || ch[0].ToActive != "failed" || ch[0].Result != "signal" || ch[1].Unit != "new" || ch[1].FromActive != "" ||
		ch[2].Unit != "gone" || ch[2].ToActive != "" {
		t.Fatalf("%+v", ch)
	}
	if len(Diff(cur, cur)) != 0 {
		t.Fatal("identical inventories must not produce changes")
	}
}
