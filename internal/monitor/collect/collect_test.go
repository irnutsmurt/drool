package collect

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/monitor"
	"drool/internal/monitor/source"
	"drool/internal/monitor/storage"
)

// machine builds a fixture machine: /proc, /sys and /dev/disk files and
// symlinks under root, and fake helpers in bin.
func machine(t *testing.T) (root, bin string) {
	t.Helper()
	root, bin = t.TempDir(), t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(p, target string) {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
	write("/proc/stat", "cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 100 0 50 800 50 0 0 0 0 0\nbtime 1700000000\n")
	write("/proc/meminfo", "MemTotal: 4000000 kB\nMemAvailable: 3000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	write("/proc/loadavg", "0.10 0.20 0.30 1/100 42\n")
	write("/proc/uptime", "100.00 50.00\n")
	write("/proc/sys/kernel/random/boot_id", "boot-1\n")
	write("/proc/net/dev", "Inter-|\n face |\n  eth0: 1000 1 0 0 0 0 0 0 2000 2 0 0 0 0 0 0\n")
	write("/proc/diskstats", "   8       0 sda 1 0 10 0 1 0 20 0 0 5 0 0 0 0 0\n")
	write("/proc/self/mountinfo", "22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n")
	// Devices tree, with /sys/block and /sys/class links into it (relative,
	// as the kernel makes them).
	dev := "/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda"
	write(dev+"/size", "1000000\n")
	write(dev+"/ro", "0\n")
	write(dev+"/removable", "0\n")
	write(dev+"/dev", "8:0\n")
	write(dev+"/queue/rotational", "0\n")
	write(dev+"/device/model", "Samsung SSD 870\n")
	write(dev+"/sda1/partition", "1\n")
	write(dev+"/sda1/dev", "8:1\n")
	write(dev+"/sda1/size", "999000\n")
	link("/sys/block/sda", "../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda")
	link("/dev/disk/by-id/wwn-0x5002538f00000001", "../../sda")
	link("/dev/disk/by-uuid/abcd-1234", "../../sda1")
	ndev := "/sys/devices/pci0000:00/0000:00:03.0/net/eth0"
	for k, v := range map[string]string{"operstate": "up", "carrier": "1", "speed": "1000", "ifindex": "2", "type": "1", "mtu": "1500"} {
		write(ndev+"/"+k, v+"\n")
	}
	link("/sys/class/net/eth0", "../../devices/pci0000:00/0000:00:03.0/net/eth0")
	link(ndev+"/device", "../../../0000:00:03.0")
	hw := "/sys/devices/platform/coretemp.0/hwmon/hwmon0"
	write(hw+"/name", "coretemp\n")
	write(hw+"/temp1_input", "45000\n")
	write(hw+"/temp1_label", "Package id 0\n")
	write(hw+"/temp1_crit", "100000\n")
	link("/sys/class/hwmon/hwmon0", "../../devices/platform/coretemp.0/hwmon/hwmon0")
	// Helpers.
	fx, _ := filepath.Abs("../storage/testdata/sata-healthy.json")
	helper := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	helper("smartctl", "cat "+fx)
	helper("systemctl", `case "$1" in
show) printf 'Id=nginx.service\nDescription=web\nLoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nActiveEnterTimestampMonotonic=5000000\nNRestarts=0\nResult=success\nTransient=no\n';;
list-unit-files) echo 'nginx.service enabled enabled';;
esac`)
	return root, bin
}

func TestAgentAndSSHPathsAgree(t *testing.T) {
	for _, tool := range []string{"grep", "find", "readlink", "stat", "timeout", "head"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	root, bin := machine(t)
	ctx := context.Background()
	// SSH path: the generated scripts, run by sh.
	run := func(_ context.Context, script string) ([]byte, error) {
		c := exec.Command("/bin/sh", "-c", script)
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		return c.Output()
	}
	ssh, err := poll(ctx, run, nil, root, 1000, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// Agent path: the same collectors on the local files.
	local := &source.Local{Root: root, Path: bin + ":" + os.Getenv("PATH")}
	agent := collectAll(ctx, local, 1000, nil, nil, true)

	if len(ssh.State.Filesystems) != 1 || ssh.State.Filesystems[0].Mount != "/" || ssh.State.Filesystems[0].TotalBytes == 0 {
		t.Fatalf("ssh filesystems (capacity from the second pass): %+v", ssh.State.Filesystems)
	}
	// Capacity of the fixture directory can change between the two
	// statfs calls; everything else must be identical.
	for _, r := range []*Result{ssh, agent} {
		for i := range r.State.Filesystems {
			f := &r.State.Filesystems[i]
			f.TotalBytes, f.UsedBytes, f.AvailBytes, f.UsedPercent, f.InodesPercent = 0, 0, 0, 0, nil
		}
		r.State.Source = ""
	}
	j := func(v any) string { b, _ := json.MarshalIndent(v, "", " "); return string(b) }
	for name, pair := range map[string][2]any{
		"state":    {ssh.State, agent.State},
		"raw":      {ssh.Raw, agent.Raw},
		"services": {ssh.Services, agent.Services},
		"storage":  {ssh.Storage, agent.Storage},
	} {
		if a, b := j(pair[0]), j(pair[1]); a != b {
			t.Errorf("%s differs between the SSH dump and local collection:\nssh:   %s\nlocal: %s", name, a, b)
		}
	}
	d := ssh.Storage.Devices
	if len(d) != 1 || d[0].Key != "wwn-0x5002538f00000001" || d[0].Health.State != storage.StateOK || d[0].Partitions[0].UUID != "abcd-1234" ||
		d[0].Partitions[0].Mounts[0] != "/" {
		t.Fatalf("storage over SSH: %+v", d)
	}
	if len(ssh.Services.Units) != 1 || ssh.Services.Units[0].Since != 1700000005 {
		t.Fatalf("services over SSH: %+v", ssh.Services)
	}
	if len(ssh.State.Sensors) != 1 || *ssh.State.Sensors[0].Critical != 100 {
		t.Fatalf("sensors over SSH: %+v", ssh.State.Sensors)
	}
	if in := ssh.Raw.NetInfo; len(in) != 1 || in[0].Kind != "physical" {
		t.Fatalf("interfaces over SSH: %+v", in)
	}
}

func TestScriptIsBoundedAndQuoted(t *testing.T) {
	s := Script()
	if strings.Contains(s, "$(cat") || strings.Count(s, "grep -sHaZ") != 1 {
		t.Fatal("first pass should be one grep for files")
	}
	if _, err := source.SecondPass([][]string{{"smartctl", "-a", "/dev/sda; rm -rf /"}}, nil, ""); err == nil {
		t.Fatal("unsafe argument accepted")
	}
	if _, err := source.SecondPass([][]string{{"rm", "-rf", "/"}}, nil, ""); err == nil {
		t.Fatal("unlisted tool accepted")
	}
	if _, err := source.SecondPass([][]string{{"smartctl", "-a", "/etc/shadow"}}, nil, ""); err == nil {
		t.Fatal("non-device path accepted")
	}
	sp, _ := source.SecondPass(nil, []string{"/mnt/it's here"}, "")
	if !strings.Contains(sp, `'/mnt/it'\''s here'`) {
		t.Fatalf("mount not quoted: %s", sp)
	}
}

func TestHelperStatus(t *testing.T) {
	f := &source.Fake{Files: map[string]string{"/sys/class/dmi/id/sys_vendor": "Raspberry Pi Foundation\n"}, Tools: map[string]bool{"smartctl": true}}
	hs := HelperStatus(f, []storage.Device{{Name: "nvme0n1", Helper: "nvme-cli"}}, nil)
	got := map[string]monitor.Helper{}
	for _, h := range hs {
		got[h.Package] = h
	}
	if !got["smartmontools"].Installed || got["smartmontools"].Useful || !got["nvme-cli"].Useful || !got["lm-sensors"].Useful {
		t.Fatalf("%+v", hs)
	}
	f.Files["/sys/class/dmi/id/sys_vendor"] = "QEMU\n"
	for _, h := range HelperStatus(f, nil, nil) {
		if h.Useful {
			t.Fatalf("nothing to suggest in a VM without devices needing it: %+v", h)
		}
	}
	if PackageName("lm-sensors", "dnf") != "lm_sensors" || PackageName("lm-sensors", "apt") != "lm-sensors" {
		t.Fatal("package names")
	}
}
