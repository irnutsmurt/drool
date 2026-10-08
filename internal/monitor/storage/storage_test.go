package storage

import (
	"context"
	"os"
	"strings"
	"testing"

	"drool/internal/monitor/source"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// disk adds a block device to a fake sysfs.
func disk(f *source.Fake, name, real string, files map[string]string) {
	f.Links["/sys/block/"+name] = real
	base := "/sys/block/" + name + "/"
	f.Files[base+"size"] = "1953525168\n"
	f.Files[base+"ro"] = "0\n"
	f.Files[base+"removable"] = "0\n"
	f.Files[base+"dev"] = "8:0\n"
	for k, v := range files {
		f.Files[base+k] = v
	}
}

func newFake() *source.Fake {
	return &source.Fake{Files: map[string]string{}, Links: map[string]string{}, Tools: map[string]bool{}, Cmds: map[string]*source.Result{}}
}

func smartCmd(dev string, extra ...string) string {
	args := append([]string{"-j", "-a", "-n", "standby"}, extra...)
	return source.Key("smartctl", append(args, dev)...)
}

func collectOne(t *testing.T, f *source.Fake) Device {
	t.Helper()
	r := Collect(context.Background(), f, true, nil)
	if len(r.Devices) != 1 {
		t.Fatalf("devices: %+v", r.Devices)
	}
	return r.Devices[0]
}

func indicator(d Device, name string) *Indicator {
	for i := range d.Health.Indicators {
		if d.Health.Indicators[i].Name == name {
			return &d.Health.Indicators[i]
		}
	}
	return nil
}

func TestSATAHealthy(t *testing.T) {
	f := newFake()
	disk(f, "sda", "/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda",
		map[string]string{"queue/rotational": "0\n", "device/model": "Samsung SSD 870\n"})
	f.Links["/dev/disk/by-id/wwn-0x5002538f00000001"] = "/dev/sda"
	f.Links["/dev/disk/by-id/ata-Samsung_SSD_870_EVO_1TB_S6PTNX0R123456"] = "/dev/sda"
	f.Tools["smartctl"] = true
	f.Cmds[smartCmd("/dev/sda")] = &source.Result{Stdout: fixture(t, "sata-healthy.json")}
	d := collectOne(t, f)
	if d.Kind != "ssd" || d.Transport != "sata" || d.Key != "wwn-0x5002538f00000001" {
		t.Fatalf("classification: %s %s %s", d.Kind, d.Transport, d.Key)
	}
	if d.Capability != CapFull || d.Health.State != StateOK || !strings.HasPrefix(d.Health.Summary, "Healthy") {
		t.Fatalf("health: %s %+v", d.Capability, d.Health)
	}
	s := d.SMART
	if *s.Reallocated != 0 || *s.Pending != 0 || *s.OfflineUncorrect != 0 || *s.CRCErrors != 2 || *s.PowerOnHours != 8123 ||
		*s.PowerCycles != 512 || *s.Celsius != 34 {
		t.Fatalf("smart: %+v", s)
	}
	// Device statistics (standard) win over the vendor wear attribute.
	if *s.EnduranceUsed != 4 || s.EnduranceVendor {
		t.Fatalf("endurance: %d vendor=%v", *s.EnduranceUsed, s.EnduranceVendor)
	}
	if *s.BytesWritten != 40960000000*512 || *s.SelfTestPassed != true || d.Serial != "S6PTNX0R123456" || d.Firmware != "SVT02B6Q" {
		t.Fatalf("details: %+v", s)
	}
	if i := indicator(d, "Interface CRC errors"); i == nil || i.Status != "info" {
		t.Fatalf("CRC errors are information, not a failure: %+v", i)
	}
	if len(d.Raw) != 7 {
		t.Fatalf("raw table: %d", len(d.Raw))
	}
}

func TestSATAFailing(t *testing.T) {
	f := newFake()
	disk(f, "sdb", "/sys/devices/pci0000:00/ata2/host1/target1:0:0/1:0:0:0/block/sdb", map[string]string{"queue/rotational": "1\n"})
	f.Tools["smartctl"] = true
	f.Cmds[smartCmd("/dev/sdb")] = &source.Result{Stdout: fixture(t, "sata-failing.json"), Exit: 24}
	d := collectOne(t, f)
	if d.Kind != "hdd" || d.Health.State != StateFailing {
		t.Fatalf("%s %+v", d.Kind, d.Health)
	}
	if !strings.Contains(d.Health.Summary, "SMART overall result FAILED") {
		t.Fatalf("summary: %s", d.Health.Summary)
	}
	if *d.SMART.Reallocated != 2864 || *d.SMART.Pending != 17 || *d.SMART.OfflineUncorrect != 9 || *d.SMART.SelfTestPassed {
		t.Fatalf("%+v", d.SMART)
	}
}

func TestVendorSpecificAttributesAreNotNormalized(t *testing.T) {
	f := newFake()
	disk(f, "sdc", "/sys/devices/pci0000:00/ata3/host2/target2:0:0/2:0:0:0/block/sdc", nil)
	f.Tools["smartctl"] = true
	f.Cmds[smartCmd("/dev/sdc")] = &source.Result{Stdout: fixture(t, "sata-vendor.json"), Exit: 4}
	d := collectOne(t, f)
	// ID 5 under another name is not "reallocated sectors".
	if d.SMART.Reallocated != nil || d.Capability != CapVendor || d.Health.State != StateOK {
		t.Fatalf("%+v %s %+v", d.SMART, d.Capability, d.Health)
	}
	if !strings.Contains(d.Health.Summary, "vendor-specific") {
		t.Fatalf("summary: %s", d.Health.Summary)
	}
}

func TestMissingHelperAndPermissions(t *testing.T) {
	f := newFake()
	disk(f, "sda", "/sys/devices/pci0000:00/ata1/host0/target0:0:0/0:0:0:0/block/sda", nil)
	d := collectOne(t, f)
	if d.Capability != CapUnavailable || d.Helper != "smartmontools" || d.Health.State != StateUnknown ||
		!strings.Contains(d.CapabilityNote, "smartmontools is not installed") {
		t.Fatalf("missing helper: %+v", d)
	}
	f.Tools["smartctl"] = true
	f.Cmds[smartCmd("/dev/sda")] = &source.Result{Stdout: fixture(t, "denied.json"), Exit: 2}
	d = collectOne(t, f)
	if d.Capability != CapUnavailable || d.Health.State != StateUnknown || !strings.Contains(d.CapabilityNote, "root") {
		t.Fatalf("denied: %+v", d)
	}
}

func TestStandbyKeepsLastReadingWithoutWaking(t *testing.T) {
	f := newFake()
	disk(f, "sdd", "/sys/devices/pci0000:00/ata4/host3/target3:0:0/3:0:0:0/block/sdd", map[string]string{"queue/rotational": "1\n"})
	f.Tools["smartctl"] = true
	f.Cmds[smartCmd("/dev/sdd")] = &source.Result{Stdout: fixture(t, "sata-failing.json")}
	first := Collect(context.Background(), f, true, nil)
	f.Cmds[smartCmd("/dev/sdd")] = &source.Result{Stdout: fixture(t, "standby.json"), Exit: 2}
	second := Collect(context.Background(), f, true, first.Devices)
	d := second.Devices[0]
	if !d.Standby || d.SMART == nil || d.Health.State != StateFailing {
		t.Fatalf("standby: %+v", d)
	}
	for _, c := range f.Called {
		if !strings.Contains(c, "-n standby") {
			t.Fatalf("a SMART query without -n standby could wake the disk: %s", c)
		}
	}
}

func TestUSB(t *testing.T) {
	usbPath := "/sys/devices/pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host6/target6:0:0/6:0:0:0/block/sde"
	t.Run("thumb drive: unknown bridge, SAT retry unsupported", func(t *testing.T) {
		f := newFake()
		disk(f, "sde", usbPath, map[string]string{"removable": "1\n"})
		f.Tools["smartctl"] = true
		f.Cmds[smartCmd("/dev/sde")] = &source.Result{Stdout: fixture(t, "usb-unknown.json"), Exit: 1}
		f.Cmds[smartCmd("/dev/sde", "-d", "sat")] = &source.Result{Stdout: fixture(t, "usb-thumb-sat.json"), Exit: 2}
		d := collectOne(t, f)
		if d.Kind != "usb-flash" || d.Capability != CapLimited || d.Health.State != StateNoIndicators ||
			d.Health.Summary != "No failure indicators detected" {
			t.Fatalf("%s %s %+v", d.Kind, d.Capability, d.Health)
		}
	})
	t.Run("SATA SSD behind a SAT bridge", func(t *testing.T) {
		f := newFake()
		disk(f, "sde", usbPath, nil)
		f.Tools["smartctl"] = true
		f.Cmds[smartCmd("/dev/sde")] = &source.Result{Stdout: fixture(t, "usb-unknown.json"), Exit: 1}
		f.Cmds[smartCmd("/dev/sde", "-d", "sat")] = &source.Result{Stdout: fixture(t, "sata-healthy.json")}
		d := collectOne(t, f)
		if d.Kind != "usb-sata" || d.Capability != CapFull || d.Health.State != StateOK || !strings.Contains(d.CapabilityNote, "USB bridge") {
			t.Fatalf("%s %s %s %+v", d.Kind, d.Capability, d.CapabilityNote, d.Health)
		}
	})
	t.Run("NVMe in a USB enclosure", func(t *testing.T) {
		f := newFake()
		disk(f, "sdf", strings.ReplaceAll(usbPath, "sde", "sdf"), nil)
		f.Tools["smartctl"] = true
		f.Cmds[smartCmd("/dev/sdf")] = &source.Result{Stdout: fixture(t, "usb-nvme.json")}
		d := collectOne(t, f)
		if d.Kind != "usb-nvme" || d.NVMe == nil || !d.NVMe.ViaUSB || d.Health.State != StateOK || *d.NVMe.BytesWritten != 3000000*512000 {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("without smartmontools the bridge is unknown", func(t *testing.T) {
		f := newFake()
		disk(f, "sde", usbPath, nil)
		d := collectOne(t, f)
		if d.Kind != "usb" || d.Capability != CapUnavailable || d.Helper != "smartmontools" || d.Health.State != StateUnknown {
			t.Fatalf("%+v", d)
		}
	})
}

func TestNVMe(t *testing.T) {
	path := "/sys/devices/pci0000:00/0000:00:1d.0/0000:3d:00.0/nvme/nvme0/nvme0n1"
	t.Run("critical warning, spare, endurance, media errors", func(t *testing.T) {
		f := newFake()
		disk(f, "nvme0n1", path, nil)
		f.Tools["smartctl"] = true
		f.Cmds[smartCmd("/dev/nvme0n1")] = &source.Result{Stdout: fixture(t, "nvme-warning.json"), Exit: 8}
		d := collectOne(t, f)
		n := d.NVMe
		if d.Kind != "nvme" || n == nil || d.Health.State != StateFailing {
			t.Fatalf("%+v", d)
		}
		if len(n.Warnings) != 2 || !strings.Contains(n.Warnings[0], "spare") || !strings.Contains(n.Warnings[1], "reliability") {
			t.Fatalf("warning bits: %v", n.Warnings)
		}
		if i := indicator(d, "Available spare"); i == nil || i.Status != "critical" {
			t.Fatalf("spare below threshold: %+v", i)
		}
		if i := indicator(d, "Endurance used"); i == nil || i.Status != "warning" || i.Value != "104%" {
			t.Fatalf("endurance: %+v", i)
		}
		if i := indicator(d, "Media and data integrity errors"); i == nil || i.Value != "12" || i.Status != "warning" {
			t.Fatalf("media errors: %+v", i)
		}
		if *n.UnsafeShutdowns != 6 || *n.ErrorLogEntries != 30 || *n.SelfTest != "Completed without error" {
			t.Fatalf("%+v", n)
		}
	})
	t.Run("nvme-cli fallback, both JSON styles", func(t *testing.T) {
		f := newFake()
		disk(f, "nvme0n1", path, nil)
		f.Tools["nvme"] = true
		f.Cmds[source.Key("nvme", "smart-log", "-o", "json", "/dev/nvme0n1")] = &source.Result{Stdout: fixture(t, "nvme-cli-v1.json")}
		d := collectOne(t, f)
		if d.Source != "nvme-cli" || d.Capability != CapFull || *d.NVMe.Celsius != 37 || *d.NVMe.PercentUsed != 3 || d.Health.State != StateOK {
			t.Fatalf("v1: %+v %+v", d, d.NVMe)
		}
		f.Cmds[source.Key("nvme", "smart-log", "-o", "json", "/dev/nvme0n1")] = &source.Result{Stdout: fixture(t, "nvme-cli-v2.json")}
		d = collectOne(t, f)
		n := d.NVMe
		if *n.Celsius != 45 || *n.AvailableSpare != 97 || *n.BytesRead != 1000*512000 || *n.MediaErrors != 2 ||
			*n.CriticalWarning != 4 || d.Health.State != StateFailing {
			t.Fatalf("v2: %+v %+v", n, d.Health)
		}
	})
	t.Run("nvme-cli from QEMU's emulated drive: spare 0 of threshold 0", func(t *testing.T) {
		f := newFake()
		disk(f, "nvme0n1", path, nil)
		f.Tools["nvme"] = true
		f.Cmds[source.Key("nvme", "smart-log", "-o", "json", "/dev/nvme0n1")] = &source.Result{Stdout: fixture(t, "nvme-cli-qemu.json")}
		d := collectOne(t, f)
		if d.Capability != CapFull || *d.NVMe.Celsius != 50 || *d.NVMe.AvailableSpare != 0 || d.Health.State != StateOK {
			t.Fatalf("%+v %+v %+v", d.Capability, d.NVMe, d.Health)
		}
	})
	t.Run("no tools", func(t *testing.T) {
		f := newFake()
		disk(f, "nvme0n1", path, nil)
		d := collectOne(t, f)
		if d.Helper != "nvme-cli" || !strings.Contains(d.CapabilityNote, "nvme-cli") || d.Health.State != StateUnknown {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("malformed nvme-cli output", func(t *testing.T) {
		if _, ok := ParseNVMeCLI([]byte(`{"temperature": "hot"}`)); ok {
			t.Fatal("garbage accepted")
		}
		if _, ok := ParseNVMeCLI([]byte(`not json`)); ok {
			t.Fatal("garbage accepted")
		}
	})
}

func TestSDAndEMMC(t *testing.T) {
	sd := "/sys/devices/platform/emmc2bus/fe340000.mmc/mmc_host/mmc0/mmc0:aaaa/block/mmcblk0"
	t.Run("SD card: limited, never 'healthy'", func(t *testing.T) {
		f := newFake()
		disk(f, "mmcblk0", sd, map[string]string{"device/type": "SD\n", "device/name": "SC64G\n"})
		f.Tools["smartctl"] = true
		d := collectOne(t, f)
		if d.Kind != "sd" || d.Capability != CapLimited || d.Health.State != StateNoIndicators || d.Model != "SC64G" {
			t.Fatalf("%+v", d)
		}
		for _, c := range f.Called {
			t.Fatalf("no helper should be asked about an SD card: %s", c)
		}
		for _, i := range d.Health.Indicators {
			if strings.Contains(strings.ToLower(i.Name), "wear") || strings.Contains(strings.ToLower(i.Name), "life") {
				t.Fatalf("invented wear data: %+v", i)
			}
		}
	})
	t.Run("SD card read-only", func(t *testing.T) {
		f := newFake()
		disk(f, "mmcblk0", sd, map[string]string{"device/type": "SD\n", "ro": "1\n"})
		d := collectOne(t, f)
		if d.Health.State != StateWarning || !d.ReadOnly {
			t.Fatalf("%+v", d.Health)
		}
	})
	t.Run("read-only virtual device (a cloud-init seed image)", func(t *testing.T) {
		f := newFake()
		disk(f, "vdb", "/sys/devices/pci0000:00/0000:00:08.0/virtio2/block/vdb", map[string]string{"ro": "1\n"})
		d := collectOne(t, f)
		if d.Kind != "virtual" || d.Health.State == StateWarning || indicator(d, "Block device") == nil {
			t.Fatalf("%+v %+v", d.Kind, d.Health)
		}
	})
	t.Run("eMMC with life time and pre-EOL", func(t *testing.T) {
		f := newFake()
		disk(f, "mmcblk1", strings.ReplaceAll(sd, "mmcblk0", "mmcblk1"), map[string]string{"device/type": "MMC\n",
			"device/life_time": "0x02 0x01\n", "device/pre_eol_info": "0x01\n"})
		d := collectOne(t, f)
		if d.Kind != "emmc" || d.Capability != CapPartial || d.EMMC.LifeA != "10–20% used" || d.EMMC.PreEOL != "normal" || d.Health.State != StateOK {
			t.Fatalf("%+v %+v", d, d.EMMC)
		}
		f.Files["/sys/block/mmcblk1/device/pre_eol_info"] = "0x03\n"
		f.Files["/sys/block/mmcblk1/device/life_time"] = "0x0b 0x0a\n"
		d = collectOne(t, f)
		if d.Health.State != StateFailing || d.EMMC.PreEOL != "urgent" || d.EMMC.LifeA != "beyond its estimated life" {
			t.Fatalf("%+v", d.Health)
		}
	})
	t.Run("eMMC without registers: limited", func(t *testing.T) {
		f := newFake()
		disk(f, "mmcblk1", strings.ReplaceAll(sd, "mmcblk0", "mmcblk1"), map[string]string{"device/type": "MMC\n"})
		d := collectOne(t, f)
		if d.Capability != CapLimited || d.EMMC != nil || d.Health.State != StateNoIndicators {
			t.Fatalf("%+v", d)
		}
	})
}

func TestVirtualDisk(t *testing.T) {
	f := newFake()
	disk(f, "vda", "/sys/devices/pci0000:00/0000:00:04.0/virtio1/block/vda", nil)
	f.Tools["smartctl"] = true
	d := collectOne(t, f)
	if d.Kind != "virtual" || d.Capability != CapUnavailable || d.Health.State != StateUnknown || len(f.Called) != 0 {
		t.Fatalf("%+v %v", d, f.Called)
	}
}

func TestPartitionsMountsAndStableKeys(t *testing.T) {
	f := newFake()
	disk(f, "sda", "/sys/devices/pci0000:00/ata1/host0/target0:0:0/0:0:0:0/block/sda", map[string]string{"device/serial": "XYZ\n", "device/model": "Disk A\n"})
	f.Files["/sys/block/sda/sda1/partition"] = "1\n"
	f.Files["/sys/block/sda/sda1/dev"] = "8:1\n"
	f.Files["/sys/block/sda/sda1/size"] = "1000\n"
	f.Files["/sys/block/sda/sda2/partition"] = "2\n"
	f.Files["/sys/block/sda/sda2/dev"] = "8:2\n"
	f.Files["/sys/block/sda/sda2/size"] = "2000\n"
	// sda2 → LVM dm-0 → /
	f.Links["/sys/block/dm-0"] = "/sys/devices/virtual/block/dm-0"
	f.Files["/sys/block/dm-0/dev"] = "253:0\n"
	f.Files["/sys/block/dm-0/dm/name"] = "vg-root\n"
	f.Links["/sys/block/dm-0/slaves/sda2"] = "/sys/devices/pci0000:00/ata1/host0/target0:0:0/0:0:0:0/block/sda/sda2"
	f.Links["/dev/disk/by-uuid/1111-2222"] = "/dev/sda1"
	f.Files["/proc/self/mountinfo"] = "22 1 253:0 / / rw,relatime shared:1 - ext4 /dev/mapper/vg-root rw\n" +
		"23 22 8:1 / /boot/efi rw,relatime shared:2 - vfat /dev/sda1 rw\n"
	d := collectOne(t, f)
	if d.Key != "serial:Disk_A_XYZ" || len(d.Partitions) != 2 {
		t.Fatalf("%+v", d)
	}
	p1, p2 := d.Partitions[0], d.Partitions[1]
	if p1.UUID != "1111-2222" || len(p1.Mounts) != 1 || p1.Mounts[0] != "/boot/efi" {
		t.Fatalf("p1 %+v", p1)
	}
	if p2.Via != "vg-root" || len(p2.Mounts) != 1 || p2.Mounts[0] != "/" {
		t.Fatalf("p2 through LVM: %+v", p2)
	}
}

func TestKernelEvidence(t *testing.T) {
	cases := []struct{ line, kind, dev string }{
		{"blk_update_request: I/O error, dev sdb, sector 123456 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0", KernIOError, "sdb"},
		{"I/O error, dev mmcblk0, sector 2048 op 0x1:(WRITE) flags 0x800 phys_seg 1 prio class 2", KernIOError, "mmcblk0"},
		{"Buffer I/O error on dev mmcblk0p2, logical block 0, lost async page write", KernIOError, "mmcblk0p2"},
		{"sd 2:0:0:0: [sdc] tag#0 Sense Key : Medium Error [current]", KernMedium, "sdc"},
		{"critical medium error, dev sdc, sector 99 op 0x0:(READ)", KernMedium, "sdc"},
		{"EXT4-fs error (device mmcblk0p2): ext4_find_entry:1536: inode #2: comm ls: reading directory lblock 0", KernFSError, "mmcblk0p2"},
		{"EXT4-fs (sda1): Remounting filesystem read-only", KernFSReadOnly, "sda1"},
		{"usb 2-1: reset SuperSpeed USB device number 3 using xhci_hcd", KernUSBReset, "2-1"},
		{"usb 1-1.3: reset high-speed USB device number 4 using dwc_otg", KernUSBReset, "1-1.3"},
		{"usb 1-1.3: USB disconnect, device number 4", KernUSBGone, "1-1.3"},
		{"nvme nvme0: I/O 123 QID 4 timeout, aborting", KernTimeout, "nvme0"},
		{"ata3.00: exception Emask 0x0 SAct 0x0 SErr 0x0 action 0x6 frozen", KernTimeout, "ata3.00"},
		{"mmc0: Timeout waiting for hardware cmd interrupt.", KernTimeout, "mmc0"},
		{"mmcblk0: error -110 sending stop command, original cmd response 0x900, card status 0xd00", KernMMC, "mmcblk0"},
		{"ata1.00: error: { ICRC ABRT }", KernCRC, "ata1.00"},
		// Recorded in a test VM after its NVMe controller was removed.
		{"Buffer I/O error on device nvme0n1, logical block 33793", KernIOError, "nvme0n1"},
		{"EXT4-fs warning (device nvme0n1): ext4_end_bio:343: I/O error 10 writing to inode 16 starting block 33793)", KernIOError, "nvme0n1"},
	}
	for _, c := range cases {
		k, d, ok := MatchKernel(c.line)
		if !ok || k != c.kind || d != c.dev {
			t.Errorf("%q: got %s %s %v, want %s %s", c.line, k, d, ok, c.kind, c.dev)
		}
	}
	for _, noise := range []string{"usb 1-1: new high-speed USB device number 2 using xhci_hcd", "EXT4-fs (sda1): mounted filesystem with ordered data mode",
		"IN=eth0 OUT= MAC=... SRC=1.2.3.4 DST=5.6.7.8 PROTO=TCP", "audit: type=1400 apparmor=\"DENIED\" operation=\"open\""} {
		if _, _, ok := MatchKernel(noise); ok {
			t.Errorf("noise matched: %q", noise)
		}
	}
	us, msg, ok := ParseKmsg("3,1234,98765432,-;I/O error, dev sdb, sector 1\n SUBSYSTEM=block")
	if !ok || us != 98765432 || msg != "I/O error, dev sdb, sector 1" {
		t.Fatalf("kmsg: %d %q", us, msg)
	}
	us, msg, ok = ParseJournalLine("[ 1234.567890] pi kernel: mmc0: Timeout waiting for hardware cmd interrupt.")
	if !ok || us != 1234567890 || !strings.HasPrefix(msg, "mmc0:") {
		t.Fatalf("journal: %d %q", us, msg)
	}
	devs := []Device{{Name: "sda"}, {Name: "mmcblk0"}, {Name: "nvme0n1"}, {Name: "sde"}}
	links := map[string]string{"/sys/block/mmcblk0": "/sys/devices/platform/x/mmc_host/mmc0/mmc0:aaaa/block/mmcblk0",
		"/sys/block/sde": "/sys/devices/pci0000:00/usb1/1-1/1-1.3/1-1.3:1.0/host6/target6:0:0/6:0:0:0/block/sde",
		"/sys/block/sda": "/sys/devices/pci0000:00/ata3/host2/target2:0:0/2:0:0:0/block/sda"}
	link := func(p string) string { return links[p] }
	for dev, want := range map[string]string{"mmcblk0p2": "mmcblk0", "mmc0": "mmcblk0", "nvme0": "nvme0n1", "1-1.3": "sde", "ata3": "sda", "sda1": "sda", "sdz": ""} {
		if got := DiskFor(dev, devs, link); got != want {
			t.Errorf("DiskFor(%s) = %q, want %q", dev, got, want)
		}
	}
}
