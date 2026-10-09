package monitor

import (
	"context"
	"math"
	"strings"
	"testing"

	"drool/internal/monitor/source"
)

func fake(files map[string]string, links map[string]string) *source.Fake {
	if links == nil {
		links = map[string]string{}
	}
	return &source.Fake{Files: files, Links: links, Tools: map[string]bool{}, Cmds: map[string]*source.Result{}}
}

const stat1 = `cpu  1000 0 500 8000 500 0 0 0 0 0
cpu0 500 0 250 4000 250 0 0 0 0 0
cpu1 500 0 250 4000 250 0 0 0 0 0
btime 1700000000
`

// 10 s later: 600 busy ticks of 1000 on all CPUs, cpu0 fully busy.
const stat2 = `cpu  1500 0 600 8300 600 0 0 0 0 0
cpu0 1000 0 250 4000 250 0 0 0 0 0
cpu1 500 0 350 4300 350 0 0 0 0 0
btime 1700000000
`

const meminfo = `MemTotal:        8000000 kB
MemFree:          500000 kB
MemAvailable:    6000000 kB
Buffers:          100000 kB
Cached:          4000000 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
`

func netdev(rx, tx, rxerr uint64) string {
	return "Inter-|   Receive |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n" +
		"  eth0: " + u(rx) + " 10 " + u(rxerr) + " 1 0 0 0 0 " + u(tx) + " 10 0 2 0 0 0 0\n"
}

func u(v uint64) string { return strings.TrimSpace(strings.Repeat(" ", 1) + itoa(v)) }

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}

func sys(stat, net, disk, boot string) *source.Fake {
	return fake(map[string]string{
		"/proc/stat": stat, "/proc/meminfo": meminfo, "/proc/loadavg": "0.50 0.40 0.30 1/200 999\n", "/proc/uptime": "3600.5 7000.1\n",
		"/proc/sys/kernel/random/boot_id": boot + "\n", "/proc/net/dev": net, "/proc/diskstats": disk,
		"/proc/pressure/memory":         "some avg10=1.50 avg60=0.75 avg300=0.10 total=123\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"/sys/class/net/eth0/operstate": "up\n", "/sys/class/net/eth0/carrier": "1\n", "/sys/class/net/eth0/speed": "1000\n",
		"/sys/class/net/eth0/ifindex": "2\n", "/sys/class/net/eth0/type": "1\n", "/sys/class/net/eth0/mtu": "1500\n",
		"/sys/class/net/lo/operstate": "unknown\n", "/sys/class/net/lo/type": "772\n", "/sys/class/net/lo/ifindex": "1\n",
		"/sys/block/sda/dev": "8:0\n", "/sys/block/loop0/dev": "7:0\n",
	}, map[string]string{"/sys/class/net/eth0/device": "/sys/devices/pci0000:00/0000:00:03.0", "/sys/block/sda": "/sys/devices/x/block/sda",
		"/sys/block/loop0": "/sys/devices/virtual/block/loop0"})
}

func diskstats(rs, ws, ticks uint64) string {
	return "   8       0 sda 100 0 " + itoa(rs) + " 50 200 0 " + itoa(ws) + " 80 0 " + itoa(ticks) + " 130 0 0 0 0\n" +
		"   8       1 sda1 100 0 999999 50 200 0 999999 80 0 999 130 0 0 0 0\n" +
		"   7       0 loop0 1 0 2 0 0 0 0 0 0 1 0 0 0 0 0\n"
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestRatesAndGauges(t *testing.T) {
	a := ReadRaw(sys(stat1, netdev(1000, 2000, 0), diskstats(1000, 2000, 100), "b1"), 0)
	b := ReadRaw(sys(stat2, netdev(1001000, 2002000, 5), diskstats(21000, 42000, 2100), "b1"), 10_000)
	s := Rates(a, b)
	near(t, "cpu", s.V[KeyCPU], 60)         // (1000 total delta − 300 idle − 100 iowait) / 1000
	near(t, "iowait", s.V[KeyIOWait], 10)   // 100/1000
	near(t, "core0", *s.Cores[0], 100)      // all busy
	near(t, "core1", *s.Cores[1], 20)       // 100 of 500
	near(t, "mem", s.V[KeyMem], 25)         // (8e6−6e6)/8e6: cache counts as available
	near(t, "swap", s.V[KeySwap], 25)       // 0.5e6/2e6
	near(t, "load1", s.V[KeyLoad1], 0.5)    //
	near(t, "psi.mem", s.V[KeyPSIMem], 1.5) // avg10
	near(t, "rx", s.V[PrefixNetRx+"eth0"], 100000)
	near(t, "tx", s.V[PrefixNetTx+"eth0"], 200000)
	near(t, "err", s.V[PrefixNetErr+"eth0"], 0.5)
	near(t, "disk.r", s.V[PrefixDiskR+"sda"], 20000*512/10)
	near(t, "disk.w", s.V[PrefixDiskW+"sda"], 40000*512/10)
	near(t, "busy", s.V[PrefixDiskBusy+"sda"], 20)
	if _, ok := s.V[PrefixDiskR+"loop0"]; ok {
		t.Error("loop devices are not physical disks")
	}
	if _, ok := s.V[PrefixDiskR+"sda1"]; ok {
		t.Error("partitions are not reported as disks")
	}
	if b.Memory().AvailableBytes != 6000000*1024 || b.Memory().Estimated {
		t.Errorf("MemAvailable: %+v", b.Memory())
	}
	in := s.Ifaces["eth0"]
	if in.Kind != "physical" || in.State != "up" || *in.SpeedMbps != 1000 || s.Ifaces["lo"].Kind != "loopback" {
		t.Errorf("interfaces: %+v %+v", in, s.Ifaces["lo"])
	}
}

func TestCountersResetsNeverSpike(t *testing.T) {
	a := ReadRaw(sys(stat2, netdev(5_000_000_000, 5_000_000_000, 10), diskstats(900000, 900000, 5000), "b1"), 0)
	t.Run("counter went backwards (driver reset or wrap)", func(t *testing.T) {
		b := ReadRaw(sys(stat2, netdev(1000, 1000, 0), diskstats(100, 100, 10), "b1"), 10_000)
		s := Rates(a, b)
		for _, k := range []string{PrefixNetRx + "eth0", PrefixNetTx + "eth0", PrefixNetErr + "eth0", PrefixDiskR + "sda", PrefixDiskW + "sda"} {
			if v, ok := s.V[k]; ok {
				t.Errorf("%s = %v after a reset; want no value", k, v)
			}
		}
	})
	t.Run("reboot (new boot id)", func(t *testing.T) {
		b := ReadRaw(sys(stat1, netdev(9_000_000_000, 9_000_000_000, 10), diskstats(999999999, 9, 9), "b2"), 10_000)
		s := Rates(a, b)
		if _, ok := s.V[KeyCPU]; ok {
			t.Error("CPU computed across a reboot")
		}
		if _, ok := s.V[PrefixNetRx+"eth0"]; ok {
			t.Error("traffic computed across a reboot")
		}
		near(t, "gauges still reported", s.V[KeyMem], 25)
	})
	t.Run("implausible rate beyond link speed", func(t *testing.T) {
		b := ReadRaw(sys(stat2, netdev(5_000_000_000+10_000_000_000, 5_000_000_000, 10), diskstats(900000, 900000, 5000), "b1"), 10_000)
		if v, ok := Rates(a, b).V[PrefixNetRx+"eth0"]; ok {
			t.Errorf("8 Gbit/s on a 1 Gbit/s link accepted: %v", v)
		}
	})
	t.Run("interface replaced (new ifindex)", func(t *testing.T) {
		f := sys(stat2, netdev(5_000_001_000, 5_000_001_000, 10), diskstats(900000, 900000, 5000), "b1")
		f.Files["/sys/class/net/eth0/ifindex"] = "7\n"
		if _, ok := Rates(a, ReadRaw(f, 10_000)).V[PrefixNetRx+"eth0"]; ok {
			t.Error("rate computed across a replaced interface")
		}
	})
	t.Run("disk replaced (new device number)", func(t *testing.T) {
		f := sys(stat2, netdev(5_000_001_000, 5_000_001_000, 10), diskstats(900100, 900100, 5010), "b1")
		f.Files["/sys/block/sda/dev"] = "8:16\n"
		if _, ok := Rates(a, ReadRaw(f, 10_000)).V[PrefixDiskR+"sda"]; ok {
			t.Error("rate computed across a replaced disk")
		}
	})
	t.Run("CPU hotplug changes the core list", func(t *testing.T) {
		b := ReadRaw(sys("cpu  1600 0 600 8400 600 0 0 0 0 0\ncpu0 1100 0 250 4000 250 0 0 0 0 0\nbtime 1700000000\n",
			netdev(5_000_001_000, 5_000_001_000, 10), diskstats(900000, 900000, 5000), "b1"), 10_000)
		s := Rates(a, b)
		if len(s.Cores) != 0 {
			t.Errorf("per-core rates across a CPU hotplug: %v", s.Cores)
		}
		if _, ok := s.V[KeyCPU]; !ok {
			t.Error("total CPU should still be computed")
		}
	})
}

func TestMemoryWithoutMemAvailable(t *testing.T) {
	f := sys(stat1, netdev(0, 0, 0), "", "b")
	f.Files["/proc/meminfo"] = "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 100 kB\nCached: 300 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"
	m := ReadRaw(f, 0).Memory()
	if !m.Estimated || m.AvailableBytes != 500*1024 || m.SwapPercent != nil {
		t.Fatalf("%+v", m)
	}
}

func TestMountinfoAndFilesystems(t *testing.T) {
	mi := strings.Join([]string{
		`22 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw`,
		`23 22 8:1 / /boot/efi rw,relatime shared:2 - vfat /dev/sda1 rw`,
		`24 22 0:21 / /proc rw - proc proc rw`,
		`25 22 0:30 / /run rw - tmpfs tmpfs rw`,
		`26 22 8:2 /var/lib/docker /var/lib/docker rw - ext4 /dev/sda2 rw`,
		`27 22 8:17 / /mnt/my\040disk ro,relatime - ext4 /dev/sdb1 rw`,
		`28 22 0:50 / /mnt/nas rw - nfs4 nas:/export rw`,
		`29 22 0:40 / /var/lib/docker/overlay2/x/merged rw - overlay overlay rw`,
		`30 22 179:2 / /media/sd rw - ext4 /dev/mmcblk0p2 ro,errors=remount-ro`,
	}, "\n")
	ms := ParseMountinfo([]byte(mi))
	var pts []string
	for _, m := range ms {
		pts = append(pts, m.Point)
	}
	if strings.Join(pts, ",") != "/,/boot/efi,/media/sd,/mnt/my disk" {
		t.Fatalf("mounts: %v", pts)
	}
	if !ms[2].ReadOnly || !ms[3].ReadOnly || ms[0].ReadOnly {
		t.Fatalf("read-only flags (mount and superblock options): %+v", ms)
	}
	f := fake(map[string]string{"/proc/self/mountinfo": mi}, nil)
	f.FS = map[string]*source.Statfs{"/": {Blocks: 1000, Bfree: 300, Bavail: 250, Bsize: 4096, Files: 100, Ffree: 40},
		"/boot/efi": {Blocks: 100, Bfree: 90, Bavail: 90, Bsize: 4096}}
	f.FSErr = map[string]error{"/media/sd": source.ErrUnresponsive}
	prev := []FS{{Mount: "/media/sd", UsedPercent: 50, TotalBytes: 1}}
	fs, err := Filesystems(context.Background(), f, prev)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 3 {
		t.Fatalf("%+v", fs)
	}
	root := fs[0]
	// df semantics: 700 used / (700 + 250 available to users).
	near(t, "used%", root.UsedPercent, 100*700.0/950)
	near(t, "inodes%", *root.InodesPercent, 60)
	if root.TotalBytes != 1000*4096 || root.AvailBytes != 250*4096 {
		t.Errorf("%+v", root)
	}
	if fs[1].InodesPercent != nil {
		t.Error("vfat has no inode counts: unknown, not 0%")
	}
	if !fs[2].Unresponsive || fs[2].UsedPercent != 50 {
		t.Errorf("a hung filesystem keeps its last values and is marked: %+v", fs[2])
	}
}

func TestSensors(t *testing.T) {
	t.Run("hwmon with hardware thresholds", func(t *testing.T) {
		f := fake(map[string]string{
			"/sys/class/hwmon/hwmon0/name": "coretemp\n", "/sys/class/hwmon/hwmon0/temp1_input": "54000\n",
			"/sys/class/hwmon/hwmon0/temp1_label": "Package id 0\n", "/sys/class/hwmon/hwmon0/temp1_max": "80000\n",
			"/sys/class/hwmon/hwmon0/temp1_crit": "100000\n", "/sys/class/hwmon/hwmon0/temp2_input": "51000\n",
			"/sys/class/hwmon/hwmon0/temp2_label": "Core 0\n",
			"/sys/class/hwmon/hwmon1/name":        "nvme\n", "/sys/class/hwmon/hwmon1/temp1_input": "38850\n",
			"/sys/class/hwmon/hwmon1/temp1_label": "Composite\n", "/sys/class/hwmon/hwmon1/temp1_crit": "84850\n",
			"/sys/class/hwmon/hwmon2/name": "k10temp\n", "/sys/class/hwmon/hwmon2/temp1_input": "-273000\n", // broken sensor
			"/sys/class/hwmon/hwmon3/name": "acpitz\n", "/sys/class/hwmon/hwmon3/temp1_input": "27800\n",
		}, map[string]string{"/sys/class/hwmon/hwmon1/device": "/sys/devices/pci0000:00/0000:3d:00.0/nvme/nvme0",
			"/sys/block/nvme0n1": "/sys/devices/pci0000:00/0000:3d:00.0/nvme/nvme0/nvme0n1"})
		s := Sensors(context.Background(), f)
		if len(s) != 4 {
			t.Fatalf("%+v", s)
		}
		pkg := s[0]
		if pkg.ID != "coretemp/Package id 0" || pkg.Kind != "cpu" || pkg.Celsius != 54 || *pkg.High != 80 || *pkg.Critical != 100 {
			t.Errorf("%+v", pkg)
		}
		if s[1].High != nil || s[1].Critical != nil {
			t.Errorf("no thresholds were provided for Core 0; none must be invented: %+v", s[1])
		}
		nv := s[2]
		if nv.Kind != "storage" || nv.Device != "nvme0n1" || nv.ID != "nvme:nvme0n1/Composite" || nv.High != nil || nv.HW != "nvme0" {
			t.Errorf("%+v", nv)
		}
		if pkg.HW != "" {
			t.Errorf("no device link: no hardware identity: %+v", pkg)
		}
		if s[3].Kind != "board" || s[3].Label != "temp1" {
			t.Errorf("%+v", s[3])
		}
	})
	t.Run("no sensors at all is normal", func(t *testing.T) {
		if s := Sensors(context.Background(), fake(map[string]string{}, nil)); len(s) != 0 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("thermal zone fallback (Raspberry Pi style)", func(t *testing.T) {
		s := Sensors(context.Background(), fake(map[string]string{
			"/sys/class/thermal/thermal_zone0/type": "cpu-thermal\n", "/sys/class/thermal/thermal_zone0/temp": "48312\n",
			"/sys/class/thermal/thermal_zone0/trip_point_0_type": "critical\n", "/sys/class/thermal/thermal_zone0/trip_point_0_temp": "110000\n",
		}, nil))
		if len(s) != 1 || s[0].Kind != "cpu" || *s[0].Critical != 110 {
			t.Fatalf("%+v", s)
		}
	})
}

func TestGPUs(t *testing.T) {
	base := map[string]string{
		"/sys/bus/pci/devices/0000:00:02.0/class": "0x030000\n", "/sys/bus/pci/devices/0000:00:02.0/vendor": "0x8086\n",
		"/sys/bus/pci/devices/0000:00:02.0/device": "0x9a49\n",
		"/sys/bus/pci/devices/0000:01:00.0/class":  "0x030000\n", "/sys/bus/pci/devices/0000:01:00.0/vendor": "0x10de\n",
		"/sys/bus/pci/devices/0000:01:00.0/device": "0x2684\n",
		"/sys/bus/pci/devices/0000:03:00.0/class":  "0x030000\n", "/sys/bus/pci/devices/0000:03:00.0/vendor": "0x1002\n",
		"/sys/bus/pci/devices/0000:03:00.0/device": "0x73bf\n", "/sys/bus/pci/devices/0000:03:00.0/gpu_busy_percent": "37\n",
		"/sys/bus/pci/devices/0000:03:00.0/mem_info_vram_used": "1073741824\n", "/sys/bus/pci/devices/0000:03:00.0/mem_info_vram_total": "17179869184\n",
		"/sys/bus/pci/devices/0000:03:00.0/hwmon/hwmon5/temp1_input": "61000\n", "/sys/bus/pci/devices/0000:03:00.0/hwmon/hwmon5/power1_average": "45000000\n",
		"/sys/bus/pci/devices/0000:04:00.0/class": "0x030000\n", "/sys/bus/pci/devices/0000:04:00.0/vendor": "0x1234\n", // QEMU VGA
		"/sys/bus/pci/devices/0000:05:00.0/class": "0x020000\n", "/sys/bus/pci/devices/0000:05:00.0/vendor": "0x10de\n", // not a display device
	}
	links := map[string]string{"/sys/bus/pci/devices/0000:00:02.0/driver": "/sys/bus/pci/drivers/i915",
		"/sys/bus/pci/devices/0000:03:00.0/driver": "/sys/bus/pci/drivers/amdgpu", "/sys/bus/pci/devices/0000:01:00.0/driver": "/sys/bus/pci/drivers/nvidia"}
	t.Run("nvidia-smi missing", func(t *testing.T) {
		g, _ := GPUs(context.Background(), fake(base, links))
		if len(g) != 3 {
			t.Fatalf("%+v", g)
		}
		nv := g[1]
		if nv.Vendor != "nvidia" || nv.UtilPercent != nil || nv.Celsius != nil || !strings.Contains(nv.Note, "GPU telemetry unavailable") {
			t.Fatalf("missing tooling must be 'unavailable', never zeros: %+v", nv)
		}
		amd := g[2]
		if *amd.UtilPercent != 37 || *amd.MemTotal != 16<<30 || *amd.Celsius != 61 || *amd.PowerW != 45 {
			t.Fatalf("%+v", amd)
		}
		in := g[0]
		if in.Vendor != "intel" || in.UtilPercent != nil || !strings.Contains(in.Note, "utilization") {
			t.Fatalf("%+v", in)
		}
	})
	t.Run("nvidia-smi present", func(t *testing.T) {
		f := fake(base, links)
		f.Tools["nvidia-smi"] = true
		c := HWSpec.Commands[0]
		f.Cmds[source.Key(c[0], c[1:]...)] = &source.Result{Stdout: []byte("0, NVIDIA GeForce RTX 4090, 12, 2048, 24564, 44, [N/A], P8\n")}
		g, note := GPUs(context.Background(), f)
		nv := g[1]
		if note != "" || nv.Model != "NVIDIA GeForce RTX 4090" || *nv.UtilPercent != 12 || *nv.MemUsed != 2048<<20 || *nv.Celsius != 44 ||
			nv.PowerW != nil || nv.PowerState != "P8" {
			t.Fatalf("[N/A] power must be missing, not 0: %+v", nv)
		}
	})
	t.Run("stable GPU identity", func(t *testing.T) {
		f := fake(base, links)
		f.Files["/sys/bus/pci/devices/0000:03:00.0/unique_id"] = "4a1c20b3e1d07a2f\n"
		f.Tools["nvidia-smi"] = true
		c := HWSpec.Commands[0]
		f.Cmds[source.Key(c[0], c[1:]...)] = &source.Result{Stdout: []byte("0, NVIDIA GeForce RTX 4090, 99, 2048, 24564, 71, 410.2, P2, GPU-8f3b5c2e-1111-2222-3333-444455556666, 00000000:01:00.0\n")}
		g, _ := GPUs(context.Background(), f)
		if g[1].UUID != "GPU-8f3b5c2e-1111-2222-3333-444455556666" || g[1].PCI != "0000:01:00.0" || *g[1].UtilPercent != 99 {
			t.Fatalf("NVIDIA GPU UUID and bus id: %+v", g[1])
		}
		if g[2].UUID != "amd:4a1c20b3e1d07a2f" || g[2].PCI != "0000:03:00.0" {
			t.Fatalf("AMD unique_id: %+v", g[2])
		}
		if g[0].UUID != "" || g[0].PCI != "0000:00:02.0" {
			t.Fatalf("no UUID: only the PCI address, honestly weaker: %+v", g[0])
		}
	})
	t.Run("malformed nvidia-smi output", func(t *testing.T) {
		if _, err := ParseNvidiaSMI([]byte("garbage line\n")); err == nil {
			t.Fatal("malformed output accepted")
		}
		f := fake(base, links)
		f.Tools["nvidia-smi"] = true
		c := HWSpec.Commands[0]
		f.Cmds[source.Key(c[0], c[1:]...)] = &source.Result{Exit: 9}
		g, note := GPUs(context.Background(), f)
		if !strings.Contains(note, "GPU telemetry unavailable") || g[1].UtilPercent != nil || g[2].UtilPercent == nil {
			t.Fatalf("one failing vendor collector must not hide the others: %q %+v", note, g)
		}
	})
	t.Run("no GPU", func(t *testing.T) {
		g, note := GPUs(context.Background(), fake(map[string]string{}, nil))
		if g != nil || note != "" {
			t.Fatal("no GPU must mean nothing to show")
		}
	})
}
