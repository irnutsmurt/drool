# First login

The first time you open DROOL, you claim it: you prove you're the person who installed it (with
the one-time setup code from its log) and create the first administrator account. After that,
setup is closed for good, and everyone signs in with a username and password.

## Before you start

- DROOL is installed and running ([Installing DROOL](Installing-DROOL.md)).
- You have its **setup code**: the six digits in DROOL's startup message, for example `482913`.
  The code works once and expires 60 minutes after DROOL started. [Installing DROOL](Installing-DROOL.md#finding-the-setup-code)
  shows where to find it, and how to get a new one.

## 1. Open DROOL in your browser

Go to the address from the startup message, for example `https://drool.example.net:8080/setup`.
Note the **https**: DROOL always encrypts the connection.

Your browser will warn that the connection isn't private or the certificate isn't trusted. That's
expected the first time: DROOL created its own (*self-signed*) certificate, which your browser
can't check against a public authority. Continue to the site (in Chrome: **Advanced → Proceed**; in
Firefox: **Advanced → Accept the Risk and Continue**).

If you want to be sure you're talking to your DROOL and not something in between, compare the
certificate's SHA-256 fingerprint in your browser's certificate viewer with the
`Certificate SHA-256:` line in DROOL's startup message. They must match. You can replace the
self-signed certificate with your own later ([Configuration](Configuration.md#your-own-tls-certificate)).

## 2. Create the administrator

![The "Set up DROOL" page with the example code 482913 and the username alex filled in](images/first-login-setup.png)

| Field | What to enter |
|---|---|
| **Setup code** | The code from the startup message, e.g. `482913`. Spaces and dashes are ignored. |
| **Admin username** | Your username. It starts as `admin`; you can keep that or use your own name. The examples use `alex`. |
| **Password** / **Confirm password** | At least 12 characters. Use a password manager. |

Click **Create administrator**. DROOL creates the account, signs you in and opens the **Overview**.

If DROOL says **invalid or expired setup code**, the code was mistyped, already used, or older
than 60 minutes. Get a fresh one ([how](Installing-DROOL.md#finding-the-setup-code)) and try again.

## 3. Find your way around

![The Overview after the first hosts were added: cards for hosts, alerts, runs, desired state and firewall, and a Security card](images/overview.png)

- **The sidebar** lists every area under plain headings: **Overview**; *Fleet* — **Hosts**,
  **Packs** (groups of hosts), **Shepherds** (DROOL agents); *Observe* — **Monitoring**, **Alerts**,
  **Network Activity**; *Automate* — **Runs**, **Schedules**, **Workflows**, **Desired State**;
  *Protect* — **Firewall**, **Credentials**, **Audit Log**; and **Settings**. Hover over Packs or
  Shepherds to see what those names mean.
- **The Overview** answers "does anything need me right now?": how many hosts are online, open
  alerts, runs in progress, desired-state drift, the firewall, and a **Security** card.
- **Search:** press **Ctrl+K** (**⌘K** on a Mac) or **/**, or click *Search pages and settings…* at
  the top, and type what you're after in your own words — "api token", "threshold", "can A reach
  B", "who changed". Search finds pages and settings (not individual hosts or runs), and only those
  your role may open.
- **The security indicator** at the top (for example *2 security issues*) stays visible until the
  things it lists are handled. On a new installation it usually says that two-factor sign-in is off
  and that the *Agent Root CA private key is on this controller*. Neither is urgent; both are
  explained in [Secure Your Installation](Secure-Your-Installation.md).
- **Light, Dark, System** at the top switches the colour theme (saved in this browser).
- **Your name** and **Sign out** are at the top right; your own settings are under
  **Settings → Account**.

## Signing in later

Go to `https://drool.example.net:8080` (your address); DROOL shows **Sign in to DROOL**. Enter your
username and password. If you've turned on two-factor authentication, DROOL then asks for the
6-digit code from your authenticator app (or one of your recovery codes).

A session ends after 12 hours without activity, and after 7 days at most; then you sign in again.

## If you forget your password

- **Another administrator can't reset it** in the web interface. They can delete your account and
  create a new one under **Settings → Users** (your history in the audit log stays).
- **If you're the only administrator**, create a new administrator on the controller itself, then
  sign in with it:

  | Installation | Command |
  |---|---|
  | Docker Compose | `docker compose exec drool drool user create --username alex2 --role admin` |
  | Podman | `podman exec -it drool drool user create --username alex2 --role admin` |
  | Package or archive | `sudo -u drool drool user create --username alex2 --role admin` |

  It asks for the new account's password. (`alex2` is just an example name; any unused username
  works.)

Lost your authenticator app instead? See [Two-Factor Authentication](Two-Factor-Authentication.md#if-you-lose-your-authenticator).

## Next

1. [Secure Your Installation](Secure-Your-Installation.md) — at least turn on two-factor sign-in.
2. [Your First Host](Your-First-Host.md) — connect your first machine.
