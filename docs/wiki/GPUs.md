# GPUs

For hosts with a graphics card (an AI box, a render node, a media server that transcodes), DROOL shows
each GPU's utilization, memory, temperature and power use, where the driver provides them. In the
examples, `gpu-01` has an NVIDIA GeForce RTX 4090 and an AMD integrated GPU.

## Where to look

The **Sensors & GPU** tab on the host's monitoring page has a **GPU** card per graphics card: its
name and how it's read (*nvidia, via nvidia-smi*; *amd, via sysfs*), **Utilization**, **VRAM** (used
of total), **Temperature** and **Power**. The **Metrics** tab has GPU charts.

![gpu-01's Sensors & GPU tab with the NVIDIA GeForce RTX 4090 (91% utilization, 19 GB of 24 GB VRAM, 71 °C, 311 W) and an AMD iGPU whose driver doesn't provide telemetry](images/monitoring-sensors-gpu.png)

## Before you start

- GPU data comes from the vendor's tools where they're installed (`nvidia-smi`, part of NVIDIA's
  driver) or from the kernel (AMD, Intel). **DROOL never installs GPU drivers.**
- Some drivers report little: the example's AMD iGPU says *GPU telemetry unavailable from the amdgpu
  driver*, with only its temperature available.

## When does DROOL call it a problem?

- **High GPU utilization** is a check you can turn on, and it's **off by default**: a busy GPU is
  usually *working*, not failing. Turn it on for machines where sustained saturation means trouble
  ([Health Overrides](Health-Overrides.md)). Turning it on or off never affects GPU temperature, disk
  health or anything else.
- **GPU temperature** is judged like any sensor, against the hardware's limits. NVIDIA GPUs report no
  limit, so set one yourself if you want their temperature judged ([Temperatures](Temperatures.md)).
- GPU hardware or driver errors (such as NVIDIA's Xid errors) aren't collected.

## Related

[Temperatures](Temperatures.md) · [Health Overrides](Health-Overrides.md)
