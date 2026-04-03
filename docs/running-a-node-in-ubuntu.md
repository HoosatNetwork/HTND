# Running a HTND Node on Ubuntu 24.04

This guide walks a Linux newcomer through setting up and running a **HTND** node on **Ubuntu 24.04**. By the end you will have a node running reliably under its own user account, managed either by a `screen` session or a `systemd` service.

---

## 1. Create a Dedicated Linux User

Running node software under its own account is a good security habit. It limits what the process can access if something goes wrong.

```bash
sudo adduser htnd
```

You will be prompted for a password and a few optional details (full name, phone number, etc.). Press **Enter** to skip the optional fields.

Switch to the new user:

```bash
su - htnd
```

All remaining commands in this guide should be run **as the `htnd` user** unless stated otherwise.

---

## 2. Identify Your CPU Architecture

HTND provides separate builds for different CPU types. Run the following to check yours:

```bash
uname -m
```

| Output       | Architecture | Release to download |
|--------------|--------------|---------------------|
| `x86_64`     | AMD / Intel  | **AMD64**           |
| `aarch64`    | ARM 64-bit   | **AARCH64**         |

---

## 3. Download the Correct Release

Use `wget` to download the matching archive directly to your home directory.

**AMD64 (x86\_64):**

```bash
wget -P ~/ https://github.com/HoosatNetwork/HTND/releases/download/v1.7.0/HTND-1.7.0-linux-amd64.tar.gz
```

**AARCH64 (arm64):**

```bash
wget -P ~/ https://github.com/HoosatNetwork/HTND/releases/download/v1.7.0/HTND-1.7.0-linux-aarch64.tar.gz
```

---

## 4. Extract HTND into `~/bin/`

Create the `bin` directory if it does not already exist, then extract the archive into it:

```bash
mkdir -p ~/bin
tar -xzf ~/HTND-1.7.0-linux-amd64.tar.gz -C ~/bin/
```

> **Note:** Replace `amd64` with `aarch64` in the filename above if you downloaded the AARCH64 release.

Make the binary executable:

```bash
chmod +x ~/bin/HTND
```

Verify it is in place:

```bash
ls -lh ~/bin/HTND
```

---

## 5. Run HTND

The minimum command to start a node is:

```bash
~/bin/HTND --saferpc
```

The `--saferpc` flag restricts the RPC interface to local connections only, which is the safest default for most setups.

---

## 6. Running HTND in a `screen` Session

`screen` lets you run HTND in a terminal session that stays alive after you disconnect from SSH.

Install `screen` if it is not already present (run this as a sudo-capable user):

```bash
sudo apt install screen
```

Start a named screen session:

```bash
screen -S htnd
```

Inside the screen session, start HTND:

```bash
~/bin/HTND --saferpc
```

To **detach** from the session while leaving HTND running, press:

```
Ctrl + A, then D
```

To **reattach** later and see the live output:

```bash
screen -r htnd
```

To **list** all running screen sessions:

```bash
screen -ls
```

---

## 7. Running HTND as a `systemd` Service

A `systemd` service starts HTND automatically at boot and restarts it if it crashes. This is the recommended approach for a long-running node.

### 7.1 Create the Service File

> **Note:** This step requires `sudo` and must be run as your regular admin user, not as `htnd`.

```bash
sudo nano /etc/systemd/system/htnd.service
```

Paste the following content:

```ini
[Unit]
Description=HTND Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=htnd
ExecStart=/home/htnd/bin/HTND --saferpc
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

Save and close the file (`Ctrl + O`, then `Ctrl + X` in nano).

### 7.2 Enable and Start the Service

Reload the systemd configuration so it picks up the new file:

```bash
sudo systemctl daemon-reload
```

Enable the service to start automatically at boot:

```bash
sudo systemctl enable htnd
```

Start the service now:

```bash
sudo systemctl start htnd
```

Check that it is running:

```bash
sudo systemctl status htnd
```

---

## 8. Inspecting Logs

### systemd / journalctl

When HTND is running as a `systemd` service, all output is captured by the system journal.

View the most recent log entries:

```bash
journalctl -u htnd -n 100
```

Follow the log in real time (like `tail -f`):

```bash
journalctl -u htnd -f
```

Show logs from the current boot only:

```bash
journalctl -u htnd -b
```

### screen session

If HTND is running inside a `screen` session, reattach to it to see the live output:

```bash
screen -r htnd
```

Scroll up through the buffer with `Ctrl + A`, then `[`. Press `Q` to exit scroll mode.

---

## Summary

| Step | Command |
|------|---------|
| Create user | `sudo adduser htnd` |
| Check architecture | `uname -m` |
| Download release | `wget -P ~/ <url>` |
| Extract to `~/bin/` | `tar -xzf ~/HTND-*.tar.gz -C ~/bin/` |
| Run manually | `~/bin/HTND --saferpc` |
| Run in screen | `screen -S htnd` then `~/bin/HTND --saferpc` |
| Enable as service | `sudo systemctl enable --now htnd` |
| Follow logs (systemd) | `journalctl -u htnd -f` |
| Reattach to screen | `screen -r htnd` |
