# Installing and running

BrokerBuddy is one self-contained program. There is nothing to install besides the program itself: no database server, no runtime, no C compiler. Pick the option that suits your computer.

| Platform | Easiest option |
|----------|----------------|
| [Windows](#windows) | Download the zip from Releases, run `brokerbuddy.exe` |
| [macOS](#macos) | Download the tar.gz from Releases, run `./brokerbuddy` |
| [Linux](#linux) | Download the tar.gz from Releases, or use Docker |
| [Docker](#docker) | `docker run` the image from GitHub Container Registry |
| [Raspberry Pi and NAS devices](#raspberry-pi-and-nas-devices) | The linux/arm64 download or the Docker image |
| [From source](#build-from-source) | `go run .` on any platform Go supports |

Every option ends the same way: open **https://127.0.0.1:8443** in a browser, accept the one-time warning for the self-signed certificate, and carry on at [Getting started](Getting-Started).

## Downloads

Each tagged release on the repository's **Releases** page has one archive per platform, plus `SHA256SUMS.txt`:

| File | For |
|------|-----|
| `brokerbuddy-<version>-windows-amd64.zip` | Windows on Intel or AMD |
| `brokerbuddy-<version>-windows-arm64.zip` | Windows on ARM |
| `brokerbuddy-<version>-darwin-arm64.tar.gz` | Macs with Apple silicon (M1 and later) |
| `brokerbuddy-<version>-darwin-amd64.tar.gz` | Intel Macs |
| `brokerbuddy-<version>-linux-amd64.tar.gz` | Linux on Intel or AMD |
| `brokerbuddy-<version>-linux-arm64.tar.gz` | Linux on ARM (Raspberry Pi 4 and 5, many NAS boxes) |

To check a download, compare its hash with `SHA256SUMS.txt`:

```
sha256sum -c SHA256SUMS.txt --ignore-missing      # Linux
shasum -a 256 -c SHA256SUMS.txt --ignore-missing  # macOS
Get-FileHash .\brokerbuddy-v1.0.0-windows-amd64.zip   # Windows PowerShell
```

The binaries are not code-signed, so your operating system may warn the first time (see each platform below).

Run `brokerbuddy -version` to see which version you have.

## Windows

1. Download the `windows-amd64` zip (or `windows-arm64` on ARM) from Releases and extract it, for example to `C:\BrokerBuddy`.
2. Open PowerShell in that folder and run:

   ```
   .\brokerbuddy.exe
   ```

   Your data (`brokerbuddy.db` and `brokerbuddy.db.key`) is created in the folder you ran it from. To keep it elsewhere, use `-db`:

   ```
   .\brokerbuddy.exe -db "$env:USERPROFILE\Documents\BrokerBuddy\brokerbuddy.db"
   ```

3. Open https://127.0.0.1:8443.

**SmartScreen warning.** Windows may say it protected your PC because the program is unrecognised. Choose **More info → Run anyway** if you downloaded it from this repository's Releases page and the hash matches.

**Firewall prompt.** The app listens on the loopback address only by default, so no prompt should appear. If you choose a network mode in Settings, allow it when asked.

**Run it at sign-in.** Create a shortcut to `brokerbuddy.exe` in `shell:startup`, or use Task Scheduler with the trigger "At log on" and the start-in folder set to where the data lives.

## macOS

1. Download the `darwin-arm64` archive (Apple silicon) or `darwin-amd64` (Intel). If unsure, **Apple menu → About This Mac** shows the chip.
2. Extract and run it from Terminal:

   ```
   tar -xzf brokerbuddy-*-darwin-*.tar.gz
   cd brokerbuddy-*-darwin-*
   ./brokerbuddy
   ```

3. Open https://127.0.0.1:8443.

**Gatekeeper.** Because the program is unsigned and was downloaded, macOS may refuse to open it ("cannot be opened because the developer cannot be verified"). Remove the quarantine flag once, after checking the hash:

```
xattr -d com.apple.quarantine ./brokerbuddy
```

**Run it at login.** Create `~/Library/LaunchAgents/au.brokerbuddy.plist`:

```
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>au.brokerbuddy</string>
  <key>ProgramArguments</key><array>
    <string>/Users/you/brokerbuddy/brokerbuddy</string>
    <string>-db</string><string>/Users/you/brokerbuddy/brokerbuddy.db</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
```

then `launchctl load ~/Library/LaunchAgents/au.brokerbuddy.plist`. Change `/Users/you` to your own folder.

## Linux

1. Download the `linux-amd64` (or `linux-arm64`) archive and extract it:

   ```
   tar -xzf brokerbuddy-*-linux-*.tar.gz
   cd brokerbuddy-*-linux-*
   ./brokerbuddy
   ```

2. Open https://127.0.0.1:8443.

If your user cannot run it, make it executable with `chmod +x brokerbuddy`.

**Run it as a service (systemd).** Put the binary in `/opt/brokerbuddy`, create a user with `sudo useradd --system --home /var/lib/brokerbuddy --create-home brokerbuddy`, then create `/etc/systemd/system/brokerbuddy.service`:

```
[Unit]
Description=BrokerBuddy
After=network-online.target

[Service]
User=brokerbuddy
WorkingDirectory=/var/lib/brokerbuddy
ExecStart=/opt/brokerbuddy/brokerbuddy -db /var/lib/brokerbuddy/brokerbuddy.db
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

and enable it:

```
sudo systemctl daemon-reload
sudo systemctl enable --now brokerbuddy
```

To use Let's Encrypt on ports 80 and 443 as a non-root user, allow the binary to bind low ports: `sudo setcap cap_net_bind_service=+ep /opt/brokerbuddy/brokerbuddy`, or add `AmbientCapabilities=CAP_NET_BIND_SERVICE` to the service.

## Docker

Images are published to GitHub Container Registry as `ghcr.io/<owner>/brokerbuddy` for `linux/amd64` and `linux/arm64`. Tags: `latest` (the newest release), a version such as `1.2.3` or `1.2`, and `edge` (the current main branch).

The image follows the [linuxserver.io](https://docs.linuxserver.io/general/understanding-puid-and-pgid/) conventions: you pass `PUID`, `PGID` and `TZ`, and mount a folder at `/data`.

### Docker Compose (recommended)

The repository has a `docker-compose.yml`:

```yaml
---
services:
  brokerbuddy:
    image: ghcr.io/<owner>/brokerbuddy:latest
    container_name: brokerbuddy
    pull_policy: always
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=Etc/UTC
    volumes:
      - /path/to/brokerbuddy/data:/data
    ports:
      - 127.0.0.1:8443:8443
    restart: unless-stopped
```

Edit it before the first start: change `/path/to/brokerbuddy/data` to a folder on your computer, and set `PUID`/`PGID` to your user's ids (run `id`). Then:

```
docker compose up -d
docker compose logs -f
docker compose pull && docker compose up -d     # update
docker compose down                             # stop (your data folder is kept)
```

### docker cli

```
docker run -d \
  --name=brokerbuddy \
  -e PUID=1000 \
  -e PGID=1000 \
  -e TZ=Etc/UTC \
  -p 127.0.0.1:8443:8443 \
  -v /path/to/brokerbuddy/data:/data \
  --restart unless-stopped \
  ghcr.io/<owner>/brokerbuddy:latest
```

Then open https://127.0.0.1:8443.

### Parameters

| Parameter | Function |
|-----------|----------|
| `-p 127.0.0.1:8443:8443` | The web interface (HTTPS). The `127.0.0.1` publishes it on this computer only. Inside a container the app listens on all interfaces, so `8443:8443` without it makes the app reachable from your network. With no password set, the app still refuses requests that do not use a localhost address, but set a password under Settings before exposing it. |
| `-e PUID=1000` | User id the app runs as. Files in `/data` are owned by it. |
| `-e PGID=1000` | Group id the app runs as. |
| `-e TZ=Etc/UTC` | Time zone, from [this list](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones#List), for example `Australia/Sydney`. |
| `-v /data` | Your database and key file. Without it your data is lost when the container is removed. A named volume (`-v brokerbuddy-data:/data`) also works. |

On start the container fixes the ownership of `/data` to `PUID:PGID` and then drops root, so a folder Docker creates for you, or a volume from an older image, needs no manual `chown`. The image includes CA certificates for lookups and Let's Encrypt.

Compose pulls the published image and never builds locally. To run an image you built yourself (`docker build -t brokerbuddy .`), set `image: brokerbuddy` and `pull_policy: never`.

### Day-to-day commands

```
docker logs -f brokerbuddy                 # watch the log
docker stop brokerbuddy                    # stop
docker start brokerbuddy                   # start again
docker pull ghcr.io/<owner>/brokerbuddy:latest
docker rm -f brokerbuddy                   # remove the old container (the volume stays)
```

After pulling, repeat the `docker run` command above to start the new version. Updating never touches your `/data` folder or volume, so your data carries over. Take a backup first (below) before a major version change.

### Demo mode in a container

```
docker run --rm -p 127.0.0.1:8443:8443 ghcr.io/<owner>/brokerbuddy:latest -db /tmp/demo.db -demo
```

The container's entrypoint already supplies `-db` and `-addr`; arguments you add are appended, and the last `-db` wins. This keeps the demo in a throwaway file.

### Backing up a container

With a folder mounted at `/data`, stop the container and copy the folder: it holds the database and the key file together, which is what you must keep.

With a named volume:

```
docker run --rm -v brokerbuddy-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/brokerbuddy-backup.tgz -C /data .
```

Store the archive somewhere safe. On Windows PowerShell use `${PWD}` in place of `"$PWD"`.

To restore it into a volume, stop the container first, then:

```
docker run --rm -v brokerbuddy-data:/data -v "$PWD":/backup alpine \
  tar xzf /backup/brokerbuddy-backup.tgz -C /data
```

The container sets the owner of the restored files to `PUID:PGID` when it next starts.

Alternatively, use the in-app database export and import under Settings, which needs no Docker commands.

### Building the image yourself

```
docker build -t brokerbuddy .
```

### Running without root

If you start the container with `--user "$(id -u):$(id -g)"` (or `user:` in Compose), it runs as that user from the start, ignores `PUID`/`PGID` and does not change any ownership. The folder mounted at `/data` must then already be writable by that user.

## Raspberry Pi and NAS devices

Use the `linux-arm64` download (64-bit operating systems) or the Docker image, which has an arm64 variant. 32-bit systems are not built; install the 64-bit operating system image or build from source with `GOARCH=arm`.

## Build from source

You need a recent [Go](https://go.dev/dl/) (the version is in `go.mod`). No C compiler is needed.

```
git clone https://github.com/<owner>/brokerbuddy.git
cd brokerbuddy
go run .
```

or build a binary:

```
go build -o brokerbuddy .
```

To cross-compile for another platform from any computer:

```
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o brokerbuddy-linux-arm64 .
```

On Windows PowerShell:

```
$env:GOOS="linux"; $env:GOARCH="arm64"; $env:CGO_ENABLED="0"; go build -o brokerbuddy-linux-arm64 .
```

## Choosing a port

If port 8443 is taken, pick another:

```
brokerbuddy -addr 127.0.0.1:9443
```

In Docker, change only the published side: `-p 127.0.0.1:9443:8443`.

## Updating

Replace the program (or pull the new image) and start it again. The database is upgraded in place and your transactions are reprocessed ([Categorisation](Categorisation#reprocessing-existing-transactions)). Back up the database and key file first. Your data is not inside the program or the container image.

## Stopping and uninstalling

Stop the process (Ctrl+C, or `docker stop brokerbuddy`). To uninstall, delete the program. Your data stays until you delete `brokerbuddy.db` and `brokerbuddy.db.key` (or the Docker volume with `docker volume rm brokerbuddy-data`).

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| "address already in use" | Another program uses the port. Choose another with `-addr`. |
| Browser cannot connect in Docker | Check the container is running (`docker ps`), and that you used `https://` (not `http://`). |
| 403 "only answers on this computer" | No password is set and you opened the app by a non-local address. Use `127.0.0.1` or `localhost`, or set a password from the local address. |
| Data missing after restart in Docker | The container ran without a volume. Always mount `/data`. |
| macOS or Windows refuses to open it | See the Gatekeeper and SmartScreen notes above. |

More in [FAQ and troubleshooting](FAQ-and-Troubleshooting).
