# Reloading or restarting a service after a deployment

Most services read their configuration when they start. After deploying a new configuration file,
a file deployment can **reload or restart the service** for you, but only on hosts where the file
actually changed.

## Before you start

- The service must be a systemd service on the hosts, and the account must be able to manage it
  (sudo, or a Shepherd preset that allows services: [Managing Services](Managing-Services.md)).
- **Reload or restart?**
  - **Reload a service** — the service rereads its configuration without stopping (nginx, sshd,
    many others). Connections stay up. Only works for services that support reloading.
  - **Restart a service** — stop and start. Use it for services that don't support reloading.
    Clients are disconnected briefly.

## Steps

In the deployment form, under **Then**, choose **Reload a service** or **Restart a service**, and
type the **Service** name (for example `jellyfin`; `.service` is added if you leave it out). Combine it
with a [validation command](Validation-Before-Deployment.md), so a broken file never reaches the
service.

## What happens

- **Only where the file changed.** Hosts where the file was already identical are left alone, and so
  is their service.
- **The service result is reported separately** from the file's. If the reload or restart fails, the
  run says the file **was installed** but the service action failed, so you know exactly what state
  the host is in: new file in place, service not (yet) using it.
- A restarted service must still be running two seconds later, as with every DROOL restart
  ([Managing Services](Managing-Services.md#how-drool-checks-the-result)).

## If the service fails afterwards

The new file is in place, and the service is unhappy with it. Your options:

- **Roll back** the file from the deployment's page ([Rolling Back a File](Rolling-Back-a-File.md)):
  the rollback restores the previous file and then reloads or restarts the service the same way;
- or fix the file and deploy again.

The run output includes the service's recent log lines, which usually name the problem.

## Related

[Deploying a File to Multiple Hosts](Deploying-a-File-to-Multiple-Hosts.md) · [Managing Services](Managing-Services.md) ·
[Service Won't Start](Service-Wont-Start.md)
